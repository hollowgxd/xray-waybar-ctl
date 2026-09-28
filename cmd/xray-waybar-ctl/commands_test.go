package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMenuFallsBackToWofi(t *testing.T) {
	dir := t.TempDir()
	launcher := filepath.Join(dir, "wofi")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nread -r line\nprintf '%s\\n' \"$line\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	choice, err := runWalker(context.Background(), "first\nsecond\n", "Pick a server", 0)
	if err != nil || choice != "first" {
		t.Fatalf("choice=%q err=%v", choice, err)
	}
}

func TestMenuReportsMissingLauncher(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := runWalker(context.Background(), "server\n", "Pick a server", -1)
	if err == nil || !strings.Contains(err.Error(), "install walker, wofi or rofi") {
		t.Fatalf("unexpected error: %v", err)
	}
}
