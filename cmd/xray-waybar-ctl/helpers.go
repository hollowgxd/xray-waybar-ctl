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
	"github.com/yourgfslove/xray-waybar-ctl/internal/geo"
	"github.com/yourgfslove/xray-waybar-ctl/internal/process"
	"github.com/yourgfslove/xray-waybar-ctl/internal/routing"
	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
	"github.com/yourgfslove/xray-waybar-ctl/internal/store"
	"github.com/yourgfslove/xray-waybar-ctl/internal/subscription"
	"github.com/yourgfslove/xray-waybar-ctl/internal/sysmode"
	"github.com/yourgfslove/xray-waybar-ctl/internal/xrayconfig"
)

// activeProfile returns state.Profile if set, otherwise cfg.RoutingProfile.
// Empty result means proxy-all. Keep in one place so menu, status and
// launch agree.
func activeProfile(lc *loadCtx) string {
	if lc.state != nil && lc.state.Profile != "" {
		return lc.state.Profile
	}
	return lc.cfg.RoutingProfile
}

// profileNeedsRules reports whether the profile needs a downloaded
// rules.json + geoip/geosite assets. Only the built-in proxy-all and
// direct render entirely from code.
func profileNeedsRules(profile string) bool {
	return !appconfig.IsBuiltinProfile(profile) && profile != ""
}

// bypassFile is read by xray-waybar-tun.service ExecStartPost. Each
// line is one IPv4 address that must NOT be routed through tun0 —
// otherwise xray's own connection to the upstream server gets
// looped back through the tunnel.
const bypassFile = "/tmp/xray-waybar-bypass.txt"

