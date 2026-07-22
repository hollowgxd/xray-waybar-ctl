package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client is a deliberately small wrapper around Mihomo's localhost API.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

func NewClient(controller, secret string, timeout time.Duration) *Client {
	if !strings.Contains(controller, "://") {
		controller = "http://" + controller
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Client{baseURL: strings.TrimRight(controller, "/"), secret: secret, http: &http.Client{Timeout: timeout}}
}

type DelayHistory struct {
	Time  string `json:"time"`
	Delay int    `json:"delay"`
}

type Proxy struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Now     string         `json:"now"`
	All     []string       `json:"all"`
	Alive   bool           `json:"alive"`
	History []DelayHistory `json:"history"`
}

type RunningConfig struct {
	MixedPort int `json:"mixed-port"`
	Tun       struct {
		Enable bool `json:"enable"`
	} `json:"tun"`
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.do(ctx, http.MethodGet, "/version", nil, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

func (c *Client) Config(ctx context.Context) (RunningConfig, error) {
	var out RunningConfig
	err := c.do(ctx, http.MethodGet, "/configs", nil, &out)
	return out, err
}

func (c *Client) Proxies(ctx context.Context) (map[string]Proxy, error) {
	var out struct {
		Proxies map[string]Proxy `json:"proxies"`
	}
	if err := c.do(ctx, http.MethodGet, "/proxies", nil, &out); err != nil {
		return nil, err
	}
	return out.Proxies, nil
}

// PrimaryGroup returns the configured selector, or the first useful
// Selector when no explicit name was configured.
func (c *Client) PrimaryGroup(ctx context.Context, preferred string) (Proxy, map[string]Proxy, error) {
	proxies, err := c.Proxies(ctx)
	if err != nil {
		return Proxy{}, nil, err
	}
	if preferred != "" {
		p, ok := proxies[preferred]
		if !ok || !IsGroup(p) {
			return Proxy{}, proxies, fmt.Errorf("mihomo: group %q not found", preferred)
		}
		return p, proxies, nil
	}
	var candidates []Proxy
	for name, p := range proxies {
		if name != "GLOBAL" && strings.EqualFold(p.Type, "Selector") && len(p.All) > 0 {
			candidates = append(candidates, p)
		}
	}
	// Maps returned by encoding/json have no useful order. Prefer a
	// top-level selector that contains another policy group, then make
	// ties deterministic. Users can always pin mihomo_group explicitly.
	sort.Slice(candidates, func(i, j int) bool {
		si, sj := groupScore(candidates[i], proxies), groupScore(candidates[j], proxies)
		if si != sj {
			return si > sj
		}
		return candidates[i].Name < candidates[j].Name
	})
	if len(candidates) > 0 {
		return candidates[0], proxies, nil
	}
	// Some valid profiles route directly through a URLTest/Fallback group
	// and contain no manual selector. They remain usable for status,
	// testing and automatic operation (manual `use` may be rejected by
	// Mihomo for a non-selector group).
	for name, p := range proxies {
		if name != "GLOBAL" && IsGroup(p) && len(p.All) > 0 {
			candidates = append(candidates, p)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		si, sj := groupScore(candidates[i], proxies), groupScore(candidates[j], proxies)
		if si != sj {
			return si > sj
		}
		return candidates[i].Name < candidates[j].Name
	})
	if len(candidates) > 0 {
		return candidates[0], proxies, nil
	}
	return Proxy{}, proxies, fmt.Errorf("mihomo: no proxy group found")
}

func (c *Client) Select(ctx context.Context, group, name string) error {
	body := map[string]string{"name": name}
	return c.do(ctx, http.MethodPut, "/proxies/"+url.PathEscape(group), body, nil)
}

func (c *Client) Delay(ctx context.Context, name, testURL string, timeout time.Duration) (int, error) {
	q := url.Values{}
	q.Set("url", testURL)
	q.Set("timeout", fmt.Sprintf("%d", timeout.Milliseconds()))
	q.Set("expected", "200/204")
	var out struct {
		Delay int `json:"delay"`
	}
	path := "/proxies/" + url.PathEscape(name) + "/delay?" + q.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return 0, err
	}
	return out.Delay, nil
}

func (c *Client) GroupDelay(ctx context.Context, group, testURL string, timeout time.Duration) (map[string]int, error) {
	q := url.Values{}
	q.Set("url", testURL)
	q.Set("timeout", fmt.Sprintf("%d", timeout.Milliseconds()))
	q.Set("expected", "200/204")
	out := map[string]int{}
	path := "/group/" + url.PathEscape(group) + "/delay?" + q.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveCurrent follows nested policy groups until it reaches the
// concrete proxy currently carrying traffic.
func ResolveCurrent(name string, proxies map[string]Proxy) string {
	seen := map[string]bool{}
	for i := 0; i < 16 && name != ""; i++ {
		if seen[name] {
			break
		}
		seen[name] = true
		p, ok := proxies[name]
		if !ok || !IsGroup(p) || p.Now == "" || p.Now == name {
			break
		}
		name = p.Now
	}
	return name
}

// IsGroup reports whether an API proxy entry is a policy group rather
// than a concrete outbound.
func IsGroup(p Proxy) bool {
	switch strings.ToLower(p.Type) {
	case "selector", "urltest", "fallback", "loadbalance", "smart":
		return true
	default:
		return len(p.All) > 0
	}
}

func groupScore(p Proxy, proxies map[string]Proxy) int {
	score := len(p.All)
	for _, name := range p.All {
		if child, ok := proxies[name]; ok && IsGroup(child) {
			score += 1000
		}
	}
	return score
}

func (c *Client) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mihomo API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("mihomo API: HTTP %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
		return fmt.Errorf("mihomo API: decode: %w", err)
	}
	return nil
}
