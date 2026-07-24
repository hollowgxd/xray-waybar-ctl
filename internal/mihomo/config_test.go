package mihomo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
	"gopkg.in/yaml.v3"
)

func TestPrepareNativeConfigForcesLocalControlAndTUN(t *testing.T) {
	source := []byte(`
allow-lan: true
external-controller: 0.0.0.0:9999
external-ui-url: https://example.invalid/ui.zip
proxies:
  - {name: SE, type: vless, server: edge.example, port: 443, uuid: id}
proxy-groups:
  - {name: VPN, type: select, proxies: [SE]}
rules: [MATCH,VPN]
`)
	out, err := Prepare(source, nil, PrepareOptions{
		MixedPort: 1080, Controller: "127.0.0.1:9090", Secret: "secret", EnableTUN: true, TUNStack: "mixed",
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["allow-lan"] != false || cfg["external-controller"] != "127.0.0.1:9090" || cfg["secret"] != "secret" {
		t.Fatalf("unsafe control settings: %#v", cfg)
	}
	if _, ok := cfg["external-ui-url"]; ok {
		t.Fatal("external-ui-url was not removed")
	}
	tun := cfg["tun"].(map[string]any)
	if tun["enable"] != true || tun["stack"] != "mixed" {
		t.Fatalf("tun=%#v", tun)
	}
}

func TestPrepareBuildsFallbackForXrayJSON(t *testing.T) {
	servers := []server.Server{{
		Name: "SE", Protocol: "vless", Address: "edge.example", Port: 443, UUID: "id",
		Network: "tcp", Security: "reality", SNI: "cdn.example", PublicKey: "pub", ShortID: "abcd",
	}}
	out, err := Prepare([]byte(`[{"remarks":"SE"}]`), servers, PrepareOptions{
		MixedPort: 1080, Controller: "127.0.0.1:9090", MainGroup: "VPN",
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg["proxies"].([]any)) != 1 || len(cfg["proxy-groups"].([]any)) != 2 {
		t.Fatalf("unexpected fallback config: %#v", cfg)
	}
}

func TestPrepareCompletesProviderOnlyProfile(t *testing.T) {
	source := []byte(`
proxy-providers:
  remnawave:
    type: http
    url: https://example.invalid/sub
    path: ./providers/remnawave.yaml
`)
	out, err := Prepare(source, nil, PrepareOptions{MixedPort: 1080, Controller: "127.0.0.1:9090"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg["proxy-groups"].([]any)) != 2 {
		t.Fatalf("proxy-groups=%#v", cfg["proxy-groups"])
	}
	if len(cfg["rules"].([]any)) != 1 {
		t.Fatalf("rules=%#v", cfg["rules"])
	}
	provider := cfg["proxy-providers"].(map[string]any)["remnawave"].(map[string]any)
	health := provider["health-check"].(map[string]any)
	if health["enable"] != true || health["interval"] != 300 {
		t.Fatalf("health-check=%#v", health)
	}
}

// This is opt-in so ordinary contributors do not need Mihomo installed.
// CI/release checks can point MIHOMO_BIN at the official binary.
func TestPreparedConfigAcceptedByMihomo(t *testing.T) {
	bin := os.Getenv("MIHOMO_BIN")
	if bin == "" {
		t.Skip("MIHOMO_BIN is not set")
	}
	servers := []server.Server{{
		Name: "SE", Protocol: "vless", Address: "203.0.113.10", Port: 443,
		UUID: "00000000-0000-0000-0000-000000000001", Network: "xhttp", Security: "tls", SNI: "example.com",
		Path: "/api", Host: "example.com", XHTTPMode: "stream-one",
	}}
	out, err := Prepare([]byte(`[{"remarks":"SE"}]`), servers, PrepareOptions{
		MixedPort: 1080, Controller: "127.0.0.1:19090", Secret: "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-t", "-d", home, "-f", path)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mihomo rejected generated config: %v\n%s", err, combined)
	}
}
