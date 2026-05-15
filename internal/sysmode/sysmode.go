// Package sysmode wraps systemctl to drive the system-wide TUN service
// (xray-waybar-tun.service). The unit, polkit rule and hev-socks5-tunnel
// config that this package relies on live in configs/ and are installed
// out-of-band via `make install-system`.
//
// Errors are categorized so the CLI can distinguish "not installed"
// (user-fixable: run install-system) from "operation failed".
package sysmode

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// UnitName is the systemd unit managed by this package.
const UnitName = "xray-waybar-tun.service"

// ErrNotInstalled means the systemd unit is absent. Returned by Start
// and Status when systemctl can't find the unit at all.
var ErrNotInstalled = errors.New("xray-waybar-tun.service not installed (run `sudo make install-system`)")

// State is the parsed `systemctl is-active` output.
type State int

const (
	StateUnknown State = iota
	StateInactive
	StateActivating
	StateActive
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateInactive:
		return "inactive"
	case StateActivating:
		return "activating"
	case StateActive:
		return "active"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Status returns the current state of the unit. A missing unit is
// reported as ErrNotInstalled, not as inactive — those are very
// different conditions for the user.
func Status(ctx context.Context) (State, error) {
	out, err := run(ctx, "systemctl", "is-active", UnitName)
	trimmed := strings.TrimSpace(out)
	switch trimmed {
	case "active":
		return StateActive, nil
	case "activating":
		return StateActivating, nil
	case "inactive":
		return StateInactive, nil
	case "failed":
		return StateFailed, nil
	}
	// is-active exits non-zero for inactive/failed/unknown — that is
	// expected. We only treat "unit not found" as a real error.
	if err != nil {
		check, _ := run(ctx, "systemctl", "list-unit-files", UnitName)
		if !strings.Contains(check, UnitName) {
			return StateUnknown, ErrNotInstalled
		}
	}
	return StateUnknown, nil
}

// Start brings the TUN service up. Returns ErrNotInstalled when the
// unit file is missing.
func Start(ctx context.Context) error {
	if _, err := Status(ctx); errors.Is(err, ErrNotInstalled) {
		return err
	}
	out, err := run(ctx, "systemctl", "start", UnitName)
	if err != nil {
		return fmt.Errorf("sysmode: start %s: %w (%s)", UnitName, err, strings.TrimSpace(out))
	}
	return nil
}

// Stop brings the TUN service down. A missing unit is silently OK —
// nothing to stop.
func Stop(ctx context.Context) error {
	if _, err := Status(ctx); errors.Is(err, ErrNotInstalled) {
		return nil
	}
	out, err := run(ctx, "systemctl", "stop", UnitName)
	if err != nil {
		return fmt.Errorf("sysmode: stop %s: %w (%s)", UnitName, err, strings.TrimSpace(out))
	}
	return nil
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
