package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	coremihomo "github.com/yourgfslove/xray-waybar-ctl/internal/mihomo"
	"github.com/yourgfslove/xray-waybar-ctl/internal/process"
	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
	"github.com/yourgfslove/xray-waybar-ctl/internal/store"
	"github.com/yourgfslove/xray-waybar-ctl/internal/subscription"
	"github.com/yourgfslove/xray-waybar-ctl/internal/sysmode"
	"github.com/yourgfslove/xray-waybar-ctl/internal/tester"
	"github.com/yourgfslove/xray-waybar-ctl/internal/waybar"
)

func mihomoClient(lc *loadCtx, timeout time.Duration) (*coremihomo.Client, error) {
	secret, err := subscription.LoadOrCreateHWID(lc.cfg.MihomoSecretFile)
	if err != nil {
		return nil, fmt.Errorf("mihomo secret: %w", err)
	}
	return coremihomo.NewClient(lc.cfg.MihomoController, secret, timeout), nil
}

// launchMihomo starts one complete native profile. Unlike the Xray
// backend, switching a node while the core is healthy is an API call,
// not a process restart.
func launchMihomo(ctx context.Context, lc *loadCtx, target *server.Server, resetFailures bool) error {
	if running, _ := process.IsRunning(lc.cfg.PIDFile); running {
		client, err := mihomoClient(lc, lc.cfg.TestTimeout())
		if err == nil {
			if _, err = client.Version(ctx); err == nil && target != nil {
				return switchMihomo(ctx, lc, client, *target, resetFailures)
			}
		}
	}

	source, err := os.ReadFile(lc.cfg.MihomoSubscriptionFile)
	if err != nil {
		return fmt.Errorf("mihomo: read cached subscription: %w", err)
	}
	secret, err := subscription.LoadOrCreateHWID(lc.cfg.MihomoSecretFile)
	if err != nil {
		return fmt.Errorf("mihomo secret: %w", err)
	}
	routingMark := 0
	if lc.cfg.SystemWide {
		routingMark = xraySOMark
	}
	raw, err := coremihomo.Prepare(source, lc.cache.Servers, coremihomo.PrepareOptions{
		MixedPort:   lc.cfg.LocalPort(),
		Controller:  lc.cfg.MihomoController,
		Secret:      secret,
		EnableTUN:   lc.cfg.SystemWide,
		TUNStack:    lc.cfg.MihomoTUNStack,
		RoutingMark: routingMark,
		MainGroup:   lc.cfg.MihomoGroup,
	})
	if err != nil {
		return err
	}
	if err := writeFileAtomic(lc.cfg.MihomoConfig, raw, 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(lc.cfg.MihomoHome, 0o700); err != nil {
		return fmt.Errorf("mihomo: create home: %w", err)
	}

	// A user may switch core in app.yaml while the legacy tun2socks
	// sidecar is still up. Mihomo owns TUN natively, so remove it first.
	if state, _ := sysmode.Status(ctx); state == sysmode.StateActive {
		if err := sysmode.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "warn: stop legacy TUN sidecar: %v\n", err)
		}
	}
	if err := process.Stop(lc.cfg.PIDFile, 2*time.Second); err != nil {
		return err
	}
	if n := process.KillStraysByConfig(lc.cfg.MihomoConfig, lc.cfg.PIDFile); n > 0 {
		fmt.Fprintf(os.Stderr, "launch: killed %d stray mihomo instance(s)\n", n)
	}
	controllerPort, err := lc.cfg.MihomoControllerPort()
	if err != nil {
		return err
	}
	_, err = process.Start(ctx, process.StartOptions{
		BinPath:      lc.cfg.MihomoBin,
		ConfigPath:   lc.cfg.MihomoConfig,
		PIDFile:      lc.cfg.PIDFile,
		LogFile:      lc.cfg.LogFile,
		Name:         "mihomo",
		Args:         []string{"-d", lc.cfg.MihomoHome, "-f", lc.cfg.MihomoConfig},
		ReadyPort:    controllerPort,
		ReadyTimeout: 15 * time.Second,
	})
	if err != nil {
		return err
	}

	client := coremihomo.NewClient(lc.cfg.MihomoController, secret, lc.cfg.TestTimeout())
	if _, err := client.Version(ctx); err != nil {
		_ = process.Stop(lc.cfg.PIDFile, 2*time.Second)
		return fmt.Errorf("mihomo started but API is unavailable: %w", err)
	}

	if target != nil {
		if err := switchMihomo(ctx, lc, client, *target, resetFailures); err != nil {
			_ = process.Stop(lc.cfg.PIDFile, 2*time.Second)
			return err
		}
		return nil
	}
	group, proxies, err := client.PrimaryGroup(ctx, lc.cfg.MihomoGroup)
	if err != nil {
		_ = process.Stop(lc.cfg.PIDFile, 2*time.Second)
		return err
	}
	if err := syncMihomoCache(lc, proxies); err != nil {
		fmt.Fprintf(os.Stderr, "warn: cache Mihomo API proxies: %v\n", err)
	}
	choice := chooseMihomoEntry(group, proxies, lc.cfg.Priority)
	if choice != "" && choice != group.Now {
		if err := client.Select(ctx, group.Name, choice); err != nil {
			_ = process.Stop(lc.cfg.PIDFile, 2*time.Second)
			return err
		}
		group.Now = choice
	}
	if err := persistMihomoSelection(ctx, lc, client, group.Name, resetFailures); err != nil {
		_ = process.Stop(lc.cfg.PIDFile, 2*time.Second)
		return err
	}
	return nil
}

