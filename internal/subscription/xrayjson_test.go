package subscription

import "testing"

func TestParseXrayJSONRealitySubscription(t *testing.T) {
	body := []byte(`[
  {
    "remarks": "SE Reality",
    "outbounds": [
      {
        "tag": "proxy",
        "protocol": "vless",
        "settings": {"vnext":[{"address":"edge.example","port":443,"users":[{"id":"00000000-0000-0000-0000-000000000001","encryption":"none","flow":"xtls-rprx-vision"}]}]},
        "streamSettings": {"network":"tcp","security":"reality","realitySettings":{"serverName":"cdn.example","fingerprint":"firefox","publicKey":"pub","shortId":"abcd","spiderX":"/"}}
      },
      {"tag":"direct","protocol":"freedom"}
    ]
  }
]`)
	servers, errs := Parse(body)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(servers) != 1 {
		t.Fatalf("servers=%d", len(servers))
	}
	s := servers[0]
	if s.Name != "SE Reality" || s.Address != "edge.example" || s.Port != 443 {
		t.Fatalf("unexpected server: %+v", s)
	}
	if s.Security != "reality" || s.PublicKey != "pub" || s.Flow != "xtls-rprx-vision" {
		t.Fatalf("unexpected reality fields: %+v", s)
	}
}

func TestParseXrayJSONXHTTPSettings(t *testing.T) {
	body := []byte(`[{"remarks":"XHTTP","outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"edge.example","port":443,"users":[{"id":"00000000-0000-0000-0000-000000000001","encryption":"none"}]}]},"streamSettings":{"network":"xhttp","security":"tls","tlsSettings":{"serverName":"cdn.example","alpn":["h2"]},"xhttpSettings":{"path":"/api","host":"cdn.example","mode":"stream-one","headers":{"X-Test":"yes"}}}}]}]`)
	servers, errs := Parse(body)
	if len(errs) != 0 || len(servers) != 1 {
		t.Fatalf("servers=%d errs=%v", len(servers), errs)
	}
	s := servers[0]
	if s.Network != "xhttp" || s.Path != "/api" || s.Host != "cdn.example" || s.XHTTPMode != "stream-one" || s.XHTTPHeaders["X-Test"] != "yes" {
		t.Fatalf("unexpected xhttp fields: %+v", s)
	}
}
