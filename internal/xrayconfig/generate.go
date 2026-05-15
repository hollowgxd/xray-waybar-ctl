package xrayconfig

import (
	"encoding/json"
	"fmt"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
)

// Options controls the generated xray.json. SocksPort is required;
// LogLevel defaults to "warning" when empty.
type Options struct {
	SocksPort int
	LogLevel  string
}

// Generate renders a complete xray-core config that routes a local
// SOCKS5 listener through the given upstream server.
//
// The output is deterministic, indented JSON suitable for writing to
// disk and for diff-friendly inspection.
func Generate(s server.Server, opts Options) ([]byte, error) {
	if opts.SocksPort == 0 {
		return nil, fmt.Errorf("xrayconfig: SocksPort is required")
	}
	if opts.LogLevel == "" {
		opts.LogLevel = "warning"
	}

	proxy, err := buildProxyOutbound(s)
	if err != nil {
		return nil, err
	}
	socksSettings, _ := json.Marshal(socksInboundSettings{Auth: "noauth", UDP: true})

	out := struct {
		Log       LogCfg          `json:"log"`
		Inbounds  []Inbound       `json:"inbounds"`
		Outbounds []Outbound      `json:"outbounds"`
		Routing   Routing         `json:"routing"`
	}{
		Log: LogCfg{Loglevel: opts.LogLevel},
		Inbounds: []Inbound{{
			Tag:      "socks-in",
			Listen:   "127.0.0.1",
			Port:     opts.SocksPort,
			Protocol: "socks",
			Settings: socksSettings,
			Sniffing: &Sniffing{Enabled: true, DestOverride: []string{"http", "tls"}},
		}},
		Outbounds: []Outbound{
			proxy,
			{Tag: "direct", Protocol: "freedom"},
			{Tag: "block", Protocol: "blackhole"},
		},
		Routing: Routing{
			DomainStrategy: "AsIs",
			Rules:          []RoutingRule{},
		},
	}

	return json.MarshalIndent(out, "", "  ")
}

func buildProxyOutbound(s server.Server) (Outbound, error) {
	if s.Address == "" || s.Port == 0 {
		return Outbound{}, fmt.Errorf("xrayconfig: server is missing address/port")
	}

	var settings json.RawMessage
	switch s.Protocol {
	case "vless":
		if s.UUID == "" {
			return Outbound{}, fmt.Errorf("xrayconfig: vless: missing uuid")
		}
		raw, err := json.Marshal(vlessSettings{Vnext: []vlessVnext{{
			Address: s.Address,
			Port:    s.Port,
			Users: []vlessUser{{
				ID:         s.UUID,
				Encryption: defaultStr(s.Encryption, "none"),
				Flow:       s.Flow,
			}},
		}}})
		if err != nil {
			return Outbound{}, err
		}
		settings = raw

	case "vmess":
		if s.UUID == "" {
			return Outbound{}, fmt.Errorf("xrayconfig: vmess: missing uuid")
		}
		raw, err := json.Marshal(vmessSettings{Vnext: []vmessVnext{{
			Address: s.Address,
			Port:    s.Port,
			Users: []vmessUser{{
				ID:       s.UUID,
				AlterID:  s.AlterID,
				Security: defaultStr(s.Encryption, "auto"),
			}},
		}}})
		if err != nil {
			return Outbound{}, err
		}
		settings = raw

	case "trojan":
		if s.Password == "" {
			return Outbound{}, fmt.Errorf("xrayconfig: trojan: missing password")
		}
		raw, err := json.Marshal(trojanSettings{Servers: []trojanServer{{
			Address:  s.Address,
			Port:     s.Port,
			Password: s.Password,
		}}})
		if err != nil {
			return Outbound{}, err
		}
		settings = raw

	default:
		return Outbound{}, fmt.Errorf("xrayconfig: unsupported protocol %q", s.Protocol)
	}

	stream, err := buildStreamSettings(s)
	if err != nil {
		return Outbound{}, err
	}
	return Outbound{
		Tag:            "proxy",
		Protocol:       s.Protocol,
		Settings:       settings,
		StreamSettings: stream,
	}, nil
}

func buildStreamSettings(s server.Server) (*StreamSettings, error) {
	ss := &StreamSettings{
		Network:  defaultStr(s.Network, "tcp"),
		Security: s.Security,
	}
	if ss.Security == "none" {
		ss.Security = ""
	}

	switch ss.Security {
	case "tls":
		ss.TLSSettings = &TLSSettings{
			ServerName:    s.SNI,
			Fingerprint:   s.Fingerprint,
			ALPN:          s.ALPN,
			AllowInsecure: s.AllowInsecure,
		}
	case "reality":
		if s.PublicKey == "" {
			return nil, fmt.Errorf("xrayconfig: reality: missing publicKey (pbk)")
		}
		ss.RealitySettings = &RealitySettings{
			ServerName:  s.SNI,
			Fingerprint: defaultStr(s.Fingerprint, "chrome"),
			PublicKey:   s.PublicKey,
			ShortID:     s.ShortID,
			SpiderX:     s.SpiderX,
		}
	case "", "none":
		// no transport security
	default:
		return nil, fmt.Errorf("xrayconfig: unsupported security %q", ss.Security)
	}

	switch ss.Network {
	case "tcp":
		if s.HeaderType == "http" {
			// Minimal HTTP camouflage header. Real configurations can
			// override this by editing xray.json post-generation.
			hdr := map[string]any{
				"type": "http",
				"request": map[string]any{
					"path":    []string{defaultStr(s.Path, "/")},
					"headers": map[string][]string{"Host": {defaultStr(s.Host, s.SNI)}},
				},
			}
			raw, _ := json.Marshal(hdr)
			ss.TCPSettings = &TCPSettings{Header: raw}
		}
	case "ws":
		ws := &WSSettings{Path: defaultStr(s.Path, "/")}
		if s.Host != "" {
			ws.Headers = map[string]string{"Host": s.Host}
		}
		ss.WSSettings = ws
	case "grpc":
		ss.GRPCSettings = &GRPCSettings{ServiceName: s.ServiceName}
	default:
		return nil, fmt.Errorf("xrayconfig: unsupported network %q", ss.Network)
	}

	return ss, nil
}

func defaultStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