func switchMihomo(ctx context.Context, lc *loadCtx, client *coremihomo.Client, target server.Server, resetFailures bool) error {
	group, _, err := client.PrimaryGroup(ctx, lc.cfg.MihomoGroup)
	if err != nil {
		return err
	}
	if !contains(group.All, target.Name) {
		return fmt.Errorf("mihomo: proxy %q is not in group %q", target.Name, group.Name)
	}
	if err := client.Select(ctx, group.Name, target.Name); err != nil {
		return err
	}
	return persistMihomoSelection(ctx, lc, client, group.Name, resetFailures)
}

func persistMihomoSelection(ctx context.Context, lc *loadCtx, client *coremihomo.Client, groupName string, resetFailures bool) error {
	group, proxies, err := client.PrimaryGroup(ctx, groupName)
	if err != nil {
		return err
	}
	actual := coremihomo.ResolveCurrent(group.Now, proxies)
	selected := server.Server{Name: actual, Protocol: "mihomo"}
	if cached, _, err := findServer(lc.cache, actual); err == nil {
		selected = cached
	}
	now := time.Now()
	lc.state.Active = &selected
	if lc.state.ConnectedAt.IsZero() {
		lc.state.ConnectedAt = now
	}
	if resetFailures {
		lc.state.WatchdogAttempts = 0
		lc.state.WatchdogPausedUntil = time.Time{}
	}
	return store.SaveState(lc.cfg.StateFile, lc.state)
}

func chooseMihomoEntry(group coremihomo.Proxy, proxies map[string]coremihomo.Proxy, priority []string) string {
	for _, name := range priority {
		if contains(group.All, name) {
			return name
		}
	}
	// Prefer a policy group over a single endpoint. URLTest/Fallback is
	// the desktop equivalent of RabbitHole's Dynamic Switch and keeps
	// working without restarting the core.
	for _, name := range group.All {
		p, ok := proxies[name]
		if !ok {
			continue
		}
		switch strings.ToLower(p.Type) {
		case "urltest", "fallback", "smart":
			return name
		}
	}
	if group.Now != "" {
		return group.Now
	}
	if len(group.All) > 0 {
		return group.All[0]
	}
	return ""
}

func cmdMihomoConnect(ctx context.Context, lc *loadCtx) error {
	if err := ensureFreshCache(ctx, lc, false); err != nil {
		return err
	}
	if len(lc.cache.Servers) == 0 {
		fmt.Fprintln(os.Stderr, "starting mihomo with a native provider profile…")
	} else {
		fmt.Fprintf(os.Stderr, "starting mihomo with %d proxies…\n", len(lc.cache.Servers))
	}
	return launchMihomo(ctx, lc, nil, true)
}

func cmdMihomoTest(ctx context.Context, lc *loadCtx) error {
	if running, _ := process.IsRunning(lc.cfg.PIDFile); !running {
		if err := launchMihomo(ctx, lc, nil, true); err != nil {
			return err
		}
	}
	client, err := mihomoClient(lc, lc.cfg.TestTimeout()+time.Second)
	if err != nil {
		return err
	}
	group, _, err := client.PrimaryGroup(ctx, lc.cfg.MihomoGroup)
	if err != nil {
		return err
	}
	delays, err := client.GroupDelay(ctx, group.Name, lc.cfg.TestURL, lc.cfg.TestTimeout())
	if err != nil {
		return err
	}
	results := make([]tester.Result, 0, len(lc.cache.Servers))
	for _, s := range lc.cache.Servers {
		delay, ok := delays[s.Name]
		r := tester.Result{Server: s, Alive: ok && delay > 0, Latency: time.Duration(delay) * time.Millisecond}
		if !r.Alive {
			r.Error = fmt.Errorf("no delay result")
		}
		results = append(results, r)
	}
	saveResults(lc, results)
	tester.SortByLatency(results)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tADDR\tLATENCY\tERROR")
	for _, r := range results {
		lat, errStr := "dead", ""
		if r.Alive {
			lat = fmt.Sprintf("%dms", r.Latency.Milliseconds())
		} else if r.Error != nil {
			errStr = r.Error.Error()
		}
		endpoint := r.Server.Endpoint()
		if r.Server.Address == "" {
			endpoint = "mihomo-managed"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Server.DisplayName(), endpoint, lat, errStr)
	}
	return w.Flush()
}

