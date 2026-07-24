package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/appconfig"
	coremihomo "github.com/yourgfslove/xray-waybar-ctl/internal/mihomo"
	"github.com/yourgfslove/xray-waybar-ctl/internal/process"
	"github.com/yourgfslove/xray-waybar-ctl/internal/store"
	"github.com/yourgfslove/xray-waybar-ctl/internal/subscription"
)

// Opt-in end-to-end smoke test for process launch, controller readiness,
// API authentication, group discovery, state persistence and shutdown.
func TestLaunchMihomoEndToEnd(t *testing.T) {
	bin := os.Getenv("MIHOMO_BIN")
	if bin == "" {
		t.Skip("MIHOMO_BIN is not set")
	}
	mixedPort := freeTCPPort(t)
	controllerPort := freeTCPPort(t)
	tmp := t.TempDir()
	source := []byte(`[{"remarks":"SE","outbounds":[{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.10","port":443,"users":[{"id":"00000000-0000-0000-0000-000000000001","encryption":"none"}]}]},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"example.com"}}}]}]`)
	servers, errs := subscription.Parse(source)
	if len(errs) != 0 || len(servers) != 1 {
		t.Fatalf("parse fixture: servers=%d errs=%v", len(servers), errs)
	}

	cfg := &appconfig.Config{
		Core: "mihomo", SubscriptionURL: "https://example.invalid/sub",
		XrayPort: mixedPort, MihomoBin: bin, MihomoHome: filepath.Join(tmp, "home"),
		MihomoConfig:           filepath.Join(tmp, "home", "config.yaml"),
		MihomoSubscriptionFile: filepath.Join(tmp, "subscription.json"),
		MihomoSecretFile:       filepath.Join(tmp, "secret"),
		MihomoController:       "127.0.0.1:" + strconv.Itoa(controllerPort),
		PIDFile:                filepath.Join(tmp, "mihomo.pid"), LogFile: filepath.Join(tmp, "mihomo.log"),
		CacheFile: filepath.Join(tmp, "cache.json"), StateFile: filepath.Join(tmp, "state.json"),
		TestTimeoutMS: 3000, MihomoTUNStack: "mixed",
	}
	if err := os.WriteFile(cfg.MihomoSubscriptionFile, source, 0o600); err != nil {
		t.Fatal(err)
	}
	lc := &loadCtx{cfg: cfg, cache: &store.Cache{FetchedAt: time.Now(), SourceURL: cfg.SubscriptionURL, Servers: servers}, state: &store.State{}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := launchMihomo(ctx, lc, nil, true); err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer process.Stop(cfg.PIDFile, 2*time.Second)

	secret, err := subscription.LoadOrCreateHWID(cfg.MihomoSecretFile)
	if err != nil {
		t.Fatal(err)
	}
	client := coremihomo.NewClient(cfg.MihomoController, secret, time.Second)
	if _, err := client.Version(ctx); err != nil {
		t.Fatal(err)
	}
	group, _, err := client.PrimaryGroup(ctx, "VPN")
	if err != nil {
		t.Fatal(err)
	}
	if group.Now != "AUTO" || lc.state.Active == nil || lc.state.Active.Name != "SE" {
		t.Fatalf("group=%+v active=%+v", group, lc.state.Active)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
