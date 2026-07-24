package subscription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchWithOptionsSendsMihomoIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "Clash-Meta/xray-waybar-ctl" || r.Header.Get("X-HWID") != "device" || r.Header.Get("X-Device-OS") != "linux" {
			t.Errorf("headers: UA=%q HWID=%q OS=%q", r.UserAgent(), r.Header.Get("X-HWID"), r.Header.Get("X-Device-OS"))
		}
		_, _ = w.Write([]byte("proxies: []\n"))
	}))
	defer srv.Close()

	body, err := FetchWithOptions(context.Background(), srv.URL, FetchOptions{
		HWID: "device", UserAgent: "Clash-Meta/xray-waybar-ctl", DeviceOS: "linux",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "proxies: []\n" {
		t.Fatalf("body=%q", body)
	}
}
