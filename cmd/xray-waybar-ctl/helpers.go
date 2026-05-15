package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/appconfig"
	"github.com/yourgfslove/xray-waybar-ctl/internal/process"
	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
	"github.com/yourgfslove/xray-waybar-ctl/internal/store"
	"github.com/yourgfslove/xray-waybar-ctl/internal/subscription"
	"github.com/yourgfslove/xray-waybar-ctl/internal/xrayconfig"
)

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
