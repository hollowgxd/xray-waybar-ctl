package subscription

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// FetchTimeout caps a single GET. Callers can also pass a context.
const FetchTimeout = 15 * time.Second

// FetchOptions describes client identity headers used by subscription
// routers such as Remnawave. Mihomo mode deliberately identifies as a
// Clash.Meta client so the panel returns a native MIHOMO document.
type FetchOptions struct {
	HWID      string
	UserAgent string
	DeviceOS  string
}

// Fetch downloads the raw subscription body. The caller is expected to
// pass the body to Parse — fetch and parse are split so a stale cache
// can be served when the network is unreachable.
//
// hwid, when non-empty, is sent as the X-HWID header. Remnawave-style
// panels gate the real server list behind this; without it the panel
// returns a placeholder URI pointing at 0.0.0.0:1 with an instructional
// fragment, which would silently produce a broken cache.
func Fetch(ctx context.Context, rawURL, hwid string) ([]byte, error) {
	return FetchWithOptions(ctx, rawURL, FetchOptions{
		HWID:      hwid,
		UserAgent: "xray-waybar-ctl/1.0",
		DeviceOS:  "linux",
	})
}

// FetchWithOptions downloads a subscription with an explicit client
// identity. It is kept separate from Fetch so existing callers retain
// the original Xray-oriented behaviour.
func FetchWithOptions(ctx context.Context, rawURL string, opts FetchOptions) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("subscription: build request: %w", err)
	}
	// Most subscription endpoints care about the User-Agent and refuse
	// generic Go clients with a 403.
	ua := opts.UserAgent
	if ua == "" {
		ua = "xray-waybar-ctl/1.0"
	}
	req.Header.Set("User-Agent", ua)
	if opts.DeviceOS != "" {
		req.Header.Set("X-Device-OS", opts.DeviceOS)
	}
	if opts.HWID != "" {
		req.Header.Set("X-HWID", opts.HWID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("subscription: HTTP %s", resp.Status)
	}
	const maxBody = 4 << 20 // 4 MiB is more than enough for any subscription
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("subscription: read body: %w", err)
	}
	return body, nil
}
