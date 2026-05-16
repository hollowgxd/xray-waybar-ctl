package xrayconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yourgfslove/xray-waybar-ctl/internal/routing"
	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
)

// minimalServer is just enough to make buildProxyOutbound happy.
func minimalServer() server.Server {
	return server.Server{
		Name:     "test",
		Protocol: "trojan",
		Address:  "1.2.3.4",
		Port:     443,
		Password: "x",
		Network:  "tcp",
	}
}

// decode unmarshals the generated xray.json so tests can introspect it
// without scraping strings.
type generated struct {
	Log     map[string]string `json:"log"`
	DNS     *DNSCfg           `json:"dns"`
	Routing struct {
		DomainStrategy string        `json:"domainStrategy"`
		Rules          []RoutingRule `json:"rules"`
	} `json:"routing"`
}

func decode(t *testing.T, raw []byte) generated {
	t.Helper()
	var g generated
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return g
}

func TestGenerate_ProxyAll_NoRules(t *testing.T) {
	raw, err := Generate(minimalServer(), Options{SocksPort: 1080})
	if err != nil {
		t.Fatal(err)
	}
	g := decode(t, raw)
	if len(g.Routing.Rules) != 0 {
		t.Errorf("expected empty rules for proxy-all, got %v", g.Routing.Rules)
	}
	if g.DNS != nil {
		t.Errorf("expected no DNS block for proxy-all, got %+v", g.DNS)
	}
	if g.Routing.DomainStrategy != "AsIs" {
		t.Errorf("expected AsIs, got %q", g.Routing.DomainStrategy)
	}
	if g.Log["access"] != "none" {
		t.Errorf("expected log.access=none, got %q", g.Log["access"])
	}
}

func TestGenerate_ForceAllDirect(t *testing.T) {
	raw, err := Generate(minimalServer(), Options{SocksPort: 1080, ForceAllDirect: true})
	if err != nil {
		t.Fatal(err)
	}
	g := decode(t, raw)
	if len(g.Routing.Rules) != 1 || g.Routing.Rules[0].OutboundTag != "direct" {
		t.Errorf("expected single direct rule, got %+v", g.Routing.Rules)
	}
	if g.DNS != nil {
		t.Errorf("expected no DNS block in direct mode, got %+v", g.DNS)
	}
}

func TestGenerate_WithRules_OrdersBlockProxyDirect(t *testing.T) {
	r := &routing.Rules{
		BlockDomains:  []string{"geosite:win-spy"},
		ProxyDomains:  []string{"geosite:telegram"},
		DirectDomains: []string{"geosite:category-ru"},
		DirectIPs:     []string{"geoip:direct"},
		RouteOrder:    "block-proxy-direct",
	}
	raw, err := Generate(minimalServer(), Options{SocksPort: 1080, Rules: r})
	if err != nil {
		t.Fatal(err)
	}
	g := decode(t, raw)
	tags := make([]string, 0, len(g.Routing.Rules))
	for _, rule := range g.Routing.Rules {
		tags = append(tags, rule.OutboundTag)
	}
	want := "block,proxy,direct,direct" // direct emitted twice: once for domains, once for ips
	if got := strings.Join(tags, ","); got != want {
		t.Errorf("rule tag order = %q, want %q", got, want)
	}
	// IP rules present → IPIfNonMatch
	if g.Routing.DomainStrategy != "IPIfNonMatch" {
		t.Errorf("expected IPIfNonMatch with IP rules, got %q", g.Routing.DomainStrategy)
	}
	if g.DNS == nil {
		t.Fatalf("expected DNS block when IPIfNonMatch")
	}
}

func TestGenerate_DomainOnlyRules_AsIsNoDNS(t *testing.T) {
	r := &routing.Rules{
		ProxyDomains:  []string{"geosite:telegram"},
		DirectDomains: []string{"geosite:category-ru"},
	}
	raw, err := Generate(minimalServer(), Options{SocksPort: 1080, Rules: r})
	if err != nil {
		t.Fatal(err)
	}
	g := decode(t, raw)
	if g.Routing.DomainStrategy != "AsIs" {
		t.Errorf("expected AsIs without IP rules, got %q", g.Routing.DomainStrategy)
	}
	if g.DNS != nil {
		t.Errorf("expected no DNS block when no IP rules and no hosts, got %+v", g.DNS)
	}
}

func TestGenerate_HostsTriggerDNSEvenWithoutIPRules(t *testing.T) {
	r := &routing.Rules{
		ProxyDomains: []string{"geosite:telegram"},
		Hosts:        map[string]string{"lkfl2.nalog.ru": "213.24.64.175"},
	}
	raw, err := Generate(minimalServer(), Options{SocksPort: 1080, Rules: r})
	if err != nil {
		t.Fatal(err)
	}
	g := decode(t, raw)
	if g.DNS == nil || g.DNS.Hosts["lkfl2.nalog.ru"] != "213.24.64.175" {
		t.Errorf("expected DNS.hosts override to be emitted, got %+v", g.DNS)
	}
}
