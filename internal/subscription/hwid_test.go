package subscription

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateHWID_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "hwid")

	first, err := LoadOrCreateHWID(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(first) != 32 {
		t.Errorf("want 32 hex chars, got %d (%q)", len(first), first)
	}
	for _, r := range first {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char %q in %q", r, first)
		}
	}

	second, err := LoadOrCreateHWID(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if second != first {
		t.Errorf("hwid changed across calls: %q vs %q", first, second)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("hwid file should be 0600, got %o", mode)
	}
}