// xraySOMark is the SO_MARK every xray outbound stamps onto its
// sockets in system_wide mode. The TUN unit installs
// `ip rule add fwmark <xraySOMark> lookup main pref 9` so any packet
// xray emits (proxy upstream *and* direct/freedom outbound) skips
// `default dev tun0` and goes via the real interface. Without this,
// xray accepts a tun2socks-delivered connection, routes it to the
// direct outbound (anything matching e.g. geosite:category-ru in a
// smart profile), freedom dials the original IP, that dial follows
// the default route into tun0, lands back at socks-in, and the loop
// runs the CPU and tun2socks netstack buffers into the floor.
//
// Must match the fwmark literal in configs/systemd/xray-waybar-tun.service.
const xraySOMark = 0x29a

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
	hwid, err := subscription.LoadOrCreateHWID(lc.cfg.HWIDFile)
	if err != nil {
		// Don't fail the whole fetch — a panel that doesn't gate on HWID
		// will still hand us real servers. Just warn so the user can
		// spot a permission problem.
		fmt.Fprintf(os.Stderr, "warn: hwid: %v\n", err)
	}
	body, err := subscription.Fetch(ctx, lc.cfg.SubscriptionURL, hwid)
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
	profile := activeProfile(lc)
	opts := xrayconfig.Options{SocksPort: lc.cfg.XrayPort}
	if lc.cfg.SystemWide {
		opts.SOMark = xraySOMark
	}

	switch {
	case profile == "direct":
		opts.ForceAllDirect = true
	case profileNeedsRules(profile):
		// Load the cached rules.json. Missing rules or missing geoip.dat
		// is recoverable: we fall back to proxy-all and warn rather than
		// letting xray fail at start, so the user can fix it with
		// `update-geo` without losing the connection.
		if !geo.AssetsPresent(lc.cfg.Geo.Dir) || !geo.RulesPresent(lc.cfg.Geo.Dir) {
			fmt.Fprintf(os.Stderr, "warn: profile %q needs geoip.dat + rules.json in %s — falling back to proxy-all. Run `xray-waybar-ctl update-geo`.\n", profile, lc.cfg.Geo.Dir)
		} else {
			rules, err := routing.LoadFile(geo.RulesPath(lc.cfg.Geo.Dir))
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: cannot parse rules.json (%v) — falling back to proxy-all\n", err)
			} else {
				opts.Rules = rules
			}
		}
	}
	// Rules are captured here (not just inside opts) so writeServerBypass
	// can pull DNS server IPs out of them: a DomesticDNS like 77.88.8.8
	// declared in the upstream rules.json must be host-routed mimo tun0,
	// otherwise xray's own DNS queries loop back through SOCKS5.
	rules := opts.Rules

	raw, err := xrayconfig.Generate(s, opts)
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
	var extraEnv []string
	if lc.cfg.Geo.Dir != "" {
		extraEnv = append(extraEnv, "XRAY_LOCATION_ASSET="+lc.cfg.Geo.Dir)
	}
	_, err = process.Start(ctx, process.StartOptions{
		BinPath:      lc.cfg.XrayBin,
		ConfigPath:   lc.cfg.XrayConfig,
		PIDFile:      lc.cfg.PIDFile,
		LogFile:      lc.cfg.LogFile,
		ReadyPort:    lc.cfg.XrayPort,
		ReadyTimeout: 5 * time.Second,
		ExtraEnv:     extraEnv,
	})
	if err != nil {
		return err
	}

	if lc.cfg.SystemWide {
		if err := writeServerBypass(ctx, s, lc.cache, rules); err != nil {
			fmt.Fprintf(os.Stderr, "warn: bypass IP write: %v\n", err)
		}
		// Always Restart, never Start. `systemctl start` on an
		// already-running unit is a no-op, so a switch-server `launch`
		// would leave the route table holding the *previous* upstream's
		// bypass entry — and the new upstream's IP routed through tun0,
		// which is exactly the loop we just fixed for the freedom
		// outbound. Restart forces ExecStartPost to re-read the bypass
		// file and reinstall the fwmark rule.
		if err := sysmode.Restart(ctx); err != nil {
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

// writeServerBypass resolves the upstream xray server (plus every other
// server in the cache) to IPv4 address(es) and writes them to bypassFile
// alongside well-known DNS servers, /etc/resolv.conf nameservers, and
// any DNS server IPs declared by the active routing rules.
//
// The TUN systemd unit reads this file on ExecStartPost and adds
// host-routes via the real interface (not tun0).
//
// Why every server, not just the active one? Tester (`xray-waybar-ctl
// test`) and pinger (the 30s timer) probe *all* cached upstreams over
// plain TCP — without SO_MARK, since they're plain Go net.Dialer code
// running outside xray. If the inactive upstream IPs aren't bypassed,
// each probe lands in tun0 → tun2socks → socks-in → xray's proxy
// outbound, hammers ephemeral ports, and grows tun2socks netstack
// buffers until the watchdog or MemoryMax cuts it down. The list is
// cheap (dozens of /32 host routes) and these IPs are VPN endpoints
// the user is never browsing anyway.
//
// Active server's IP still appears too, of course — it's just no
// longer special.
func writeServerBypass(ctx context.Context, active server.Server, cache *store.Cache, rules *routing.Rules) error {
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
	// Active first (so failures here surface even if the cache walk
	// silently skips a dead DNS lookup later).
	ips, err := resolveServerIPs(ctx, active.Address)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("no IPv4 addresses for %q", active.Address)
	}
	for _, ip := range ips {
		add(ip)
	}
	// Every other cached server. Failures are non-fatal — pinger will
	// just take the slow path through tun0 for that one host. The
	// resolver call is local-DNS only and bounded by the parent ctx.
	if cache != nil {
		for _, s := range cache.Servers {
			if s.Address == active.Address {
				continue
			}
			rIPs, rErr := resolveServerIPs(ctx, s.Address)
			if rErr != nil {
				fmt.Fprintf(os.Stderr, "warn: bypass resolve %s: %v\n", s.Address, rErr)
				continue
			}
			for _, ip := range rIPs {
				add(ip)
			}
		}
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
	if rules != nil {
		add(net.ParseIP(rules.RemoteDNS))
		add(net.ParseIP(rules.DomesticDNS))
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
