package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/process"
	"github.com/yourgfslove/xray-waybar-ctl/internal/store"
	"github.com/yourgfslove/xray-waybar-ctl/internal/subscription"
	"github.com/yourgfslove/xray-waybar-ctl/internal/sysmode"
)

func cmdSubscription(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errUsage
		}
		lc, err := loadAll()
		if err != nil {
			return err
		}
		if len(lc.registry.Items) == 0 {
			fmt.Println("No subscriptions. Add one: xray-waybar-ctl subscription add NAME")
			return nil
		}
		for _, name := range lc.registry.Names() {
			marker := " "
			if name == lc.subscriptionName {
				marker = "*"
			}
			cache, err := store.LoadCache(cachePathFor(lc.cfg.CacheFile, name))
			if err != nil {
				return err
			}
			count := 0
			if cache != nil && cache.SourceURL == lc.registry.Items[name] {
				count = len(cache.Servers)
			}
			fmt.Printf("%s %-20s %d cached servers\n", marker, name, count)
		}
		return nil
	case "add":
		if len(args) != 2 {
			return errUsage
		}
		raw, err := readSubscriptionURL(os.Stdin)
		if err != nil {
			return err
		}
		return addSubscription(ctx, args[1], raw)
	case "use":
		if len(args) != 2 {
			return errUsage
		}
		return useSubscription(ctx, args[1])
	case "remove":
		if len(args) != 2 {
			return errUsage
		}
		return removeSubscription(ctx, args[1])
	case "update":
		if len(args) > 2 {
			return errUsage
		}
		if len(args) == 1 {
			return cmdUpdate(ctx)
		}
		return updateNamedSubscription(ctx, args[1])
	default:
		return fmt.Errorf("unknown subscription command %q: %w", args[0], errUsage)
	}
}

func readSubscriptionURL(r io.Reader) (string, error) {
	fmt.Fprint(os.Stderr, "Subscription URL: ")
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	raw := strings.TrimSpace(line)
	if raw == "" {
		return "", errors.New("subscription URL is empty")
	}
	if err := subscription.ValidateURL(raw); err != nil {
		return "", err
	}
	return raw, nil
}

func addSubscription(ctx context.Context, name, raw string) error {
	if err := subscription.ValidateName(name); err != nil {
		return err
	}
	if err := subscription.ValidateURL(raw); err != nil {
		return err
	}
	lock, err := acquireUserLock()
	if err != nil {
		return err
	}
	defer releaseWatchdogLock(lock)
	lc, err := loadAll()
	if err != nil {
		return err
	}
	if _, exists := lc.registry.Items[name]; exists {
		return fmt.Errorf("subscription %q exists; remove it first or use a new name", name)
	}
	// Validate the feed before persisting. Existing selections remain intact on failure.
	probe := *lc
	cfg := *lc.cfg
	probe.cfg = &cfg
	probe.cfg.SubscriptionURL = raw
	probe.subscriptionName = name
	probe.cachePath = cachePathFor(cfg.CacheFile, name)
	probe.cache = &store.Cache{}
	if err := ensureFreshCache(ctx, &probe, true); err != nil {
		return err
	}
	lc.registry.Items[name] = raw
	if lc.registry.Active == "" {
		lc.registry.Active = name
	}
	if err := subscription.SaveRegistry(lc.cfg.SubscriptionsFile, lc.registry); err != nil {
		return err
	}
	fmt.Printf("Added %q (%d servers). Active: %s\n", name, len(probe.cache.Servers), lc.registry.Active)
	return nil
}

func removeSubscription(ctx context.Context, name string) error {
	lock, err := acquireUserLock()
	if err != nil {
		return err
	}
	lc, err := loadAll()
	if err != nil {
		releaseWatchdogLock(lock)
		return err
	}
	if _, ok := lc.registry.Items[name]; !ok {
		releaseWatchdogLock(lock)
		return fmt.Errorf("subscription %q not found", name)
	}
	active := name == lc.subscriptionName
	running := false
	if active {
		running, _ = process.IsRunning(lc.cfg.PIDFile)
		if running {
			if lc.cfg.SystemWide {
				if err := sysmode.Stop(ctx); err != nil {
					releaseWatchdogLock(lock)
					return err
				}
			}
			if err := process.Stop(lc.cfg.PIDFile, 3*time.Second); err != nil {
				releaseWatchdogLock(lock)
				return err
			}
		}
		lc.state.Active = nil
		lc.state.ConnectedAt = time.Time{}
		lc.state.Results = nil
	}
	delete(lc.registry.Items, name)
	if active {
		lc.registry.Active = ""
		if len(lc.registry.Items) > 0 {
			lc.registry.Active = lc.registry.Names()[0]
		}
		lc.state.Subscription = lc.registry.Active
		if err := store.SaveState(lc.cfg.StateFile, lc.state); err != nil {
			releaseWatchdogLock(lock)
			return err
		}
	}
	if err := subscription.SaveRegistry(lc.cfg.SubscriptionsFile, lc.registry); err != nil {
		releaseWatchdogLock(lock)
		return err
	}
	if err := os.Remove(cachePathFor(lc.cfg.CacheFile, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "warn: couldn't remove old subscription cache: %v\n", err)
	}
	releaseWatchdogLock(lock)
	fmt.Printf("Removed %q", name)
	if active && lc.registry.Active != "" {
		fmt.Printf("; active: %q", lc.registry.Active)
	}
	fmt.Println()
	if running && lc.registry.Active != "" {
		return cmdConnect(ctx)
	}
	return nil
}

