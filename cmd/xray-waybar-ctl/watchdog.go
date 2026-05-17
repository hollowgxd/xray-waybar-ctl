package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/process"
	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
	"github.com/yourgfslove/xray-waybar-ctl/internal/sleepwatch"
	"github.com/yourgfslove/xray-waybar-ctl/internal/store"
	"github.com/yourgfslove/xray-waybar-ctl/internal/sysmode"
	"github.com/yourgfslove/xray-waybar-ctl/internal/tester"
)

// watchdogLock is held during a watchdog tick so a 10s timer-driven run
// cannot race a user-driven `connect` / `reconnect`. Non-blocking — if
// we can't acquire it, another instance is busy and we just skip.
const watchdogLock = "/tmp/xray-waybar-watchdog.lock"

// watchdogMaxAttempts is the consecutive-failure ceiling. Once crossed,
// the watchdog clears state.Active so it stops trying — a broken
// subscription or a permanently down upstream shouldn't burn CPU
// relaunching xray every 10 seconds forever.
const watchdogMaxAttempts = 3

// xrayProbeTimeout is how long we wait for the SOCKS5 port to respond.
// Long enough to ride out a brief CPU spike, short enough that a stuck
// xray gets detected within one tick.
const xrayProbeTimeout = 500 * time.Millisecond

// resumeRouteWait bounds how long we wait for a default route to
// appear after the system resumes. NetworkManager usually
// re-associates wifi in 1–3 seconds; 10s leaves headroom for a slow
// roam without making the watchdog look hung if the network never
// comes back.
const resumeRouteWait = 10 * time.Second

// cmdWatchdog runs the auto-reconnect watchdog.
//
// Usage:
//
//	xray-waybar-ctl watchdog                 # one tick, exit (manual use)
//	xray-waybar-ctl watchdog --loop 10s      # daemon mode: tick forever
//
// In daemon mode it ticks on a `time.Ticker` and shuts down cleanly on
// SIGTERM (the main signal context cancels). systemd runs it Type=simple
// without a timer — one `Started` line per session instead of one every
// tick, and no fork/exec overhead.
func cmdWatchdog(ctx context.Context, args []string) error {
	interval, err := parseWatchdogArgs(args)
	if err != nil {
		return err
	}
	if interval == 0 {
		return watchdogTick(ctx)
	}

	fmt.Fprintf(os.Stderr, "watchdog: looping every %s\n", interval)

	// Subscribe to logind PrepareForSleep. We do this best-effort: if
	// it fails (no logind, no system bus, broken D-Bus) the watchdog
	// still runs as a pure poll loop — the 10s tick is the original
	// safety net and stays in place. The D-Bus path is just the
	// "react immediately" optimization that also lets us shut xray
	// down *before* the kernel freezes its sockets, so resume never
	// has stale state to clean up in the first place.
	var sleepEvents <-chan sleepwatch.Event
	sw, err := sleepwatch.Connect(ctx, "xray-waybar-ctl", "Cleanly stop xray before suspend; reconnect on resume")
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: sleepwatch unavailable, polling only: %v\n", err)
	} else {
		defer sw.Close()
		sleepEvents = sw.Events()
		fmt.Fprintln(os.Stderr, "watchdog: subscribed to logind PrepareForSleep")
	}

	// Run one tick immediately so a fresh start doesn't have to wait
	// `interval` before noticing a dead xray inherited from a previous
	// session.
	if err := watchdogTick(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "watchdog: tick failed: %v\n", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := watchdogTick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "watchdog: tick failed: %v\n", err)
			}
		case ev, ok := <-sleepEvents:
			if !ok {
				// Dispatcher exited (ctx cancel or connection drop).
				// Disable the case so the select doesn't spin on a
				// closed channel; keep polling.
				sleepEvents = nil
				continue
			}
			if ev.Pre {
				handlePreSuspend(ctx)
				// Always release, even if handler bailed early —
				// otherwise logind waits the full 5s for nothing.
				ev.Done()
			} else {
				handlePostResume(ctx)
			}
		}
	}
}

