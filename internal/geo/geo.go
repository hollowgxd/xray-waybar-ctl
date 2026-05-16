// Package geo downloads xray asset files (geoip.dat / geosite.dat)
// into the configured directory and detects whether the on-disk copy
// changed. xray reads these files only on startup, so a "changed"
// return value is the signal the caller uses to decide whether to
// reconnect.
package geo

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// minDatSize is a sanity floor below which a downloaded .dat payload
// is rejected. Real geoip.dat / geosite.dat are tens of KB to a few MB;
// a 200-byte response means the CDN returned an HTML error page.
const minDatSize = 1024

// minRulesSize is the floor for the HAPP rules JSON. The smallest real
// rules file (JSONSUB.JSON) is just under 1KB.
const minRulesSize = 200

// RulesFileName is the on-disk basename for the cached routing rules.
// Fixed name so xrayconfig.Load can find it without configuration.
const RulesFileName = "rules.json"

// Sources is the input to Update. Each URL is optional; an empty URL
// skips the corresponding file.
//
// RulesURL points to a HAPP-style routing JSON (DirectSites/ProxySites/...).
// When set, Update saves it as <Dir>/rules.json — xrayconfig reads
// from that fixed name.
type Sources struct {
	Dir        string
	GeoipURL   string
	GeositeURL string
	RulesURL   string
}

// Result describes the outcome for one file.
type Result struct {
	Name    string // basename, e.g. "geoip.dat"
	Path    string
	Changed bool
	Bytes   int64
}

// Update downloads each configured asset into Dir. ETag is honoured
// when the server provides one; otherwise content hashes are compared
// to avoid bumping mtime when the file is byte-identical. Returns one
// Result per attempted file. Any per-file error is returned alongside
// the partial results so the caller can log it and still see what
// succeeded.
func Update(ctx context.Context, src Sources) ([]Result, error) {
	if src.Dir == "" {
		return nil, fmt.Errorf("geo: Dir is required")
	}
	if err := os.MkdirAll(src.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("geo: mkdir %s: %w", src.Dir, err)
	}
	items := []struct {
		name, url string
		minBytes  int64
	}{
		{"geoip.dat", src.GeoipURL, minDatSize},
		{"geosite.dat", src.GeositeURL, minDatSize},
		{RulesFileName, src.RulesURL, minRulesSize},
	}
	var results []Result
	var firstErr error
	for _, it := range items {
		if it.url == "" {
			continue
		}
		r, err := fetchOne(ctx, it.url, filepath.Join(src.Dir, it.name), it.minBytes)
		r.Name = it.name
		results = append(results, r)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("geo: %s: %w", it.name, err)
		}
	}
	return results, firstErr
}

// RemoveRules deletes the on-disk rules.json so a follow-up launch
// falls back to "no rules" (proxy-all). Used when switching to a
// built-in profile that does not need a rules file.
func RemoveRules(dir string) error {
	if dir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(dir, RulesFileName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Drop the etag too so the next download is unconditional.
	_ = os.Remove(filepath.Join(dir, RulesFileName+".etag"))
	return nil
}

func fetchOne(ctx context.Context, url, dst string, minBytes int64) (Result, error) {
	res := Result{Path: dst}
	etagPath := dst + ".etag"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return res, err
	}
	if prev, err := os.ReadFile(etagPath); err == nil && len(prev) > 0 {
		req.Header.Set("If-None-Match", string(prev))
	}
	req.Header.Set("User-Agent", "xray-waybar-ctl/geo")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		// ETag-cache hit: existing on-disk file is current.
		if info, err := os.Stat(dst); err == nil {
			res.Bytes = info.Size()
		}
		return res, nil
	}
	if resp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("HTTP %s", resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return res, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return res, err
	}
	if n < minBytes {
		return res, fmt.Errorf("payload too small (%d bytes); refusing", n)
	}
	res.Bytes = n
	newHash := hasher.Sum(nil)

	// If the existing file is identical, skip the rename so mtime
	// stays put — that lets callers cheaply tell "actually changed"
	// from "downloaded again, same bytes".
	changed := true
	if existing, err := os.ReadFile(dst); err == nil {
		oldHash := sha256.Sum256(existing)
		if string(oldHash[:]) == string(newHash) {
			changed = false
		}
	}
	if changed {
		if err := os.Rename(tmpName, dst); err != nil {
			return res, fmt.Errorf("rename: %w", err)
		}
	}
	res.Changed = changed
	if etag := resp.Header.Get("ETag"); etag != "" {
		_ = os.WriteFile(etagPath, []byte(etag), 0o644)
	}
	return res, nil
}

// AssetsPresent reports whether geoip.dat exists in Dir. We use it
// before honouring a geo-aware routing profile so missing assets
// surface as a clear CLI message instead of a cryptic xray startup
// failure.
func AssetsPresent(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "geoip.dat"))
	if err != nil {
		return false
	}
	return info.Size() >= minDatSize
}

// RulesPresent reports whether rules.json exists in Dir.
func RulesPresent(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, RulesFileName))
	if err != nil {
		return false
	}
	return info.Size() >= minRulesSize
}

// RulesPath returns the canonical on-disk path for rules.json. Empty
// dir → empty path.
func RulesPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, RulesFileName)
}
