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

// Fetch downloads the raw subscription body. The caller is expected to
// pass the body to Parse — fetch and parse are split so a stale cache
// can be served when the network is unreachable.
func Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("subscription: build request: %w", err)
	}
	// Most subscription endpoints care about the User-Agent and refuse
	// generic Go clients with a 403.
	req.Header.Set("User-Agent", "xray-waybar-ctl/1.0")

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