// watchdogTick is one detect-and-recover cycle. Returns nil for the
// "nothing to do" path so the daemon loop doesn't spam errors.
//
// The recovery loop is intentionally narrow:
//
//  1. If state.Active == nil we are not supposed to be connected; exit
//     fast. The user disconnected by hand or the previous watchdog
//     already gave up — don't fight that decision.
//  2. If xray is healthy (process alive AND SOCKS5 responds), reset the
//     failure counter and exit.
//  3. Otherwise: tear down TUN immediately. With a dead xray behind it,
//     tun2socks busy-loops on `connect: cannot assign requested address`
//     and burns hundreds of MB of RAM within seconds. Stopping the unit
//     is the priority — reconnect comes after.
//  4. Then attempt one reconnect: prefer the last-active server if ping
//     thinks it's still alive (so a transient xray crash recovers in
//     place), otherwise fall back to priority/latency picking from the
//     last ping batch. After watchdogMaxAttempts failures, give up.
func watchdogTick(ctx context.Context) error {
	lock, err := acquireWatchdogLock()
	if err != nil {
		// Another watchdog tick or a user command is running — let it
		// finish; we'll catch up next tick.
		return nil
	}
	defer releaseWatchdogLock(lock)

	lc, err := loadAll()
	if err != nil {
		return err
	}
	if lc.state.Active == nil {
		return nil
	}

	if xrayHealthy(lc.cfg.PIDFile, lc.cfg.XrayPort) {
		if lc.state.WatchdogAttempts != 0 {
			lc.state.WatchdogAttempts = 0
			_ = store.SaveState(lc.cfg.StateFile, lc.state)
		}
		return nil
	}

	fmt.Fprintf(os.Stderr, "watchdog: xray unhealthy (active=%s, attempts=%d)\n",
		lc.state.Active.Name, lc.state.WatchdogAttempts)

	// Tear down the tunnel before anything else. Every 10ms of busy-loop
	// is hundreds of new ephemeral sockets and a chunk of netstack
	// buffer growth.
	if lc.cfg.SystemWide {
		if err := sysmode.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "watchdog: sysmode.Stop: %v\n", err)
		}
	}
	if err := process.Stop(lc.cfg.PIDFile, 2*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: process.Stop: %v\n", err)
	}

	if lc.state.WatchdogAttempts >= watchdogMaxAttempts {
		fmt.Fprintf(os.Stderr, "watchdog: giving up after %d attempts; clearing active server\n",
			lc.state.WatchdogAttempts)
		lc.state.Active = nil
		lc.state.ConnectedAt = time.Time{}
		lc.state.WatchdogAttempts = 0
		return store.SaveState(lc.cfg.StateFile, lc.state)
	}

	pick, ok := pickRecoveryTarget(lc)
	if !ok {
		fmt.Fprintln(os.Stderr, "watchdog: no alive server in last ping batch; clearing active")
		lc.state.Active = nil
		lc.state.ConnectedAt = time.Time{}
		lc.state.WatchdogAttempts = 0
		return store.SaveState(lc.cfg.StateFile, lc.state)
	}

	// Persist the attempt counter *before* launching so a crash mid-launch
	// still counts toward the ceiling.
	lc.state.WatchdogAttempts++
	if err := store.SaveState(lc.cfg.StateFile, lc.state); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: persist attempt counter: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "watchdog: reconnecting to %s (attempt %d/%d)\n",
		pick.Name, lc.state.WatchdogAttempts, watchdogMaxAttempts)
	return launch(ctx, lc, pick)
}

