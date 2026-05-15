// Package tester benchmarks a list of servers by running a temporary
// xray-core instance for each, then issuing an HTTP GET through its
// local SOCKS5 inbound.
//
// Concurrency is bounded by Options.Concurrency; each parallel test
// claims its own port from a sequential range starting at StartPort.
package tester

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/proxy"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
	"github.com/yourgfslove/xray-waybar-ctl/internal/xrayconfig"
)

// Result is one server's measurement.
type Result struct {
	Server  server.Server
	Latency time.Duration
	Alive   bool
	Error   error
}

// Options controls a Test batch.
type Options struct {
	XrayBin     string
	TestURL     string
	Timeout     time.Duration
	Concurrency int
	// StartPort is the first port used. Each parallel test uses one
	// port from [StartPort, StartPort+Concurrency).
	StartPort int
}

// Test runs the URL-test for every server. The returned slice is in
// the same order as servers — never sorted. Sorting is the caller's
// concern.
func Test(ctx context.Context, servers []server.Server, opts Options) []Result {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Second
	}

	results := make([]Result, len(servers))
	// Each slot is a port — one goroutine at a time may hold it.
	ports := make(chan int, opts.Concurrency)
	for i := 0; i < opts.Concurrency; i++ {
		ports <- opts.StartPort + i
	}

	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func(i int, s server.Server) {
			defer wg.Done()
			port := <-ports
			defer func() { ports <- port }()
			results[i] = testOne(ctx, s, port, opts)
		}(i, s)
	}
	wg.Wait()
	return results
}

func testOne(ctx context.Context, s server.Server, port int, opts Options) Result {
	res := Result{Server: s}

	cfgBytes, err := xrayconfig.Generate(s, xrayconfig.Options{
		SocksPort: port,
		LogLevel:  "error",
	})
	if err != nil {
		res.Error = fmt.Errorf("generate config: %w", err)
		return res
	}
	tmpDir, err := os.MkdirTemp("", "xray-test-*")
	if err != nil {
		res.Error = err
		return res
	}
	defer os.RemoveAll(tmpDir)

	cfgPath := filepath.Join(tmpDir, "xray.json")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		res.Error = err
		return res
	}

	cmd := exec.Command(opts.XrayBin, "-config", cfgPath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		res.Error = fmt.Errorf("start xray: %w", err)
		return res
	}
	defer func() {
		// Kill the whole process group; xray spawns no children today
		// but this is cheap insurance.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_, _ = cmd.Process.Wait()
	}()

	if err := waitForPort(ctx, port, opts.Timeout); err != nil {
		res.Error = fmt.Errorf("xray did not open port: %w", err)
		return res
	}

	latency, err := probe(ctx, opts.TestURL, port, opts.Timeout)
	if err != nil {
		res.Error = err
		return res
	}
	res.Latency = latency
	res.Alive = true
	return res
}

func waitForPort(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("port %d not open after %s", port, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func probe(ctx context.Context, testURL string, port int, timeout time.Duration) (time.Duration, error) {
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, &net.Dialer{Timeout: timeout})
	if err != nil {
		return 0, fmt.Errorf("socks5: %w", err)
	}
	transport := &http.Transport{
		Dial:                dialer.Dial,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
	}
	client := &http.Client{Transport: transport, Timeout: timeout}

	if _, err := url.Parse(testURL); err != nil {
		return 0, fmt.Errorf("parse test_url: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, testURL, nil)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	latency := time.Since(start)

	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return latency, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return latency, nil
}

// SortByLatency reorders results in-place so live entries come first,
// sorted ascending by latency. Dead entries are appended.
func SortByLatency(rs []Result) {
	sortStable(rs, func(a, b Result) bool {
		switch {
		case a.Alive && !b.Alive:
			return true
		case !a.Alive && b.Alive:
			return false
		case !a.Alive && !b.Alive:
			return false
		default:
			return a.Latency < b.Latency
		}
	})
}

// PickByPriority returns the first result whose server name matches an
// entry in priority and is alive. Returns -1 if none match.
func PickByPriority(rs []Result, priority []string) int {
	if len(priority) == 0 || len(rs) == 0 {
		return -1
	}
	index := make(map[string]int, len(rs))
	for i, r := range rs {
		index[r.Server.Name] = i
	}
	for _, name := range priority {
		if i, ok := index[name]; ok && rs[i].Alive {
			return i
		}
	}
	return -1
}

// sortStable is a tiny stable sort to avoid pulling in sort.Slice's
// less function dance for one call site.
func sortStable(rs []Result, less func(a, b Result) bool) {
	if len(rs) < 2 {
		return
	}
	// Insertion sort — O(n²) but n is small (subscription sizes are
	// dozens, not thousands) and it is stable by construction.
	for i := 1; i < len(rs); i++ {
		j := i
		for j > 0 && less(rs[j], rs[j-1]) {
			rs[j], rs[j-1] = rs[j-1], rs[j]
			j--
		}
	}
}

