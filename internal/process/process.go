// Package process manages a detached xray-core child process. The
// process is identified by a PID file written to disk; restarts after
// the CLI exits are how the model becomes "daemon-like" without
// systemd.
package process

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// StartOptions configures a launch. All fields are required except APIPort.
type StartOptions struct {
	BinPath    string
	ConfigPath string
	PIDFile    string
	LogFile    string

	// ReadyPort is polled with a TCP dial to confirm xray actually
	// bound its inbound. Set to the SOCKS port.
	ReadyPort int
	// ReadyTimeout caps how long Start waits for the port to open.
	ReadyTimeout time.Duration

	// ExtraEnv is appended to os.Environ() before launch — typically
	// "XRAY_LOCATION_ASSET=…" so xray finds geoip.dat / geosite.dat in
	// the user-owned share directory instead of /usr/share/xray.
	ExtraEnv []string
}

// Start launches xray detached from the current terminal, writes the
// PID file, and blocks until the inbound port accepts connections.
//
// If xray exits before the port opens, the returned error wraps the
// last bytes of LogFile so the caller can show them to the user.
func Start(ctx context.Context, opts StartOptions) (int, error) {
	if err := ensureParentDirs(opts.PIDFile, opts.LogFile); err != nil {
		return 0, err
	}
	if running, pid := IsRunning(opts.PIDFile); running {
		return pid, fmt.Errorf("process: xray already running (pid %d)", pid)
	}

	logF, err := os.OpenFile(opts.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("process: open log: %w", err)
	}
	defer logF.Close()

	cmd := exec.Command(opts.BinPath, "-config", opts.ConfigPath)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Stdin = nil
	if len(opts.ExtraEnv) > 0 {
		cmd.Env = append(os.Environ(), opts.ExtraEnv...)
	}
	// Setsid detaches from our terminal so closing the shell that
	// launched us does not also kill xray.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("process: start %s: %w", opts.BinPath, err)
	}
	pid := cmd.Process.Pid
	if err := writePID(opts.PIDFile, pid); err != nil {
		_ = cmd.Process.Kill()
		return 0, err
	}
	// Release so we are not the parent waiting on a zombie.
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("process: release: %w", err)
	}

	if err := waitForPort(ctx, opts.ReadyPort, opts.ReadyTimeout, opts.PIDFile); err != nil {
		// xray failed to come up — clean up so the next invocation
		// does not see a stale PID file.
		_ = Stop(opts.PIDFile, 2*time.Second)
		tail, _ := tailFile(opts.LogFile, 4096)
		if tail != "" {
			return pid, fmt.Errorf("%w\nxray log tail:\n%s", err, tail)
		}
		return pid, err
	}
	return pid, nil
}

// Stop sends SIGTERM, waits up to gracePeriod, then SIGKILL if the
// process is still alive. The PID file is removed on success.
func Stop(pidFile string, gracePeriod time.Duration) error {
	pid, err := readPID(pidFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(pidFile)
		return nil
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) || strings.Contains(err.Error(), "process already finished") {
			_ = os.Remove(pidFile)
			return nil
		}
		// "no such process" — race with external kill
		_ = os.Remove(pidFile)
		return nil
	}

	deadline := time.Now().Add(gracePeriod)
	for time.Now().Before(deadline) {
		if !isAlive(pid) {
			_ = os.Remove(pidFile)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	// SIGTERM didn't take. Force-kill and *wait* for the process table
	// entry to actually clear before returning. Under memory pressure
	// the kernel can take hundreds of ms to deliver SIGKILL; if we
	// remove the PID file and return immediately, a follow-up
	// process.Start will pass IsRunning() and spawn a duplicate while
	// the old one is still alive — that is exactly how 30+ xray
	// orphans accumulated once.
	_ = proc.Signal(syscall.SIGKILL)
	killDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(killDeadline) {
		if !isAlive(pid) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = os.Remove(pidFile)
	return nil
}

// KillStraysByConfig SIGKILLs every xray-core process whose argv contains
// `-config <configPath>` and whose PID is NOT pidFile's recorded PID.
//
// This is a belt-and-braces cleanup: in theory, the Stop → Start sequence
// in launch() is enough to keep at most one xray per config alive. In
// practice, observed bugs (e.g. SIGKILL racing the PID-file removal, or
// `connect` running concurrently with the watchdog) have produced 30+
// orphan xrays. Scanning /proc and killing matches before each launch
// turns a logic bug into a self-healing event instead of a process leak.
//
// configPath is matched byte-for-byte against an argv entry. Returns the
// number of processes killed (best-effort: errors per-PID are swallowed).
func KillStraysByConfig(configPath, pidFile string) int {
	if configPath == "" {
		return 0
	}
	keep := 0
	if pid, err := readPID(pidFile); err == nil {
		keep = pid
	}

	procs, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	killed := 0
	for _, entry := range procs {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == keep || pid == os.Getpid() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		// /proc/<pid>/cmdline is NUL-separated argv. Split and look for
		// an exact `-config` + path pair so a substring collision can't
		// false-positive.
		argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if !matchesXrayConfig(argv, configPath) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			killed++
		}
	}
	return killed
}

// matchesXrayConfig returns true if argv looks like `xray ... -config <path>`.
func matchesXrayConfig(argv []string, configPath string) bool {
	if len(argv) == 0 {
		return false
	}
	// The binary path can be anything ending in /xray (system path) or
	// the bare name. Don't constrain it — we identify by the config arg.
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-config" && argv[i+1] == configPath {
			return true
		}
	}
	return false
}

// IsRunning reports whether the PID in the file refers to a live process.
// A stale PID file (process gone) is reported as not-running; callers
// can choose to remove it.
func IsRunning(pidFile string) (bool, int) {
	pid, err := readPID(pidFile)
	if err != nil {
		return false, 0
	}
	return isAlive(pid), pid
}

func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// signal 0 doesn't deliver — it only checks existence/permission.
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but isn't ours — still alive.
	return errors.Is(err, syscall.EPERM)
}

func writePID(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func readPID(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("process: malformed pid file %s: %w", path, err)
	}
	return pid, nil
}

func ensureParentDirs(paths ...string) error {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("process: mkdir for %s: %w", p, err)
		}
	}
	return nil
}

// waitForPort polls localhost:port until a TCP dial succeeds, the
// timeout elapses, or the underlying process exits.
func waitForPort(ctx context.Context, port int, timeout time.Duration, pidFile string) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if running, _ := IsRunning(pidFile); !running {
			return fmt.Errorf("process: xray exited before port %d opened", port)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process: port %d did not open within %s", port, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// tailFile returns up to maxBytes from the end of path. Best-effort —
// errors are swallowed because this is only used for error messages.
func tailFile(path string, maxBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	offset := int64(0)
	if size > maxBytes {
		offset = size - maxBytes
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return "", err
	}
	buf := make([]byte, size-offset)
	if _, err := f.Read(buf); err != nil {
		return "", err
	}
	return string(buf), nil
}