func updateNamedSubscription(ctx context.Context, name string) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	raw, ok := lc.registry.Items[name]
	if !ok {
		return fmt.Errorf("subscription %q not found", name)
	}
	probe := *lc
	cfg := *lc.cfg
	probe.cfg = &cfg
	probe.cfg.SubscriptionURL = raw
	probe.subscriptionName = name
	probe.cachePath = cachePathFor(cfg.CacheFile, name)
	probe.cache, err = store.LoadCache(probe.cachePath)
	if err != nil {
		return err
	}
	if probe.cache == nil {
		probe.cache = &store.Cache{}
	}
	if err := ensureFreshCache(ctx, &probe, true); err != nil {
		return err
	}
	fmt.Printf("Updated %q: %d servers\n", name, len(probe.cache.Servers))
	return nil
}

func useSubscription(ctx context.Context, name string) error {
	lock, err := acquireUserLock()
	if err != nil {
		return err
	}
	lc, err := loadAll()
	if err != nil {
		releaseWatchdogLock(lock)
		return err
	}
	raw, ok := lc.registry.Items[name]
	if !ok {
		releaseWatchdogLock(lock)
		return fmt.Errorf("subscription %q not found", name)
	}
	if name == lc.subscriptionName {
		releaseWatchdogLock(lock)
		fmt.Printf("%q is already active\n", name)
		return nil
	}
	// Fetch and parse before disrupting a live connection.
	probe := *lc
	cfg := *lc.cfg
	probe.cfg = &cfg
	probe.cfg.SubscriptionURL = raw
	probe.subscriptionName = name
	probe.cachePath = cachePathFor(cfg.CacheFile, name)
	probe.cache, err = store.LoadCache(probe.cachePath)
	if err != nil {
		releaseWatchdogLock(lock)
		return err
	}
	if probe.cache == nil {
		probe.cache = &store.Cache{}
	}
	if err := ensureFreshCache(ctx, &probe, false); err != nil {
		releaseWatchdogLock(lock)
		return err
	}
	running, _ := process.IsRunning(lc.cfg.PIDFile)
	if running {
		if lc.cfg.SystemWide {
			if err := sysmode.Stop(ctx); err != nil {
				releaseWatchdogLock(lock)
				return err
			}
		}
		if err := process.Stop(lc.cfg.PIDFile, 3*time.Second); err != nil {
			releaseWatchdogLock(lock)
			return err
		}
	}
	lc.state.Active = nil
	lc.state.ConnectedAt = time.Time{}
	lc.state.Results = nil
	lc.state.Subscription = name
	if err := store.SaveState(lc.cfg.StateFile, lc.state); err != nil {
		releaseWatchdogLock(lock)
		return err
	}
	lc.registry.Active = name
	if err := subscription.SaveRegistry(lc.cfg.SubscriptionsFile, lc.registry); err != nil {
		releaseWatchdogLock(lock)
		return err
	}
	releaseWatchdogLock(lock)
	fmt.Printf("Switched to %q (%d servers)\n", name, len(probe.cache.Servers))
	if running {
		return cmdConnect(ctx)
	}
	return nil
}

const subscriptionMenuMarker = "☰"
const addSubscriptionMarker = "＋"

func cmdMenuSubscriptions(ctx context.Context) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	var lines strings.Builder
	lines.WriteString(addSubscriptionMarker + menuSep + "Add subscription URL…\n")
	current := -1
	for i, name := range lc.registry.Names() {
		mark := "  "
		if name == lc.subscriptionName {
			mark = "→ "
			current = i + 1
		}
		fmt.Fprintf(&lines, "%s%s%s%s%s\n", mark, subscriptionMenuMarker, menuSep, name, menuSep)
	}
	choice, err := runWalker(ctx, lines.String(), "Subscriptions", current)
	if err != nil || choice == "" {
		return err
	}
	if strings.HasPrefix(choice, addSubscriptionMarker) {
		return spawnDetached("menu-subscription-add")
	}
	parts := strings.Split(choice, menuSep)
	if len(parts) < 2 {
		return errors.New("invalid subscription menu selection")
	}
	return useSubscription(ctx, strings.TrimSpace(parts[1]))
}

func cmdMenuSubscriptionAdd(ctx context.Context) error {
	raw, err := runWalker(ctx, "", "Paste subscription URL", -1)
	if err != nil || raw == "" {
		return err
	}
	lc, err := loadAll()
	if err != nil {
		return err
	}
	name := "feed-1"
	for i := 1; ; i++ {
		name = fmt.Sprintf("feed-%d", i)
		if _, ok := lc.registry.Items[name]; !ok {
			break
		}
	}
	if err := addSubscription(ctx, name, raw); err != nil {
		return err
	}
	return useSubscription(ctx, name)
}

// Detached Waybar menus have stderr redirected to /dev/null; surface failures
// without risking a token-bearing HTTP diagnostic in a desktop notification.
func notifySubscriptionMenuError(err error) {
	if err == nil || !hasCommand("notify-send") {
		return
	}
	_ = exec.Command("notify-send", "Xray subscription", "Operation failed. Run xray-waybar-ctl subscription update for details.").Run()
}
