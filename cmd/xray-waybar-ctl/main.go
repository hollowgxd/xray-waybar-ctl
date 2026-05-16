// Command xray-waybar-ctl is the CLI surface of the project. It is a
// thin orchestration layer over the internal packages — every command
// is a function in commands.go.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// version is overridden at build time via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprint(os.Stderr, helpText)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

const helpText = `xray-waybar-ctl — control xray-core from waybar

Usage:
  xray-waybar-ctl <command> [args]

Connection:
  connect           Test, pick the best server and launch xray
  disconnect        Stop xray
  toggle            connect if stopped, disconnect if running
  reconnect         disconnect + connect

Servers:
  update            Re-fetch the subscription into the local cache
  list              Show cached servers
  test              URL-test every cached server, print latency table
  ping              TCP-only liveness check (cheap; used by systemd timer)
  watchdog [--loop D]  Detect a dead/stuck xray and auto-reconnect.
                       Without --loop: one tick (manual use).
                       --loop 10s: daemon mode (used by systemd user service).
  use <name>        Connect to a specific server (by fragment name)
  use-next          Switch to the next server in the cached list
  use-prev          Switch to the previous server in the cached list

Waybar:
  status            Emit one line of JSON describing the current state
  menu              Open a walker dmenu picker of cached servers; selection → use

Misc:
  version           Print version
  help              Show this help

Environment:
  XRAY_WAYBAR_CONFIG   Override the config path (default: $XDG_CONFIG_HOME/xray-waybar/app.yaml)
`

func run(ctx context.Context, args []string) error {
	cmd := "help"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "help", "--help", "-h":
		fmt.Print(helpText)
		return nil
	case "version", "--version", "-v":
		fmt.Println("xray-waybar-ctl", version)
		return nil
	case "status":
		return cmdStatus(ctx)
	case "menu":
		return cmdMenu(ctx)
	case "connect":
		return cmdConnect(ctx)
	case "disconnect":
		return cmdDisconnect(ctx)
	case "toggle":
		return cmdToggle(ctx)
	case "reconnect":
		return cmdReconnect(ctx)
	case "update":
		return cmdUpdate(ctx)
	case "list":
		return cmdList(ctx)
	case "test":
		return cmdTest(ctx)
	case "ping":
		return cmdPing(ctx)
	case "watchdog":
		return cmdWatchdog(ctx, args)
	case "use":
		if len(args) < 1 {
			return fmt.Errorf("use: server name required: %w", errUsage)
		}
		return cmdUse(ctx, args[0])
	case "use-next":
		return cmdUseDir(ctx, +1)
	case "use-prev":
		return cmdUseDir(ctx, -1)
	default:
		return fmt.Errorf("unknown command %q: %w", cmd, errUsage)
	}
}
