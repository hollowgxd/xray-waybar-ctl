package subscription

import (
	"encoding/base64"
	"strings"
	"testing"
)

const realVLESS = "vless://b83d044e-b9e7-48bc-856c-578868f81b0c@195.189.96.224:8444" +
	"?security=reality&type=tcp&flow=xtls-rprx-vision" +
	"&sni=m.vk.ru&fp=chrome&pbk=PUBKEY_HERE&sid=deadbeef&spx=%2F#DE-Frankfurt-2"

func TestParseVLESS_RealityFromTZ(t *testing.T) {
	s, err := ParseURI(realVLESS)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	checks := map[string]string{
		"protocol":    s.Protocol,
		"address":     s.Address,
		"uuid":        s.UUID,
		"flow":        s.Flow,
		"sni":         s.SNI,
		"security":    s.Security,
		"network":     s.Network,
		"fingerprint": s.Fingerprint,
		"pbk":         s.PublicKey,
		"sid":         s.ShortID,
		"name":        s.Name,
	}
	want := map[string]string{
		"protocol":    "vless",
		"address":     "195.189.96.224",
		"uuid":        "b83d044e-b9e7-48bc-856c-578868f81b0c",
		"flow":        "xtls-rprx-vision",
		"sni":         "m.vk.ru",
		"security":    "reality",
		"network":     "tcp",
		"fingerprint": "chrome",
		"pbk":         "PUBKEY_HERE",
		"sid":         "deadbeef",
		"name":        "DE-Frankfurt-2",
	}
	for k, got := range checks {
		if got != want[k] {
			t.Errorf("%s: got %q want %q", k, got, want[k])
		}
	}
	if s.Port != 8444 {
		t.Errorf("port: got %d want 8444", s.Port)
	}
	if s.SpiderX != "/" {
		t.Errorf("spiderX: got %q want %q", s.SpiderX, "/")
	}
}

func TestParseVLESS_DefaultsWhenParamsMissing(t *testing.T) {
	s, err := ParseURI("vless://aaaa-bbbb@host.example:443")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Network != "tcp" {
		t.Errorf("network default: %q", s.Network)
	}
	if s.Security != "none" {
		t.Errorf("security default: %q", s.Security)
	}
	if s.Encryption != "none" {
		t.Errorf("encryption default: %q", s.Encryption)
	}
	if s.Name != "host.example:443" {
		t.Errorf("name fallback: %q", s.Name)
	}
}

func TestParseVMess(t *testing.T) {
	body := `{"v":"2","ps":"Tokyo","add":"jp.example","port":"443","id":"u","aid":"0","scy":"auto","net":"ws","tls":"tls","sni":"jp.example","path":"/ray","host":"jp.example"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	s, err := ParseURI("vmess://" + encoded)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "Tokyo" {
		t.Errorf("ps: %q", s.Name)
	}
	if s.Port != 443 {
		t.Errorf("port: %d", s.Port)
	}
	if s.Network != "ws" || s.Path != "/ray" {
		t.Errorf("ws fields: %+v", s)
	}
	if s.Security != "tls" {
		t.Errorf("tls: %q", s.Security)
	}
}

func TestParseUnsupportedScheme(t *testing.T) {
	if _, err := ParseURI("ss://abc@host:1"); err == nil {
		t.Fatal("expected error for ss://")
	}
}

func TestParseSubscription_Base64WrappedLines(t *testing.T) {
	second := "vless://aaaa-bbbb@1.2.3.4:443?security=tls&sni=foo.example#second"
	raw := realVLESS + "\n" + second
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	// Insert newlines every 50 chars to simulate wrapped base64.
	var wrapped strings.Builder
	for i, r := range encoded {
		if i > 0 && i%50 == 0 {
			wrapped.WriteByte('\n')
		}
		wrapped.WriteRune(r)
	}
	servers, errs := Parse([]byte(wrapped.String()))
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(servers) != 2 {
		t.Fatalf("want 2 servers, got %d", len(servers))
	}
	if servers[1].Name != "second" {
		t.Errorf("second name: %q", servers[1].Name)
	}
}

func TestParseSubscription_PlainText(t *testing.T) {
	servers, errs := Parse([]byte(realVLESS + "\n"))
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(servers) != 1 {
		t.Fatalf("want 1, got %d", len(servers))
	}
}

func TestParseSubscription_SkipsBadLines(t *testing.T) {
	raw := realVLESS + "\nnot-a-uri\nss://abc@host:1\n" + realVLESS + "#ok"
	servers, errs := Parse([]byte(raw))
	if len(servers) != 2 {
		t.Errorf("want 2 ok, got %d", len(servers))
	}
	if len(errs) != 2 {
		t.Errorf("want 2 errs, got %d: %v", len(errs), errs)
	}
}
