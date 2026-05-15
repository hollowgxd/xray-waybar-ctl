package waybar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
)

func TestDisconnectedShape(t *testing.T) {
	raw, err := Marshal(Disconnected())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["class"] != "disconnected" {
		t.Errorf("class: %v", got["class"])
	}
	if !strings.Contains(got["text"].(string), "VPN") {
		t.Errorf("text: %v", got["text"])
	}
}

func TestConnectedTooltipContainsLatencyAndEndpoint(t *testing.T) {
	s := server.Server{
		Name:     "DE-Frankfurt-2",
		Protocol: "vless",
		Address:  "1.2.3.4",
		Port:     8444,
		Network:  "tcp",
		Security: "reality",
	}
	st := Connected(s, ConnectedOptions{Latency: 87 * time.Millisecond, LocalPort: 1080})
	if !strings.Contains(st.Tooltip, "87ms") {
		t.Errorf("tooltip missing latency: %q", st.Tooltip)
	}
	if !strings.Contains(st.Tooltip, "1.2.3.4:8444") {
		t.Errorf("tooltip missing endpoint: %q", st.Tooltip)
	}
	if st.Class != "connected" {
		t.Errorf("class: %s", st.Class)
	}
	if st.Percentage <= 0 || st.Percentage > 100 {
		t.Errorf("percentage out of range: %d", st.Percentage)
	}
}

func TestShortNameTruncatesLongLabels(t *testing.T) {
	s := server.Server{Name: "Server_DE_Frankfurt_2", Address: "x", Port: 1}
	out := shortName(s)
	if out != "Frankfurt-2" {
		t.Errorf("short: %q", out)
	}
}
