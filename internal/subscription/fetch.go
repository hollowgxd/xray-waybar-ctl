package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// FetchTimeout caps a single GET. Callers can also pass a context.
const FetchTimeout = 15 * time.Second

// Fetch downloads the raw subscription body. The caller is expected to
// pass the body to Parse — fetch and parse are split so a stale cache
// can be served when the network is unreachable.
//
// hwid, when non-empty, is sent as the X-HWID header. Remnawave-style
// panels gate the real server list behind this; without it the panel
// returns a placeholder URI pointing at 0.0.0.0:1 with an instructional
// fragment, which would silently produce a broken cache.
func Fetch(ctx context.Context, rawURL, hwid string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("subscription: build request: %w", err)
	}
	// Most subscription endpoints care about the User-Agent and refuse
	// generic Go clients with a 403.
	req.Header.Set("User-Agent", "xray-waybar-ctl/1.0")
	if hwid != "" {
		req.Header.Set("X-HWID", hwid)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("subscription: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("subscription: HTTP %s", resp.Status)
	}
	const maxBody = 4 << 20 // 4 MiB is more than enough for any subscription
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("subscription: read body: %w", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("subscription: response exceeds %d bytes", maxBody)
	}
	return body, nil
}
