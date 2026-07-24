package subscription

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
)

// Xray JSON subscriptions are arrays of complete client configs. Each
// item usually has one proxy outbound plus freedom/block helpers. We
// flatten the supported proxy outbounds into the same server.Server
// representation used by URI and Mihomo YAML subscriptions.
type xrayJSONConfig struct {
	Remarks   string             `json:"remarks"`
	Remark    string             `json:"remark"`
	Name      string             `json:"name"`
	Outbounds []xrayJSONOutbound `json:"outbounds"`
}

type xrayJSONOutbound struct {
	Tag            string                  `json:"tag"`
	Protocol       string                  `json:"protocol"`
	Settings       json.RawMessage         `json:"settings"`
	StreamSettings *xrayJSONStreamSettings `json:"streamSettings"`
}

type xrayJSONStreamSettings struct {
	Network           string                   `json:"network"`
	Security          string                   `json:"security"`
	TLSSettings       *xrayJSONTLSSettings     `json:"tlsSettings"`
	RealitySettings   *xrayJSONRealitySettings `json:"realitySettings"`
	WSSettings        *xrayJSONWSSettings      `json:"wsSettings"`
	GRPCSettings      *xrayJSONGRPCSettings    `json:"grpcSettings"`
	XHTTPSettings     *xrayJSONXHTTPSettings   `json:"xhttpSettings"`
	SplitHTTPSettings *xrayJSONXHTTPSettings   `json:"splithttpSettings"`
}

type xrayJSONTLSSettings struct {
	ServerName    string   `json:"serverName"`
	Fingerprint   string   `json:"fingerprint"`
	ALPN          []string `json:"alpn"`
	AllowInsecure bool     `json:"allowInsecure"`
}

type xrayJSONRealitySettings struct {
	ServerName  string `json:"serverName"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	ShortID     string `json:"shortId"`
	SpiderX     string `json:"spiderX"`
}

type xrayJSONWSSettings struct {
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

type xrayJSONGRPCSettings struct {
	ServiceName string `json:"serviceName"`
}

type xrayJSONXHTTPSettings struct {
	Path    string            `json:"path"`
	Host    string            `json:"host"`
	Mode    string            `json:"mode"`
	Headers map[string]string `json:"headers"`
}

func parseXrayJSON(body []byte) ([]server.Server, []error) {
	var configs []xrayJSONConfig
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		var one xrayJSONConfig
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, []error{fmt.Errorf("xray-json: %w", err)}
		}
		configs = []xrayJSONConfig{one}
	} else if err := json.Unmarshal(body, &configs); err != nil {
		return nil, []error{fmt.Errorf("xray-json: %w", err)}
	}

	var servers []server.Server
	var errs []error
	for configIndex, cfg := range configs {
		proxyIndex := 0
		for _, outbound := range cfg.Outbounds {
			protocol := strings.ToLower(outbound.Protocol)
			if protocol != "vless" && protocol != "vmess" && protocol != "trojan" {
				continue
			}
			s, err := xrayOutboundToServer(outbound)
			if err != nil {
				errs = append(errs, fmt.Errorf("xray-json config %d outbound %q: %w", configIndex+1, outbound.Tag, err))
				continue
			}
			proxyIndex++
			name := strings.TrimSpace(firstNonEmpty(cfg.Remarks, cfg.Remark, cfg.Name))
			if name == "" {
				name = strings.TrimSpace(outbound.Tag)
			}
			if name == "" {
				name = s.Endpoint()
			}
			if proxyIndex > 1 {
				name = fmt.Sprintf("%s-%d", name, proxyIndex)
			}
			s.Name = name
			servers = append(servers, s)
		}
	}
	return servers, errs
}

func xrayOutboundToServer(out xrayJSONOutbound) (server.Server, error) {
	s := server.Server{Protocol: strings.ToLower(out.Protocol), Network: "tcp", Security: "none"}
	switch s.Protocol {
	case "vless", "vmess":
		var settings struct {
			Vnext []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
				Users   []struct {
					ID         string `json:"id"`
					Encryption string `json:"encryption"`
					Security   string `json:"security"`
					Flow       string `json:"flow"`
					AlterID    int    `json:"alterId"`
				} `json:"users"`
			} `json:"vnext"`
		}
		if err := json.Unmarshal(out.Settings, &settings); err != nil {
			return s, err
		}
		if len(settings.Vnext) == 0 || len(settings.Vnext[0].Users) == 0 {
			return s, fmt.Errorf("missing vnext/users")
		}
		vnext, user := settings.Vnext[0], settings.Vnext[0].Users[0]
		s.Address, s.Port, s.UUID = vnext.Address, vnext.Port, user.ID
		s.Flow, s.AlterID = user.Flow, user.AlterID
		if s.Protocol == "vless" {
			s.Encryption = firstNonEmpty(user.Encryption, "none")
		} else {
			s.Encryption = firstNonEmpty(user.Security, "auto")
		}
	case "trojan":
		var settings struct {
			Servers []struct {
				Address  string `json:"address"`
				Port     int    `json:"port"`
				Password string `json:"password"`
			} `json:"servers"`
		}
		if err := json.Unmarshal(out.Settings, &settings); err != nil {
			return s, err
		}
		if len(settings.Servers) == 0 {
			return s, fmt.Errorf("missing servers")
		}
		s.Address, s.Port, s.Password = settings.Servers[0].Address, settings.Servers[0].Port, settings.Servers[0].Password
	}
	if s.Address == "" || s.Port == 0 {
		return s, fmt.Errorf("missing address/port")
	}

	if st := out.StreamSettings; st != nil {
		s.Network = firstNonEmpty(st.Network, "tcp")
		s.Security = firstNonEmpty(st.Security, "none")
		if st.TLSSettings != nil {
			s.SNI = st.TLSSettings.ServerName
			s.Fingerprint = st.TLSSettings.Fingerprint
			s.ALPN = st.TLSSettings.ALPN
			s.AllowInsecure = st.TLSSettings.AllowInsecure
		}
		if st.RealitySettings != nil {
			s.Security = "reality"
			s.SNI = st.RealitySettings.ServerName
			s.Fingerprint = st.RealitySettings.Fingerprint
			s.PublicKey = st.RealitySettings.PublicKey
			s.ShortID = st.RealitySettings.ShortID
			s.SpiderX = st.RealitySettings.SpiderX
		}
		if st.WSSettings != nil {
			s.Path = st.WSSettings.Path
			s.Host = st.WSSettings.Headers["Host"]
		}
		if st.GRPCSettings != nil {
			s.ServiceName = st.GRPCSettings.ServiceName
		}
		xhttp := st.XHTTPSettings
		if xhttp == nil {
			xhttp = st.SplitHTTPSettings
		}
		if xhttp != nil {
			s.Path = xhttp.Path
			s.Host = xhttp.Host
			s.XHTTPMode = xhttp.Mode
			s.XHTTPHeaders = xhttp.Headers
		}
	}
	return s, nil
}
