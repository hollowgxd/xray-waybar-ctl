package appconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_AppliesDefaultsAndExpandsHome(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "app.yaml")
	body := []byte("subscription_url: \"https://x/y\"\n")
	if err := os.WriteFile(cfgPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.XrayPort != 1080 {
		t.Errorf("default xray_port: %d", cfg.XrayPort)
	}
	if cfg.TestURL == "" {
		t.Error("default test_url empty")
	}
	if strings.HasPrefix(cfg.LogFile, "~") {
		t.Errorf("log_file not expanded: %s", cfg.LogFile)
	}
	if cfg.SubscriptionUpdateInterval.Seconds() != 3600 {
		t.Errorf("interval: %v", cfg.SubscriptionUpdateInterval)
	}
}

func TestLoad_AllowsSubscriptionSetup(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(cfgPath, []byte("xray_port: 9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(cfgPath); err != nil {
		t.Fatalf("empty subscription should allow setup: %v", err)
	}
}
