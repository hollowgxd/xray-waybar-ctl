package subscription

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateHWID returns a stable device identifier persisted at path.
// On first run it generates 16 random bytes (hex-encoded — 32 chars,
// the format Remnawave-style panels accept) and writes them atomically.
// Subsequent calls read the same value back. Mode 0600 because the HWID
// authenticates this client to the subscription panel.
func LoadOrCreateHWID(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return s, nil
		}
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("hwid: rand: %w", err)
	}
	hwid := hex.EncodeToString(b[:])

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("hwid: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hwid.*")
	if err != nil {
		return "", fmt.Errorf("hwid: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(hwid + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("hwid: write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("hwid: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("hwid: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("hwid: rename: %w", err)
	}
	return hwid, nil
}
