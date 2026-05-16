package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/appconfig"
	"github.com/yourgfslove/xray-waybar-ctl/internal/process"
	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
	"github.com/yourgfslove/xray-waybar-ctl/internal/store"
	"github.com/yourgfslove/xray-waybar-ctl/internal/subscription"
	"github.com/yourgfslove/xray-waybar-ctl/internal/sysmode"
	"github.com/yourgfslove/xray-waybar-ctl/internal/xrayconfig"
)

// bypassFile is read by xray-waybar-tun.service ExecStartPost. Each
// line is one IPv4 address that must NOT be routed through tun0 —
// otherwise xray's own connection to the upstream server gets
// looped back through the tunnel.
const bypassFile = "/tmp/xray-waybar-bypass.txt"

// loadCtx is the bundle every command needs. It is reloaded per
// invocation — the CLI is short-lived.
type loadCtx struct {
	cfg   *appconfig.Config
	cache *store.Cache
	state *store.State
}

// loadAll reads config, cache and state. Cache and state default to
// empty structs when the files don't exist yet.
func loadAll() (*loadCtx, error) {
	cfg, _, err := appconfig.Load(os.Getenv("XRAY_WAYBAR_CONFIG"))
	if err != nil {
		return nil, err
	}
	cache, err := store.LoadCache(cfg.CacheFile)
	if err != nil {
		return nil, err
	}
	if cache == nil {
		cache = &store.Cache{}
	}
	state, err := store.LoadState(cfg.StateFile)
	if err != nil {
		return nil, err
	}
	if state == nil {
		state = &store.State{}
	}
	return &loadCtx{cfg: cfg, cache: cache, state: state}, nil
}

// ensureFreshCache fetches the subscription if the cache is missing or
// older than the configured interval. force=true ignores the interval.
func ensureFreshCache(ctx context.Context, lc *loadCtx, force bool) error {
	stale := force ||
		len(lc.cache.Servers) == 0 ||
		lc.cache.SourceURL != lc.cfg.SubscriptionURL ||
		(lc.cfg.SubscriptionUpdateInterval > 0 && lc.cache.Age() > lc.cfg.SubscriptionUpdateInterval)
	if !stale {
		return nil
	}
	body, err := subscription.Fetch(ctx, lc.cfg.SubscriptionURL)
	if err != nil {
		// keep the stale cache if we have one — partial connectivity
		// shouldn't break a `connect` that worked yesterday.
		if len(lc.cache.Servers) > 0 {
			fmt.Fprintf(os.Stderr, "warn: subscription fetch failed, using stale cache: %v\n", err)
			return nil
		}
		return err
	}
	servers, errs := subscription.Parse(body)
	if len(servers) == 0 {
		return fmt.Errorf("subscription returned no usable servers: %v", errs)
	}
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warn:", e)
	}
	lc.cache = &store.Cache{
		FetchedAt: time.Now(),
		SourceURL: lc.cfg.SubscriptionURL,
		Servers:   servers,
	}
	return store.SaveCache(lc.cfg.CacheFile, lc.cache)
}

// findServer returns the cached server with the given name (fragment).
func findServer(cache *store.Cache, name string) (server.Server, int, error) {
	for i, s := range cache.Servers {
		if s.Name == name {
			return s, i, nil
		}
	}
	return server.Server{}, -1, fmt.Errorf("server %q not found in cache", name)
}

