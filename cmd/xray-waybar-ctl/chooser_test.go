package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunChooserFallsBackToWofi(t *testing.T) {
	dir := t.TempDir()
	wofi := filepath.Join(dir, "wofi")
	if err := os.WriteFile(wofi, []byte("#!/bin/sh\n/usr/bin/cat >/dev/null\nprintf '  picked-server  \\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	choice, err := runChooser(context.Background(), "one\ntwo\n", "Pick", 1)
	if err != nil {
		t.Fatal(err)
	}
	if choice != "picked-server" {
		t.Fatalf("choice = %q", choice)
	}
}

func TestRunChooserReportsMissingLauncher(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := runChooser(context.Background(), "one\n", "Pick", -1)
	if !errors.Is(err, errNoMenuLauncher) {
		t.Fatalf("error = %v, want errNoMenuLauncher", err)
	}
}
