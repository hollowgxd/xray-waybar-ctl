package tester

import (
	"testing"
	"time"

	"github.com/yourgfslove/xray-waybar-ctl/internal/server"
)

func TestSortByLatency_DeadGoesLast(t *testing.T) {
	rs := []Result{
		{Server: server.Server{Name: "slow"}, Latency: 200 * time.Millisecond, Alive: true},
		{Server: server.Server{Name: "dead"}, Alive: false},
		{Server: server.Server{Name: "fast"}, Latency: 50 * time.Millisecond, Alive: true},
	}
	SortByLatency(rs)
	if rs[0].Server.Name != "fast" {
		t.Errorf("[0]: %s", rs[0].Server.Name)
	}
	if rs[1].Server.Name != "slow" {
		t.Errorf("[1]: %s", rs[1].Server.Name)
	}
	if rs[2].Server.Name != "dead" {
		t.Errorf("[2]: %s", rs[2].Server.Name)
	}
}

func TestPickByPriority(t *testing.T) {
	rs := []Result{
		{Server: server.Server{Name: "a"}, Alive: true},
		{Server: server.Server{Name: "b"}, Alive: false},
		{Server: server.Server{Name: "c"}, Alive: true},
	}
	if got := PickByPriority(rs, []string{"missing", "b", "c"}); got != 2 {
		t.Errorf("priority skipped dead: got %d", got)
	}
	if got := PickByPriority(rs, []string{"missing"}); got != -1 {
		t.Errorf("no match should be -1, got %d", got)
	}
	if got := PickByPriority(rs, nil); got != -1 {
		t.Errorf("empty priority should be -1, got %d", got)
	}
}