// parseWatchdogArgs accepts `--loop <duration>` (or `--loop=<duration>`)
// and returns the parsed interval. Zero means "one-shot mode".
func parseWatchdogArgs(args []string) (time.Duration, error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--loop":
			if i+1 >= len(args) {
				return 0, fmt.Errorf("watchdog: --loop requires a duration (e.g. 10s)")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return 0, fmt.Errorf("watchdog: parse --loop duration: %w", err)
			}
			if d <= 0 {
				return 0, fmt.Errorf("watchdog: --loop duration must be positive")
			}
			return d, nil
		case len(a) > len("--loop=") && a[:len("--loop=")] == "--loop=":
			d, err := time.ParseDuration(a[len("--loop="):])
			if err != nil {
				return 0, fmt.Errorf("watchdog: parse --loop duration: %w", err)
			}
			if d <= 0 {
				return 0, fmt.Errorf("watchdog: --loop duration must be positive")
			}
			return d, nil
		default:
			return 0, fmt.Errorf("watchdog: unknown arg %q", a)
		}
	}
	return 0, nil
}

// xrayHealthy reports whether xray is both alive (PID file points at a
// running process) and serving SOCKS5 on its inbound port. Either check
// alone is insufficient: the PID can be alive while xray is stuck (no
// accept loop), and the port can be transiently closed during config
// reloads even when the process is healthy.
func xrayHealthy(pidFile string, port int) bool {
	running, _ := process.IsRunning(pidFile)
	if !running {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), xrayProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// pickRecoveryTarget chooses which server to relaunch on. Preference:
//  1. The previously active server, if ping still thinks it's alive
//     (this handles the common case of xray crashing transiently).
//  2. The first priority entry that is alive in the last ping batch.
//  3. The fastest alive server from the last ping batch.
//
// Returns false if nothing is known to be alive — caller should give up.
func pickRecoveryTarget(lc *loadCtx) (server.Server, bool) {
	if lc.state.Active != nil {
		if r, ok := lc.state.Results[lc.state.Active.Name]; ok && r.Alive {
			if s, _, err := findServer(lc.cache, lc.state.Active.Name); err == nil {
				return s, true
			}
		}
	}

	// Build a Result slice over the cached servers so we can reuse the
	// existing tester picking helpers. Missing entries in state.Results
	// become non-alive — same effect as treating them as untested.
	rs := make([]tester.Result, 0, len(lc.cache.Servers))
	for _, s := range lc.cache.Servers {
		r := tester.Result{Server: s}
		if pr, ok := lc.state.Results[s.Name]; ok {
			r.Alive = pr.Alive
			r.Latency = pr.Latency
		}
		rs = append(rs, r)
	}
	if i := tester.PickByPriority(rs, lc.cfg.Priority); i >= 0 {
		return rs[i].Server, true
	}
	tester.SortByLatency(rs)
	if len(rs) > 0 && rs[0].Alive {
		return rs[0].Server, true
	}
	return server.Server{}, false
}

// acquireWatchdogLock takes an exclusive non-blocking flock on a known
// path. Returning an error means somebody else holds it; the caller
// should simply skip this tick.
func acquireWatchdogLock() (*os.File, error) {
	f, err := os.OpenFile(watchdogLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// acquireUserLock blocks until the watchdog lock is free, then holds it
// for the duration of a user-driven command (connect / disconnect /
// use*). Watchdog uses LOCK_NB and quietly skips its tick when the user
// holds this — so a disconnect mid-tick won't get steamrolled by the
// tail of a watchdog reconnect that started moments earlier with the
// pre-disconnect state cached in memory.
func acquireUserLock() (*os.File, error) {
	f, err := os.OpenFile(watchdogLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func releaseWatchdogLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// handlePreSuspend stops xray + TUN cleanly while logind is holding
// the rest of the suspend chain on our delay inhibitor. The window
// is short (InhibitDelayMaxSec, default 5s) so we keep the work
// bounded: a 2-second SIGTERM grace on xray, no retries.
//
// Critically: we do NOT clear state.Active. The post-resume handler
// reads it as "we were connected; reconnect to this server". That's
// the whole point of doing pre-suspend cleanup — we tear xray down
// *with* the knowledge that we'll bring the same target back up on
// the other side.
func handlePreSuspend(ctx context.Context) {
	// Blocking flock: a user-initiated command (connect/disconnect/use)
	// in flight should finish first, otherwise pre-suspend cleanup
	// races against a half-applied state transition. The wait is
	// bounded by logind's InhibitDelayMaxSec; if the user command
	// somehow takes longer, suspend proceeds without our cleanup and
	// the watchdog tick + post-resume reconnect picks up the pieces.
	lock, err := acquireUserLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: pre-suspend lock: %v\n", err)
		return
	}
	defer releaseWatchdogLock(lock)

	lc, err := loadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: pre-suspend load: %v\n", err)
		return
	}
	if lc.state.Active == nil {
		// Not connected; nothing to tear down.
		return
	}

	fmt.Fprintf(os.Stderr, "watchdog: pre-suspend cleanup (active=%s)\n", lc.state.Active.Name)
	if lc.cfg.SystemWide {
		if err := sysmode.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "watchdog: pre-suspend sysmode.Stop: %v\n", err)
		}
	}
	if err := process.Stop(lc.cfg.PIDFile, 2*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: pre-suspend process.Stop: %v\n", err)
	}
}

// handlePostResume relaunches xray + TUN onto the server that was
// active before suspend. Skipped if state.Active was cleared (either
// the user disconnected before suspend, or watchdog gave up earlier).
//
// We wait for a default route first because the subscription cache
// is already loaded by launch() — but launch() itself doesn't fetch
// anything, so the only thing it needs from the network is xray's
// connection to upstream. Still, with no route the relaunch races
// against NetworkManager re-associating wifi and the TCP probe
// inside process.Start tends to fail, so we wait the route in.
func handlePostResume(ctx context.Context) {
	lock, err := acquireUserLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: post-resume lock: %v\n", err)
		return
	}
	defer releaseWatchdogLock(lock)

	lc, err := loadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: post-resume load: %v\n", err)
		return
	}
	if lc.state.Active == nil {
		// User disconnected before suspend, or the previous suspend's
		// pre-handler crashed before we could record the active
		// target. Either way: stay disconnected, mirror the existing
		// behavior where a sleeping disconnected laptop stays that
		// way.
		return
	}

	if !waitDefaultRoute(ctx, resumeRouteWait) {
		fmt.Fprintf(os.Stderr, "watchdog: post-resume no default route after %s; deferring to next tick\n", resumeRouteWait)
		return
	}

	// Reset the failure budget on a fresh resume: the pre-suspend
	// counter is irrelevant to the post-resume world.
	lc.state.WatchdogAttempts = 0

	target := *lc.state.Active
	// If the cached server vanished (subscription refreshed during
	// the previous session, name changed), fall through to the
	// existing recovery picker.
	if _, _, err := findServer(lc.cache, target.Name); err != nil {
		pick, ok := pickRecoveryTarget(lc)
		if !ok {
			fmt.Fprintln(os.Stderr, "watchdog: post-resume no live target; clearing active")
			lc.state.Active = nil
			lc.state.ConnectedAt = time.Time{}
			_ = store.SaveState(lc.cfg.StateFile, lc.state)
			return
		}
		target = pick
	}

	fmt.Fprintf(os.Stderr, "watchdog: post-resume reconnecting to %s\n", target.Name)
	if err := launch(ctx, lc, target); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: post-resume launch: %v\n", err)
		// Don't bump WatchdogAttempts here — the regular tick loop
		// will detect the unhealthy state on its next pass and
		// run the normal failure-budget path.
	}
}

// waitDefaultRoute polls /proc/net/route once a second for at most
// d, returning true as soon as an IPv4 default route exists. Reading
// /proc directly avoids forking `ip route` every tick.
//
// The /proc/net/route format is tab-separated; the destination field
// is the 2nd column in hex little-endian. A default route is dest
// 00000000 with flags & 0x1 (RTF_UP) set.
func waitDefaultRoute(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if hasDefaultRoute() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func hasDefaultRoute() bool {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Skip header.
	if !sc.Scan() {
		return false
	}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		// Destination column == "00000000" means default.
		if fields[1] != "00000000" {
			continue
		}
		// Flags column (4th) — RTF_UP is bit 0x1.
		var flags uint64
		if _, err := fmt.Sscanf(fields[3], "%X", &flags); err != nil {
			continue
		}
		if flags&0x1 != 0 {
			return true
		}
	}
	return false
}
