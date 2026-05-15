// Package waybar formats the CLI's runtime view as the JSON shape
// Waybar expects from a custom module (return-type=json).
package waybar

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
)

// Icons are nerd-font glyphs. Kept here so themes can be tweaked in
// one place.
const (
	IconConnected    = "󰖂"
	IconDisconnected = "󰖄"
	IconLoading      = "󰔟"
	IconError        = "󰈂"
)

// Status mirrors waybar's "json" return type for custom modules.
// `omitempty` keeps the output compact so it diffs cleanly.
type Status struct {
	Text       string `json:"text"`
	Tooltip    string `json:"tooltip,omitempty"`
	Class      string `json:"class,omitempty"`
	Alt        string `json:"alt,omitempty"`
	Percentage int    `json:"percentage,omitempty"`
}

// Marshal renders status as the single-line JSON waybar expects.
func Marshal(s Status) ([]byte, error) { return json.Marshal(s) }

// Disconnected is the resting state shown when xray is not running.
func Disconnected() Status {
	return Status{
		Text:    fmt.Sprintf("%s VPN", IconDisconnected),
		Tooltip: "Disconnected",
		Class:   "disconnected",
	}
}

// Loading is shown while a connect/test is in progress.
func Loading() Status {
	return Status{
		Text:    fmt.Sprintf("%s …", IconLoading),
		Tooltip: "Connecting…",
		Class:   "loading",
	}
}

// Error is shown when the controller knows xray is in a broken state.
func Error(msg string) Status {
	tooltip := msg
	if tooltip == "" {
		tooltip = "Error"
	}
	return Status{
		Text:    fmt.Sprintf("%s VPN", IconError),
		Tooltip: tooltip,
		Class:   "error",
	}
}

// ConnectedOptions carries the tooltip-only data Connected needs to
// render. All fields are optional — empty values are simply omitted.
type ConnectedOptions struct {
	Latency      time.Duration
	UploadBytes  int64
	DownBytes    int64
	LocalPort    int
	ConnectedAt  time.Time
}

// Connected renders the running state. The text is a short tag —
// usually the server fragment — and the tooltip carries the detail.
func Connected(s server.Server, opt ConnectedOptions) Status {
	var b strings.Builder
	fmt.Fprintf(&b, "Server: %s\n", s.DisplayName())
	fmt.Fprintf(&b, "Endpoint: %s\n", s.Endpoint())
	fmt.Fprintf(&b, "Protocol: %s", s.Protocol)
	if s.Security != "" && s.Security != "none" {
		fmt.Fprintf(&b, " / %s", s.Security)
	}
	if s.Network != "" {
		fmt.Fprintf(&b, " / %s", s.Network)
	}
	if opt.Latency > 0 {
		fmt.Fprintf(&b, "\nLatency: %dms", opt.Latency.Milliseconds())
	}
	if opt.UploadBytes > 0 || opt.DownBytes > 0 {
		fmt.Fprintf(&b, "\nUp: %s  Down: %s", humanBytes(opt.UploadBytes), humanBytes(opt.DownBytes))
	}
	if opt.LocalPort > 0 {
		fmt.Fprintf(&b, "\nSOCKS5: 127.0.0.1:%d", opt.LocalPort)
	}
	if !opt.ConnectedAt.IsZero() {
		fmt.Fprintf(&b, "\nUptime: %s", roundDuration(time.Since(opt.ConnectedAt)))
	}

	status := Status{
		Text:    fmt.Sprintf("%s %s", IconConnected, shortName(s)),
		Tooltip: b.String(),
		Class:   "connected",
	}
	if opt.Latency > 0 {
		// Encode latency as a 0..100 "quality" percentage so themes
		// can hook on it. 50ms -> 95, 500ms -> ~50.
		score := 100 - int(opt.Latency.Milliseconds()/10)
		if score < 0 {
			score = 0
		}
		if score > 100 {
			score = 100
		}
		status.Percentage = score
	}
	return status
}

// shortName picks the most compact human label for the text field.
// Full server names like "Server_DE_Frankfurt_2" become "DE-2" so the
// waybar pill stays narrow.
func shortName(s server.Server) string {
	name := s.DisplayName()
	if name == s.Endpoint() {
		return name
	}
	// Heuristic: keep last two underscore/dash segments.
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == ' ' })
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "-")
	}
	return name
}

func humanBytes(n int64) string {
	const unit = 1024.0
	if n < int64(unit) {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	suffix := []string{"KB", "MB", "GB", "TB"}
	i := 0
	for v >= unit && i < len(suffix)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", v, suffix[i])
}

func roundDuration(d time.Duration) time.Duration {
	switch {
	case d < time.Minute:
		return d.Round(time.Second)
	case d < time.Hour:
		return d.Round(time.Second)
	default:
		return d.Round(time.Minute)
	}
}
