package mihomo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientSelectAndResolveNestedGroup(t *testing.T) {
	selected := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"proxies":{"VPN":{"name":"VPN","type":"Selector","now":"AUTO","all":["AUTO","SE"]},"AUTO":{"name":"AUTO","type":"URLTest","now":"SE","all":["SE"]},"SE":{"name":"SE","type":"VLESS","alive":true}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/VPN":
			selected = "yes"
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", time.Second)
	group, proxies, err := c.PrimaryGroup(context.Background(), "VPN")
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveCurrent(group.Now, proxies); got != "SE" {
		t.Fatalf("resolved=%q", got)
	}
	if err := c.Select(context.Background(), "VPN", "SE"); err != nil {
		t.Fatal(err)
	}
	if selected != "yes" {
		t.Fatal("selection endpoint not called")
	}
}

func TestPrimaryGroupPrefersTopLevelSelector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"proxies":{"Region":{"name":"Region","type":"Selector","now":"SE","all":["SE","DE"]},"VPN":{"name":"VPN","type":"Selector","now":"AUTO","all":["AUTO","DIRECT"]},"AUTO":{"name":"AUTO","type":"URLTest","now":"SE","all":["SE","DE"]},"SE":{"name":"SE","type":"VLESS"},"DE":{"name":"DE","type":"VLESS"}}}`))
	}))
	defer srv.Close()

	group, _, err := NewClient(srv.URL, "", time.Second).PrimaryGroup(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if group.Name != "VPN" {
		t.Fatalf("group=%q", group.Name)
	}
}

func TestPrimaryGroupAcceptsAutomaticOnlyProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"proxies":{"AUTO":{"name":"AUTO","type":"URLTest","now":"SE","all":["SE"]},"SE":{"name":"SE","type":"VLESS"}}}`))
	}))
	defer srv.Close()
	group, _, err := NewClient(srv.URL, "", time.Second).PrimaryGroup(context.Background(), "")
	if err != nil || group.Name != "AUTO" {
		t.Fatalf("group=%+v err=%v", group, err)
	}
}
