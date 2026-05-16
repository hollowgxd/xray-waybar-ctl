// Package pinger does a cheap TCP-level liveness check on every server.
//
// Unlike the full URL test (`internal/tester`), this never spawns xray
// processes — it just dials server:port. That makes it light enough to
// run from a 30-second systemd timer without burning CPU or memory.
//
// The trade-off: a positive ping only proves the TCP port is reachable,
// not that the REALITY/TLS handshake or upstream proxying still works.
// For real-world latency, the URL tester is still the source of truth.
package pinger

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
)

// Result is one TCP probe outcome.
type Result struct {
	Server  server.Server
	Latency time.Duration
	Alive   bool
	Error   error
}

// PingAll concurrently TCP-dials every server. Concurrency is hard-
// capped so a 100-server subscription doesn't open 100 sockets at once.
func PingAll(ctx context.Context, servers []server.Server, timeout time.Duration) []Result {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	const maxParallel = 20

	results := make([]Result, len(servers))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func(i int, s server.Server) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = pingOne(ctx, s, timeout)
		}(i, s)
	}
	wg.Wait()
	return results
}

func pingOne(ctx context.Context, s server.Server, timeout time.Duration) Result {
	addr := fmt.Sprintf("%s:%d", s.Address, s.Port)
	dialer := &net.Dialer{Timeout: timeout}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	latency := time.Since(start)
	if err != nil {
		return Result{Server: s, Alive: false, Error: err, Latency: latency}
	}
	_ = conn.Close()
	return Result{Server: s, Alive: true, Latency: latency}
}
