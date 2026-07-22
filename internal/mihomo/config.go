// Package mihomo owns the small adapter between subscription documents
// and the external Mihomo process. It intentionally does not embed the
// core: the official binary remains independently updatable and the
// Waybar controller stays small.
package mihomo

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
	"gopkg.in/yaml.v3"
)

// PrepareOptions are local-only settings that must not be controlled by
// a remote subscription document.
type PrepareOptions struct {
	MixedPort   int
	Controller  string
	Secret      string
	EnableTUN   bool
	TUNStack    string
	RoutingMark int
	MainGroup   string
}

// Prepare preserves a native Mihomo YAML profile and overlays safe
// localhost runtime settings. If source is Xray JSON/base64 rather than
// Mihomo YAML, servers contains the already-normalized nodes and a
// compact native profile is generated instead.
func Prepare(source []byte, servers []server.Server, opts PrepareOptions) ([]byte, error) {
	if opts.MixedPort <= 0 {
		return nil, fmt.Errorf("mihomo: mixed port is required")
	}
	if opts.Controller == "" {
		return nil, fmt.Errorf("mihomo: controller is required")
	}

	var cfg map[string]any
	if err := yaml.Unmarshal(source, &cfg); err != nil || !hasProxySource(cfg) {
		var buildErr error
		cfg, buildErr = fallbackConfig(servers, opts.MainGroup)
		if buildErr != nil {
			if err != nil {
				return nil, fmt.Errorf("mihomo: source is not native YAML (%v), fallback failed: %w", err, buildErr)
			}
			return nil, buildErr
		}
	}

	// A subscription is untrusted input. Keep all control surfaces bound
	// to localhost and remove alternate controllers/UI download hooks.
	cfg["mixed-port"] = opts.MixedPort
	cfg["allow-lan"] = false
	cfg["bind-address"] = "127.0.0.1"
	cfg["external-controller"] = opts.Controller
	cfg["secret"] = opts.Secret
	delete(cfg, "external-controller-tls")
	delete(cfg, "external-controller-unix")
	delete(cfg, "external-controller-pipe")
	delete(cfg, "external-ui")
	delete(cfg, "external-ui-url")
	delete(cfg, "listeners")
	delete(cfg, "authentication")
	delete(cfg, "skip-auth-prefixes")

	if _, ok := cfg["unified-delay"]; !ok {
		cfg["unified-delay"] = true
	}
	if _, ok := cfg["tcp-concurrent"]; !ok {
		cfg["tcp-concurrent"] = true
	}
	profile := mapValue(cfg["profile"])
	profile["store-selected"] = true
	profile["store-fake-ip"] = true
	cfg["profile"] = profile
	ensurePolicy(cfg, opts.MainGroup)
	if len(mapValue(cfg["dns"])) == 0 {
		cfg["dns"] = defaultDNS()
	}

	tun := mapValue(cfg["tun"])
	tun["enable"] = opts.EnableTUN
	if opts.EnableTUN {
		if opts.TUNStack == "" {
			opts.TUNStack = "mixed"
		}
		tun["stack"] = strings.ToLower(opts.TUNStack)
		tun["auto-route"] = true
		tun["auto-detect-interface"] = true
		if _, ok := tun["dns-hijack"]; !ok {
			tun["dns-hijack"] = []string{"any:53", "tcp://any:53"}
		}
	}
	cfg["tun"] = tun
	if opts.RoutingMark != 0 {
		cfg["routing-mark"] = opts.RoutingMark
	} else {
		delete(cfg, "routing-mark")
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("mihomo: marshal runtime config: %w", err)
	}
	return out, nil
}

// ensurePolicy turns a proxy-only subscription into a complete runnable
// profile. Full templates keep their own groups and rules unchanged.
func ensurePolicy(cfg map[string]any, mainGroup string) {
	groups, hasGroups := cfg["proxy-groups"].([]any)
	if mainGroup == "" && hasGroups {
		for _, rawGroup := range groups {
			if name, _ := mapValue(rawGroup)["name"].(string); name != "" {
				mainGroup = name
				break
			}
		}
	}
	if mainGroup == "" {
		mainGroup = "VPN"
	}
	if !hasGroups || len(groups) == 0 {
		proxyNames := inlineProxyNames(cfg["proxies"])
		providerNames := make([]string, 0)
		providers := mapValue(cfg["proxy-providers"])
		for name, rawProvider := range providers {
			providerNames = append(providerNames, name)
			provider := mapValue(rawProvider)
			if len(mapValue(provider["health-check"])) == 0 {
				provider["health-check"] = map[string]any{
					"enable": true, "url": "https://www.gstatic.com/generate_204", "interval": 300, "lazy": true,
				}
			}
			providers[name] = provider
		}
		if len(providers) > 0 {
			cfg["proxy-providers"] = providers
		}
		sort.Strings(providerNames)

		auto := map[string]any{
			"name": "AUTO", "type": "url-test", "proxies": proxyNames,
			"url": "https://www.gstatic.com/generate_204", "interval": 300, "tolerance": 50,
		}
		selector := map[string]any{"name": mainGroup, "type": "select", "proxies": append([]string{"AUTO"}, proxyNames...)}
		if len(providerNames) > 0 {
			auto["use"] = providerNames
			selector["use"] = providerNames
		}
		cfg["proxy-groups"] = []any{selector, auto}
	}
	if rules, ok := cfg["rules"].([]any); !ok || len(rules) == 0 {
		cfg["rules"] = []string{"MATCH," + mainGroup}
	}
}

