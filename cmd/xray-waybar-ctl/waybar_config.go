package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yourgfslove/xray-waybar-ctl/internal/waybarconfig"
)

func cmdWaybarInstall(args []string) error {
	position := "right"
	configPath := ""
	for len(args) > 0 {
		switch args[0] {
		case "--position":
			if len(args) < 2 {
				return fmt.Errorf("waybar-install: --position needs left, center or right: %w", errUsage)
			}
			position = args[1]
			args = args[2:]
		case "--config":
			if len(args) < 2 {
				return fmt.Errorf("waybar-install: --config needs a path: %w", errUsage)
			}
			configPath = args[1]
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") || configPath != "" {
				return fmt.Errorf("waybar-install: unexpected argument %q: %w", args[0], errUsage)
			}
			configPath = args[0]
			args = args[1:]
		}
	}

	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("waybar-install: executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	if binary, err = filepath.Abs(binary); err != nil {
		return fmt.Errorf("waybar-install: absolute executable path: %w", err)
	}

	result, err := waybarconfig.Install(configPath, binary, position)
	if err != nil {
		return err
	}
	if !result.Changed {
		fmt.Printf("Waybar already has custom/vpn: %s\n", result.ConfigPath)
		return nil
	}
	fmt.Printf("Waybar config updated: %s\n", result.ConfigPath)
	fmt.Printf("Backup: %s\n", result.BackupPath)
	return nil
}
