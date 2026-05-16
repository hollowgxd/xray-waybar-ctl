// Package routing parses the HAPP-style routing JSON published by
// hydraponique/roscomvpn-routing and turns it into the typed Rules
// xrayconfig.Generate consumes.
//
// We do not invent the lists ourselves: every category in DirectSites/
// ProxySites/BlockSites is shipped by the upstream repo and kept in
// sync with the matching geosite.dat. This means a new "what's
// blocked in RU this week" tag lands in xray-waybar-ctl automatically
// the next time `update-geo` runs.
package routing

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
)

// happProfile is the on-disk schema of HAPP/*.JSON. Fields we do not
// use are omitted; json.Unmarshal ignores them silently.
//
// The format is HAPP's, not xray's — the values inside (geosite:foo,
// geoip:bar) happen to match xray's routing rule grammar 1:1, which
// is why this conversion is essentially a rename.
type happProfile struct {
	Name           string            `json:"Name"`
	DirectSites    []string          `json:"DirectSites"`
	DirectIp       []string          `json:"DirectIp"`
	ProxySites     []string          `json:"ProxySites"`
	ProxyIp        []string          `json:"ProxyIp"`
	BlockSites     []string          `json:"BlockSites"`
	BlockIp        []string          `json:"BlockIp"`
	DomainStrategy string            `json:"DomainStrategy"`
	RouteOrder     string            `json:"RouteOrder"`
	RemoteDns      string            `json:"RemoteDns"`
	DomesticDns    string            `json:"DomesticDns"`
	DnsHosts       map[string]string `json:"DnsHosts"`
}

// Rules is the typed routing model the rest of the codebase consumes.
// It is intentionally tag-agnostic: xrayconfig knows how to map
// proxy/direct/block to its outbound tags.
type Rules struct {
	Name string

	// Per-outbound classifiers. Domain entries can be bare hostnames,
	// `domain:foo.com`, `keyword:foo`, `regexp:^...$`, or `geosite:tag`.
	// IP entries can be CIDRs, bare IPs, or `geoip:tag`. We do not
	// validate here — xray will reject invalid tokens at start.
	DirectDomains []string
	DirectIPs     []string
	ProxyDomains  []string
	ProxyIPs      []string
	BlockDomains  []string
	BlockIPs      []string

	// DomainStrategy is xray's routing.domainStrategy. AsIs / IPIfNonMatch /
	// IPOnDemand. Empty → caller decides (xrayconfig defaults to AsIs).
	DomainStrategy string

	// RouteOrder controls which classifier wins when a domain matches
	// more than one list. Mirrors the HAPP keyword: "block-proxy-direct"
	// is the upstream default. Empty → block, proxy, direct.
	RouteOrder string

	// DNS — plain IPv4 servers only (HAPP uses DoH URLs as a HAPP
	// concept, not as xray DNS server addresses). RemoteDNS is the
	// "untrusted" server, DomesticDNS is the one trusted to give
	// correct answers for RU-side domains.
	RemoteDNS   string
	DomesticDNS string

	// Hosts is the xray dns.hosts map: name → IP, used to short-circuit
	// a few names whose authoritative DNS lies or geoblocks resolvers.
	Hosts map[string]string
}

// HasRules reports whether the profile actually defines any routing —
// an all-empty rules file (HAPP JSONSUB.JSON) is treated the same as
// proxy-all by the caller.
func (r *Rules) HasRules() bool {
	if r == nil {
		return false
	}
	return len(r.DirectDomains)+len(r.DirectIPs)+
		len(r.ProxyDomains)+len(r.ProxyIPs)+
		len(r.BlockDomains)+len(r.BlockIPs) > 0
}

// HasIPRules reports whether any rule targets IPs. xray's IPIfNonMatch
// strategy is only meaningful when there are IP-side rules to match
// against; otherwise AsIs is cheaper.
func (r *Rules) HasIPRules() bool {
	if r == nil {
		return false
	}
	return len(r.DirectIPs)+len(r.ProxyIPs)+len(r.BlockIPs) > 0
}

// LoadFile reads and parses a HAPP rules JSON file. A non-existent
// file returns (nil, nil) — the caller treats that as "no rules".
func LoadFile(path string) (*Rules, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("routing: read %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse turns a HAPP rules JSON payload into Rules.
func Parse(data []byte) (*Rules, error) {
	var p happProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("routing: parse: %w", err)
	}
	r := &Rules{
		Name:           p.Name,
		DirectDomains:  dedup(p.DirectSites),
		DirectIPs:      dedup(p.DirectIp),
		ProxyDomains:   dedup(p.ProxySites),
		ProxyIPs:       dedup(p.ProxyIp),
		BlockDomains:   dedup(p.BlockSites),
		BlockIPs:       dedup(p.BlockIp),
		DomainStrategy: p.DomainStrategy,
		RouteOrder:     p.RouteOrder,
		RemoteDNS:      sanitizeDNS(p.RemoteDns),
		DomesticDNS:    sanitizeDNS(p.DomesticDns),
		Hosts:          p.DnsHosts,
	}
	return r, nil
}

// sanitizeDNS keeps only plain-IP DNS server addresses. HAPP also ships
// DoH URLs in adjacent fields; xray accepts DoH too, but the URL forms
// vary by version and a misconfigured DoH server taking 30s to time
// out is the worst failure mode here — so we stick with IPs.
func sanitizeDNS(s string) string {
	if s == "" {
		return ""
	}
	if net.ParseIP(s) != nil {
		return s
	}
	return ""
}

func dedup(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
