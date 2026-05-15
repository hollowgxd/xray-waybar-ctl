package xrayconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
)

func TestGenerate_VLESSReality(t *testing.T) {
	s := server.Server{
		Name:        "DE-2",
		Protocol:    "vless",
		Address:     "195.189.96.224",
		Port:        8444,
		UUID:        "b83d044e-b9e7-48bc-856c-578868f81b0c",
		Encryption:  "none",
		Flow:        "xtls-rprx-vision",
		Network:     "tcp",
		Security:    "reality",
		SNI:         "m.vk.ru",
		Fingerprint: "chrome",
		PublicKey:   "PUBKEY",
		ShortID:     "deadbeef",
	}
	raw, err := Generate(s, Options{SocksPort: 1080})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	outbounds := parsed["outbounds"].([]any)
	if len(outbounds) != 3 {
		t.Fatalf("expected proxy/direct/block, got %d", len(outbounds))
	}
	proxy := outbounds[0].(map[string]any)
	if proxy["protocol"] != "vless" {
		t.Errorf("protocol: %v", proxy["protocol"])
	}
	stream := proxy["streamSettings"].(map[string]any)
	if stream["security"] != "reality" {
		t.Errorf("security: %v", stream["security"])
	}
	reality := stream["realitySettings"].(map[string]any)
	for k, want := range map[string]any{
		"serverName":  "m.vk.ru",
		"fingerprint": "chrome",
		"publicKey":   "PUBKEY",
		"shortId":     "deadbeef",
	} {
		if reality[k] != want {
			t.Errorf("reality.%s = %v want %v", k, reality[k], want)
		}
	}
	settings := proxy["settings"].(map[string]any)
	user := settings["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow: %v", user["flow"])
	}
}

func TestGenerate_VMessWS_TLS(t *testing.T) {
	s := server.Server{
		Protocol: "vmess",
		Address:  "host.example",
		Port:     443,
		UUID:     "uuid",
		Network:  "ws",
		Security: "tls",
		SNI:      "host.example",
		Path:     "/ray",
		Host:     "host.example",
	}
	raw, err := Generate(s, Options{SocksPort: 1080})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(string(raw), `"wsSettings"`) {
		t.Errorf("expected wsSettings in output:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"tlsSettings"`) {
		t.Errorf("expected tlsSettings in output")
	}
}

func TestGenerate_RejectsRealityWithoutPubkey(t *testing.T) {
	s := server.Server{
		Protocol: "vless",
		Address:  "host", Port: 1,
		UUID:     "u",
		Network:  "tcp", Security: "reality",
	}
	if _, err := Generate(s, Options{SocksPort: 1080}); err == nil {
		t.Fatal("expected error for reality without publicKey")
	}
}

func TestGenerate_RequiresSocksPort(t *testing.T) {
	s := server.Server{Protocol: "vless", Address: "a", Port: 1, UUID: "u", Network: "tcp"}
	if _, err := Generate(s, Options{}); err == nil {
		t.Fatal("expected error without SocksPort")
	}
}
