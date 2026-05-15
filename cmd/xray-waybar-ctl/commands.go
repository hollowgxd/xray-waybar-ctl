package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/process"
	"github.com/yourgfslove/xray-waybar-ctl/internal/store"
	"github.com/yourgfslove/xray-waybar-ctl/internal/tester"
	"github.com/yourgfslove/xray-waybar-ctl/internal/waybar"
)

// cmdStatus emits one line of JSON for waybar. It must be fast — waybar
// polls it on an interval — and must never fail with a non-zero exit
// because waybar would then leave the previous icon stuck on screen.
func cmdStatus(_ context.Context) error {
	lc, err := loadAll()
	if err != nil {
		emitWaybar(waybar.Error(err.Error()))
		return nil
	}

	running, _ := process.IsRunning(lc.cfg.PIDFile)
	if !running || lc.state.Active == nil {
		emitWaybar(waybar.Disconnected())
		return nil
	}
	opt := waybar.ConnectedOptions{
		LocalPort:   lc.cfg.XrayPort,
		ConnectedAt: lc.state.ConnectedAt,
	}
	if r, ok := lc.state.Results[lc.state.Active.Name]; ok && r.Alive {
		opt.Latency = r.Latency
	}
	emitWaybar(waybar.Connected(*lc.state.Active, opt))
	return nil
}

func emitWaybar(s waybar.Status) {
	raw, _ := waybar.Marshal(s)
	os.Stdout.Write(raw)
	os.Stdout.Write([]byte{'\n'})
}

func cmdConnect(ctx context.Context) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	if err := ensureFreshCache(ctx, lc, false); err != nil {
		return err
	}
	if len(lc.cache.Servers) == 0 {
		return errNoServers
	}

	fmt.Fprintf(os.Stderr, "testing %d servers…\n", len(lc.cache.Servers))
	results := tester.Test(ctx, lc.cache.Servers, tester.Options{
		XrayBin:     lc.cfg.XrayBin,
		TestURL:     lc.cfg.TestURL,
		Timeout:     lc.cfg.TestTimeout(),
		Concurrency: lc.cfg.TestConcurrency,
		StartPort:   lc.cfg.XrayPort + 100, // avoid clashing with the live xray
	})
	saveResults(lc, results)

	var pickIdx int = -1
	if i := tester.PickByPriority(results, lc.cfg.Priority); i >= 0 {
		pickIdx = i
	} else {
		tester.SortByLatency(results)
		if len(results) > 0 && results[0].Alive {
			// PickByPriority used the pre-sort index; for the sort
			// fallback we already mutated results, so index 0 is the
			// best.
			pickIdx = 0
		}
	}
	if pickIdx < 0 {
		return fmt.Errorf("no alive servers found")
	}
	picked := results[pickIdx]
	fmt.Fprintf(os.Stderr, "connecting to %s (%dms)\n", picked.Server.DisplayName(), picked.Latency.Milliseconds())
	return launch(ctx, lc, picked.Server)
}

func cmdDisconnect(_ context.Context) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	if err := process.Stop(lc.cfg.PIDFile, 3*time.Second); err != nil {
		return err
	}
	lc.state.Active = nil
	lc.state.ConnectedAt = time.Time{}
	return store.SaveState(lc.cfg.StateFile, lc.state)
}

func cmdToggle(ctx context.Context) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	running, _ := process.IsRunning(lc.cfg.PIDFile)
	if running {
		return cmdDisconnect(ctx)
	}
	return cmdConnect(ctx)
}

func cmdReconnect(ctx context.Context) error {
	if err := cmdDisconnect(ctx); err != nil {
		return err
	}
	return cmdConnect(ctx)
}

func cmdUpdate(ctx context.Context) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	if err := ensureFreshCache(ctx, lc, true); err != nil {
		return err
	}
	fmt.Printf("cached %d servers from %s\n", len(lc.cache.Servers), lc.cfg.SubscriptionURL)
	return nil
}

func cmdList(_ context.Context) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	if len(lc.cache.Servers) == 0 {
		return errNoServers
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "#\tNAME\tPROTOCOL\tADDR\tSECURITY\tLATENCY\tSTATUS")
	for i, s := range lc.cache.Servers {
		latency := "—"
		status := ""
		if r, ok := lc.state.Results[s.Name]; ok {
			if r.Alive {
				latency = fmt.Sprintf("%dms", r.Latency.Milliseconds())
			} else {
				latency = "dead"
			}
		}
		if lc.state.Active != nil && lc.state.Active.Name == s.Name {
			status = "*active"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			i, s.DisplayName(), s.Protocol, s.Endpoint(), s.Security, latency, status)
	}
	return w.Flush()
}

func cmdTest(ctx context.Context) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	if err := ensureFreshCache(ctx, lc, false); err != nil {
		return err
	}
	if len(lc.cache.Servers) == 0 {
		return errNoServers
	}
	results := tester.Test(ctx, lc.cache.Servers, tester.Options{
		XrayBin:     lc.cfg.XrayBin,
		TestURL:     lc.cfg.TestURL,
		Timeout:     lc.cfg.TestTimeout(),
		Concurrency: lc.cfg.TestConcurrency,
		StartPort:   lc.cfg.XrayPort + 100,
	})
	saveResults(lc, results)

	tester.SortByLatency(results)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tADDR\tLATENCY\tERROR")
	for _, r := range results {
		lat := "dead"
		errStr := ""
		if r.Alive {
			lat = fmt.Sprintf("%dms", r.Latency.Milliseconds())
		} else if r.Error != nil {
			errStr = r.Error.Error()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			r.Server.DisplayName(), r.Server.Endpoint(), lat, errStr)
	}
	return w.Flush()
}

func cmdUse(ctx context.Context, name string) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	if err := ensureFreshCache(ctx, lc, false); err != nil {
		return err
	}
	s, _, err := findServer(lc.cache, name)
	if err != nil {
		return err
	}
	return launch(ctx, lc, s)
}

// cmdUseDir picks the next/previous server relative to the active one.
// If there is no active server, starts from the head of the list.
func cmdUseDir(ctx context.Context, step int) error {
	lc, err := loadAll()
	if err != nil {
		return err
	}
	if err := ensureFreshCache(ctx, lc, false); err != nil {
		return err
	}
	if len(lc.cache.Servers) == 0 {
		return errNoServers
	}
	idx := activeIndex(lc)
	if idx < 0 {
		idx = 0
	} else {
		idx = (idx + step + len(lc.cache.Servers)) % len(lc.cache.Servers)
	}
	return launch(ctx, lc, lc.cache.Servers[idx])
}

func saveResults(lc *loadCtx, results []tester.Result) {
	if lc.state.Results == nil {
		lc.state.Results = make(map[string]store.TestResult, len(results))
	}
	now := time.Now()
	for _, r := range results {
		tr := store.TestResult{
			Name:       r.Server.Name,
			Latency:    r.Latency,
			Alive:      r.Alive,
			MeasuredAt: now,
		}
		if r.Error != nil {
			tr.Error = r.Error.Error()
		}
		lc.state.Results[r.Server.Name] = tr
	}
	lc.state.TestedAt = now
	_ = store.SaveState(lc.cfg.StateFile, lc.state)
}
