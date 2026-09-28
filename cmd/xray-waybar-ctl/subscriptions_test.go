package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourgfslove/xray-waybar-ctl/internal/subscription"
)

func setupSubscriptionsTest(t *testing.T, legacy string) string {
	t.Helper()
	dir := t.TempDir()
	config := filepath.Join(dir, "app.yaml")
	raw := fmt.Sprintf("subscription_url: %q\nsubscriptions_file: %q\ncache_file: %q\nstate_file: %q\nhwid_file: %q\npid_file: %q\n", legacy, filepath.Join(dir, "subscriptions.json"), filepath.Join(dir, "cache.json"), filepath.Join(dir, "state.json"), filepath.Join(dir, "hwid"), filepath.Join(dir, "missing.pid"))
	if err := os.WriteFile(config, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_WAYBAR_CONFIG", config)
	return dir
}

func TestSubscriptionsAddSwitchAndLegacy(t *testing.T) {
	feed := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "vless://00000000-0000-0000-0000-000000000001@example.com:443?security=tls#%s\n", name)
		}))
	}
	old := feed("old")
	defer old.Close()
	first := feed("first")
	defer first.Close()
	second := feed("second")
	defer second.Close()
	dir := setupSubscriptionsTest(t, old.URL)
	ctx := context.Background()
	lc, err := loadAll()
	if err != nil {
		t.Fatal(err)
	}
	if lc.subscriptionName != "default" || lc.registry.Items["default"] != old.URL {
		t.Fatalf("legacy feed missing: %+v", lc.registry)
	}
	if err := addSubscription(ctx, "one", first.URL); err != nil {
		t.Fatal(err)
	}
	if err := addSubscription(ctx, "two", second.URL); err != nil {
		t.Fatal(err)
	}
	if err := useSubscription(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	lc, err = loadAll()
	if err != nil {
		t.Fatal(err)
	}
	if lc.subscriptionName != "two" || len(lc.cache.Servers) != 1 || lc.cache.Servers[0].Name != "second" {
		t.Fatalf("wrong active feed: %s %+v", lc.subscriptionName, lc.cache)
	}
	if err := useSubscription(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	lc, err = loadAll()
	if err != nil {
		t.Fatal(err)
	}
	if lc.cache.Servers[0].Name != "first" {
		t.Fatalf("cache crossed feeds: %+v", lc.cache)
	}
	info, err := os.Stat(filepath.Join(dir, "subscriptions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry permissions: %v", info.Mode())
	}
	if err := removeSubscription(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	lc, err = loadAll()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lc.registry.Items["two"]; ok {
		t.Fatal("removed feed resurrected")
	}
	if err := removeSubscription(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	lc, err = loadAll()
	if err != nil {
		t.Fatal(err)
	}
	if lc.subscriptionName != "default" {
		t.Fatalf("expected fallback to default, got %q", lc.subscriptionName)
	}
	if err := removeSubscription(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	lc, err = loadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(lc.registry.Items) != 0 || lc.subscriptionName != "" {
		t.Fatalf("last removal failed: %+v", lc.registry)
	}
}

func TestEmptySubscriptionDoesNotReplaceGoodCache(t *testing.T) {
	body := "vless://00000000-0000-0000-0000-000000000001@example.com:443?security=tls#good\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	setupSubscriptionsTest(t, "")
	ctx := context.Background()
	if err := addSubscription(ctx, "home", srv.URL); err != nil {
		t.Fatal(err)
	}
	body = "[]"
	if err := updateNamedSubscription(ctx, "home"); err != nil {
		t.Fatalf("good cache should survive empty response: %v", err)
	}
	lc, err := loadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(lc.cache.Servers) != 1 || lc.cache.Servers[0].Name != "good" {
		t.Fatalf("cache replaced by empty response: %+v", lc.cache)
	}
}

func TestEmptySubscriptionDiagnosticNoToken(t *testing.T) {
	secret := "private-token-value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") }))
	defer srv.Close()
	setupSubscriptionsTest(t, "")
	err := addSubscription(context.Background(), "empty", srv.URL+"?token="+secret)
	if err == nil || !strings.Contains(err.Error(), "empty JSON array") || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe or missing diagnostic: %v", err)
	}
}

func TestRegistryRejectsTraversalAndPersistsRemoval(t *testing.T) {
	if err := subscription.ValidateName("../oops"); err == nil {
		t.Fatal("path traversal accepted")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.json")
	r := &subscription.Registry{Active: "", Items: map[string]string{}}
	if err := subscription.SaveRegistry(path, r); err != nil {
		t.Fatal(err)
	}
	got, err := subscription.LoadRegistry(path, "https://legacy.test/secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Fatal("legacy unexpectedly resurrected")
	}
}
