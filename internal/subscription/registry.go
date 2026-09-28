package subscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Registry contains named feeds. Once written, it is authoritative; this
// lets users remove a legacy app.yaml subscription without it reappearing.
type Registry struct {
	Active string            `json:"active"`
	Items  map[string]string `json:"items"`
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,49}$`)

func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return errors.New("subscription name must be 1–50 letters, digits, dots, underscores or hyphens")
	}
	return nil
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.ContainsAny(raw, "\r\n") {
		return errors.New("subscription URL must be an http(s) URL with a host and no embedded credentials")
	}
	return nil
}

func LoadRegistry(path, legacyURL string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		r := &Registry{Items: map[string]string{}}
		if legacyURL != "" {
			r.Items["default"] = legacyURL
			r.Active = "default"
		}
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read subscriptions: %w", err)
	}
	r := &Registry{}
	if err := json.Unmarshal(raw, r); err != nil {
		return nil, fmt.Errorf("parse subscriptions: %w", err)
	}
	if r.Items == nil {
		r.Items = map[string]string{}
	}
	for name, url := range r.Items {
		if err := ValidateName(name); err != nil {
			return nil, fmt.Errorf("invalid stored subscription name: %w", err)
		}
		if err := ValidateURL(url); err != nil {
			return nil, fmt.Errorf("invalid stored subscription %q: %w", name, err)
		}
	}
	if r.Active != "" && r.Items[r.Active] == "" {
		return nil, errors.New("active subscription is missing from registry")
	}
	return r, nil
}

func SaveRegistry(path string, r *Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".subscriptions-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.Items))
	for name := range r.Items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