// launch swaps out xray onto the given server. It is the single place
// that writes xray.json, stops the old process and starts the new one.
func launch(ctx context.Context, lc *loadCtx, s server.Server) error {
	raw, err := xrayconfig.Generate(s, xrayconfig.Options{SocksPort: lc.cfg.XrayPort})
	if err != nil {
		return err
	}
	if err := writeFileAtomic(lc.cfg.XrayConfig, raw, 0o600); err != nil {
		return err
	}
	if err := process.Stop(lc.cfg.PIDFile, 2*time.Second); err != nil {
		return err
	}
	// Defense in depth: even if Stop above did the right thing, scan for
	// any other xray that's still alive against our config (e.g. orphaned
	// by an earlier crash, or spawned by a racing `connect`) and SIGKILL
	// it. Otherwise process.Start would happily add yet another instance
	// and the leak compounds.
	if n := process.KillStraysByConfig(lc.cfg.XrayConfig, lc.cfg.PIDFile); n > 0 {
		fmt.Fprintf(os.Stderr, "launch: killed %d stray xray instance(s)\n", n)
	}
	_, err = process.Start(ctx, process.StartOptions{
		BinPath:      lc.cfg.XrayBin,
		ConfigPath:   lc.cfg.XrayConfig,
		PIDFile:      lc.cfg.PIDFile,
		LogFile:      lc.cfg.LogFile,
		ReadyPort:    lc.cfg.XrayPort,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		return err
	}

	if lc.cfg.SystemWide {
		if err := writeServerBypass(ctx, s); err != nil {
			fmt.Fprintf(os.Stderr, "warn: bypass IP write: %v\n", err)
		}
		if err := sysmode.Start(ctx); err != nil {
			// xray is already up — print the warning but don't undo it.
			// The user can rerun `connect` after fixing the unit.
			fmt.Fprintf(os.Stderr, "warn: system_wide tunnel did not start: %v\n", err)
		}
	}

	now := time.Now()
	lc.state.Active = &s
	lc.state.ConnectedAt = now
	return store.SaveState(lc.cfg.StateFile, lc.state)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dirOf(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// activeIndex returns the index of the active server in the cache, or
// -1 if there is no active server or the active server is no longer
// in the cache.
func activeIndex(lc *loadCtx) int {
	if lc.state.Active == nil {
		return -1
	}
	for i, s := range lc.cache.Servers {
		if s.Name == lc.state.Active.Name {
			return i
		}
	}
	return -1
}

// errNoServers is returned when no servers are cached.
var errNoServers = errors.New("no servers in cache; run `xray-waybar-ctl update` first")

// writeServerBypass resolves the upstream xray server to its IPv4
// address(es) and writes them to bypassFile alongside well-known DNS
// servers and any nameservers from /etc/resolv.conf.
//
// The TUN systemd unit reads this file on ExecStartPost and adds
// host-routes so xray's own connection (and DNS) bypasses tun0.
// Without this:
//   - every packet xray emits gets looped back through the tunnel;
//   - DNS-over-UDP doesn't survive REALITY (TCP-only), sites hang.
func writeServerBypass(ctx context.Context, s server.Server) error {
	ips, err := resolveServerIPs(ctx, s.Address)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("no IPv4 addresses for %q", s.Address)
	}
	// dedup + DNS additions
	seen := map[string]struct{}{}
	add := func(ip net.IP) {
		if ip == nil {
			return
		}
		v4 := ip.To4()
		if v4 == nil {
			return
		}
		seen[v4.String()] = struct{}{}
	}
	for _, ip := range ips {
		add(ip)
	}
	// well-known public DNS — these don't reveal much (you'd query
	// them either way) but their reachability lets browsers resolve
	// hostnames while the rest of traffic goes through xray.
	for _, s := range []string{"1.1.1.1", "1.0.0.1", "8.8.8.8", "8.8.4.4"} {
		add(net.ParseIP(s))
	}
	for _, ip := range readResolvConfNameservers() {
		add(ip)
	}

	var b strings.Builder
	for ip := range seen {
		b.WriteString(ip)
		b.WriteByte('\n')
	}
	return os.WriteFile(bypassFile, []byte(b.String()), 0o644)
}

// readResolvConfNameservers parses /etc/resolv.conf for `nameserver`
// lines. Best-effort — errors are swallowed because the well-known
// DNS entries provide a safe fallback.
func readResolvConfNameservers() []net.IP {
	raw, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ip := net.ParseIP(fields[1])
		if ip != nil && ip.To4() != nil {
			out = append(out, ip)
		}
	}
	return out
}

func resolveServerIPs(ctx context.Context, host string) ([]net.IP, error) {
	// If host is already an IP, skip DNS.
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return []net.IP{v4}, nil
		}
		return []net.IP{ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	var out []net.IP
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			out = append(out, v4)
		}
	}
	return out, nil
}
