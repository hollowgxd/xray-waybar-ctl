package subscription

import (
	"strings"
	"testing"
)

const clashSample = `mixed-port: 7890
proxies:
  - name: "DE-Reality"
    type: vless
    server: 1.2.3.4
    port: 443
    uuid: 00000000-0000-0000-0000-000000000001
    network: tcp
    tls: true
    servername: m.vk.ru
    flow: xtls-rprx-vision
    client-fingerprint: chrome
    reality-opts:
      public-key: PUBKEY
      short-id: deadbeef
  - name: "NL-WS-Trojan"
    type: trojan
    server: 5.6.7.8
    port: 8443
    password: hunter2
    sni: example.com
    network: ws
    ws-opts:
      path: /vpn
      headers:
        Host: cdn.example.com
  - name: "VMess-gRPC"
    type: vmess
    server: 9.10.11.12
    port: 443
    uuid: 00000000-0000-0000-0000-000000000002
    alterId: 0
    cipher: auto
    tls: true
    servername: grpc.example.com
    network: grpc
    grpc-opts:
      grpc-service-name: vmess-grpc
proxy-groups:
  - name: auto
    type: select
    proxies: []
`

func TestParse_ClashYAML(t *testing.T) {
	servers, errs := Parse([]byte(clashSample))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(servers) != 3 {
		t.Fatalf("want 3 servers, got %d", len(servers))
	}

	v := servers[0]
	if v.Protocol != "vless" || v.Address != "1.2.3.4" || v.Port != 443 {
		t.Errorf("vless basics wrong: %+v", v)
	}
	if v.Security != "reality" || v.PublicKey != "PUBKEY" || v.ShortID != "deadbeef" {
		t.Errorf("reality fields wrong: %+v", v)
	}
	if v.Flow != "xtls-rprx-vision" || v.SNI != "m.vk.ru" || v.Fingerprint != "chrome" {
		t.Errorf("reality meta wrong: %+v", v)
	}

	tr := servers[1]
	if tr.Protocol != "trojan" || tr.Password != "hunter2" {
		t.Errorf("trojan basics wrong: %+v", tr)
	}
	if tr.Security != "tls" {
		t.Errorf("trojan must imply tls: %+v", tr)
	}
	if tr.Network != "ws" || tr.Path != "/vpn" || tr.Host != "cdn.example.com" {
		t.Errorf("trojan ws transport wrong: %+v", tr)
	}

	vm := servers[2]
	if vm.Protocol != "vmess" || vm.UUID == "" || vm.Encryption != "auto" {
		t.Errorf("vmess basics wrong: %+v", vm)
	}
	if vm.Network != "grpc" || vm.ServiceName != "vmess-grpc" {
		t.Errorf("vmess grpc wrong: %+v", vm)
	}
}

func TestParse_ClashEmptyProxies(t *testing.T) {
	servers, errs := Parse([]byte("mixed-port: 7890\nproxies: []\n"))
	if len(servers) != 0 {
		t.Errorf("want 0 servers, got %d", len(servers))
	}
	if len(errs) != 0 {
		t.Errorf("empty proxies should be silent, got: %v", errs)
	}
}

func TestParse_ClashUnsupportedTypeSkipped(t *testing.T) {
	doc := `proxies:
  - name: ss-only
    type: ss
    server: 1.1.1.1
    port: 8388
    cipher: aes-256-gcm
    password: x
  - name: good
    type: vless
    server: 2.2.2.2
    port: 443
    uuid: 00000000-0000-0000-0000-000000000003
    network: tcp
    tls: true
`
	servers, errs := Parse([]byte(doc))
	if len(servers) != 1 || servers[0].Name != "good" {
		t.Fatalf("want only the vless entry, got %+v", servers)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "unsupported type") {
		t.Errorf("want one unsupported-type error, got %v", errs)
	}
}

func TestParse_BaseB64StillWorks(t *testing.T) {
	// Sanity: make sure the Clash detector doesn't false-positive on
	// the existing base64+URI path.
	body := "vless://b83d044e-b9e7-48bc-856c-578868f81b0c@1.2.3.4:443?security=tls&type=tcp#node\n"
	servers, errs := Parse([]byte(body))
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(servers) != 1 {
		t.Fatalf("want 1 server, got %d", len(servers))
	}
}