func cmdMihomoPing(ctx context.Context, lc *loadCtx) error {
	client, err := mihomoClient(lc, time.Second)
	if err != nil {
		return err
	}
	proxies, err := client.Proxies(ctx)
	if err != nil {
		return err
	}
	if err := syncMihomoCache(lc, proxies); err != nil {
		return err
	}
	if lc.state.Results == nil {
		lc.state.Results = make(map[string]store.TestResult, len(lc.cache.Servers))
	}
	now := time.Now()
	for _, s := range lc.cache.Servers {
		p, ok := proxies[s.Name]
		prev := lc.state.Results[s.Name]
		latency := prev.Latency
		if len(p.History) > 0 && p.History[len(p.History)-1].Delay > 0 {
			latency = time.Duration(p.History[len(p.History)-1].Delay) * time.Millisecond
		}
		lc.state.Results[s.Name] = store.TestResult{Name: s.Name, Alive: ok && p.Alive, Latency: latency, MeasuredAt: now}
	}
	lc.state.TestedAt = now
	return store.SaveState(lc.cfg.StateFile, lc.state)
}

func mihomoWaybarStatus(ctx context.Context, lc *loadCtx) waybar.Status {
	running, _ := process.IsRunning(lc.cfg.PIDFile)
	if !running {
		return waybar.Disconnected()
	}
	client, err := mihomoClient(lc, 600*time.Millisecond)
	if err != nil {
		return waybar.Error(err.Error())
	}
	group, proxies, err := client.PrimaryGroup(ctx, lc.cfg.MihomoGroup)
	if err != nil {
		return waybar.Error(err.Error())
	}
	actual := coremihomo.ResolveCurrent(group.Now, proxies)
	s := server.Server{Name: actual, Protocol: "mihomo"}
	if cached, _, err := findServer(lc.cache, actual); err == nil {
		s = cached
	}
	latency := time.Duration(0)
	if r, ok := lc.state.Results[actual]; ok && r.Alive {
		latency = r.Latency
	} else if p, ok := proxies[actual]; ok && len(p.History) > 0 {
		latency = time.Duration(p.History[len(p.History)-1].Delay) * time.Millisecond
	}
	tunActive := false
	localPort := lc.cfg.LocalPort()
	if cfg, err := client.Config(ctx); err == nil {
		tunActive = cfg.Tun.Enable
		if cfg.MixedPort > 0 {
			localPort = cfg.MixedPort
		}
	}
	return waybar.Connected(s, waybar.ConnectedOptions{
		Latency: latency, LocalPort: localPort, ConnectedAt: lc.state.ConnectedAt,
		SystemWide: lc.cfg.SystemWide, TunActive: tunActive, Profile: "mihomo / " + group.Name,
	})
}

func mihomoHealthy(lc *loadCtx) bool {
	running, _ := process.IsRunning(lc.cfg.PIDFile)
	if !running {
		return false
	}
	client, err := mihomoClient(lc, xrayProbeTimeout)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), xrayProbeTimeout)
	defer cancel()
	_, err = client.Version(ctx)
	return err == nil
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// syncMihomoCache makes provider-supplied proxies available to list/menu/use
// after the core has resolved them. Inline subscription entries retain their
// richer address/security metadata; API-only entries are represented by name
// and type because Mihomo deliberately does not expose credentials here.
func syncMihomoCache(lc *loadCtx, proxies map[string]coremihomo.Proxy) error {
	byName := make(map[string]server.Server, len(lc.cache.Servers)+len(proxies))
	for _, s := range lc.cache.Servers {
		// Address-less entries came from the previous API snapshot. Do not
		// retain them when a provider has removed or renamed a node.
		if s.Address != "" {
			byName[s.Name] = s
		}
	}
	for name, p := range proxies {
		kind := strings.ToLower(p.Type)
		if name == "" || coremihomo.IsGroup(p) {
			continue
		}
		switch kind {
		case "direct", "reject", "rejectdrop", "pass", "compatible":
			continue
		}
		if _, exists := byName[name]; !exists {
			byName[name] = server.Server{Name: name, Protocol: kind}
		}
	}
	if len(byName) == 0 {
		return nil
	}
	servers := make([]server.Server, 0, len(byName))
	for _, s := range byName {
		servers = append(servers, s)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	lc.cache.Servers = servers
	if lc.cache.SourceURL == "" {
		lc.cache.SourceURL = lc.cfg.SubscriptionURL
	}
	if lc.cache.FetchedAt.IsZero() {
		lc.cache.FetchedAt = time.Now()
	}
	return store.SaveCache(lc.cfg.CacheFile, lc.cache)
}
