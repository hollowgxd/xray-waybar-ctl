package routing

import (
	"strings"
	"testing"
)

// happDefault is a trimmed copy of hydraponique HAPP/DEFAULT.JSON used
// to lock down the parse → struct mapping. Real shape: Direct/Proxy/
// Block sites and IPs, plus DnsHosts and DomainStrategy.
const happDefault = `{
  "Name": "RoscomVPN",
  "RemoteDns": "8.8.8.8",
  "DomesticDns": "77.88.8.8",
  "RemoteDNSType": "DoH",
  "RemoteDNSDomain": "https://8.8.8.8/dns-query",
  "DnsHosts": {
    "lkfl2.nalog.ru": "213.24.64.175"
  },
  "RouteOrder": "block-proxy-direct",
  "DirectSites": ["geosite:private", "geosite:category-ru", "geosite:whitelist"],
  "DirectIp":    ["geoip:private", "geoip:direct"],
  "ProxySites":  ["geosite:youtube", "geosite:telegram"],
  "ProxyIp":     [],
  "BlockSites":  ["geosite:win-spy", "geosite:torrent"],
  "BlockIp":     [],
  "DomainStrategy": "IPIfNonMatch"
}`

func TestParse_HAPPDefault(t *testing.T) {
	r, err := Parse([]byte(happDefault))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Name != "RoscomVPN" {
		t.Errorf("name = %q", r.Name)
	}
	if !r.HasRules() {
		t.Fatalf("expected HasRules=true")
	}
	if !r.HasIPRules() {
		t.Fatalf("expected HasIPRules=true")
	}
	if r.DomainStrategy != "IPIfNonMatch" {
		t.Errorf("domain strategy = %q", r.DomainStrategy)
	}
	if r.RouteOrder != "block-proxy-direct" {
		t.Errorf("route order = %q", r.RouteOrder)
	}
	if r.RemoteDNS != "8.8.8.8" || r.DomesticDNS != "77.88.8.8" {
		t.Errorf("dns servers = %q / %q", r.RemoteDNS, r.DomesticDNS)
	}
	if got := r.Hosts["lkfl2.nalog.ru"]; got != "213.24.64.175" {
		t.Errorf("host override = %q", got)
	}
	wantProxy := []string{"geosite:youtube", "geosite:telegram"}
	if !equalStrings(r.ProxyDomains, wantProxy) {
		t.Errorf("proxy domains = %v, want %v", r.ProxyDomains, wantProxy)
	}
	wantBlock := []string{"geosite:win-spy", "geosite:torrent"}
	if !equalStrings(r.BlockDomains, wantBlock) {
		t.Errorf("block domains = %v, want %v", r.BlockDomains, wantBlock)
	}
}

// TestParse_JSONSubEmpty confirms HAPP/JSONSUB.JSON (all-empty arrays)
// is treated as "no rules" — the caller will then render proxy-all.
func TestParse_JSONSubEmpty(t *testing.T) {
	const empty = `{"Name":"sub","DirectSites":[],"ProxySites":[],"BlockSites":[]}`
	r, err := Parse([]byte(empty))
	if err != nil {
		t.Fatal(err)
	}
	if r.HasRules() {
		t.Errorf("expected HasRules=false for all-empty rules file")
	}
}

func TestSanitizeDNS_RejectsDoH(t *testing.T) {
	r, err := Parse([]byte(`{"RemoteDns":"https://1.1.1.1/dns-query"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.RemoteDNS != "" {
		t.Errorf("expected DoH URL to be dropped, got %q", r.RemoteDNS)
	}
}

func TestDedup(t *testing.T) {
	in := []string{"a", "b", "a", "", "c", "b"}
	out := dedup(in)
	want := []string{"a", "b", "c"}
	if !equalStrings(out, want) {
		t.Errorf("dedup = %v, want %v", out, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Sanity: a parse error doesn't panic and returns a wrapped error.
func TestParse_GarbageReturnsError(t *testing.T) {
	_, err := Parse([]byte("not json"))
	if err == nil || !strings.Contains(err.Error(), "routing:") {
		t.Errorf("expected wrapped routing error, got %v", err)
	}
}