func inlineProxyNames(raw any) []string {
	items, _ := raw.([]any)
	names := make([]string, 0, len(items))
	for _, item := range items {
		if name, _ := mapValue(item)["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func defaultDNS() map[string]any {
	return map[string]any{
		"enable": true, "ipv6": false, "enhanced-mode": "fake-ip",
		"default-nameserver": []string{"1.1.1.1", "8.8.8.8"},
		"nameserver":         []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"},
	}
}

func hasProxySource(cfg map[string]any) bool {
	if len(cfg) == 0 {
		return false
	}
	if proxies, ok := cfg["proxies"].([]any); ok && len(proxies) > 0 {
		return true
	}
	if providers := mapValue(cfg["proxy-providers"]); len(providers) > 0 {
		return true
	}
	return false
}

func fallbackConfig(servers []server.Server, mainGroup string) (map[string]any, error) {
	if len(servers) == 0 {
		return nil, fmt.Errorf("mihomo: no proxies in subscription")
	}
	if mainGroup == "" {
		mainGroup = "VPN"
	}
	proxies := make([]any, 0, len(servers))
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		p, err := proxyFromServer(s)
		if err != nil {
			return nil, err
		}
		proxies = append(proxies, p)
		names = append(names, s.Name)
	}
	auto := "AUTO"
	return map[string]any{
		"mode":           "rule",
		"log-level":      "warning",
		"ipv6":           false,
		"tcp-concurrent": true,
		"unified-delay":  true,
		"proxies":        proxies,
		"proxy-groups": []any{
			map[string]any{"name": mainGroup, "type": "select", "proxies": append([]string{auto}, names...)},
			map[string]any{"name": auto, "type": "url-test", "proxies": names, "url": "https://www.gstatic.com/generate_204", "interval": 300, "tolerance": 50},
		},
		"dns": map[string]any{
			"enable":             true,
			"ipv6":               false,
			"enhanced-mode":      "fake-ip",
			"default-nameserver": []string{"1.1.1.1", "8.8.8.8"},
			"nameserver":         []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"},
		},
		"rules": []string{"MATCH," + mainGroup},
	}, nil
}

func proxyFromServer(s server.Server) (map[string]any, error) {
	if s.Name == "" || s.Address == "" || s.Port <= 0 {
		return nil, fmt.Errorf("mihomo: proxy is missing name/address/port")
	}
	p := map[string]any{
		"name":    s.Name,
		"type":    s.Protocol,
		"server":  s.Address,
		"port":    s.Port,
		"network": defaultString(s.Network, "tcp"),
		"udp":     true,
	}
	switch s.Protocol {
	case "vless":
		p["uuid"] = s.UUID
		p["flow"] = s.Flow
		if s.Encryption != "" && s.Encryption != "none" {
			p["encryption"] = s.Encryption
		}
	case "vmess":
		p["uuid"] = s.UUID
		p["alterId"] = s.AlterID
		p["cipher"] = defaultString(s.Encryption, "auto")
	case "trojan":
		p["password"] = s.Password
	default:
		return nil, fmt.Errorf("mihomo: unsupported protocol %q", s.Protocol)
	}

	if s.Security == "tls" || s.Security == "reality" || s.Protocol == "trojan" {
		p["tls"] = true
		p["servername"] = s.SNI
		p["client-fingerprint"] = defaultString(s.Fingerprint, "chrome")
		p["skip-cert-verify"] = s.AllowInsecure
	}
	if s.Security == "reality" {
		p["reality-opts"] = map[string]any{
			"public-key": s.PublicKey,
			"short-id":   s.ShortID,
			"spider-x":   s.SpiderX,
		}
	}
	if len(s.ALPN) > 0 {
		p["alpn"] = s.ALPN
	}
	switch s.Network {
	case "", "tcp":
	case "ws":
		p["ws-opts"] = map[string]any{"path": defaultString(s.Path, "/"), "headers": map[string]string{"Host": s.Host}}
	case "grpc":
		p["grpc-opts"] = map[string]any{"grpc-service-name": s.ServiceName}
	case "xhttp", "splithttp":
		if s.Protocol != "vless" {
			return nil, fmt.Errorf("mihomo: xhttp is only supported by vless")
		}
		p["network"] = "xhttp"
		opts := map[string]any{"path": defaultString(s.Path, "/")}
		if s.Host != "" {
			opts["host"] = s.Host
		}
		if s.XHTTPMode != "" {
			opts["mode"] = s.XHTTPMode
		}
		if len(s.XHTTPHeaders) > 0 {
			opts["headers"] = s.XHTTPHeaders
		}
		p["xhttp-opts"] = opts
	default:
		return nil, fmt.Errorf("mihomo: unsupported network %q", s.Network)
	}
	return p, nil
}

func mapValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
