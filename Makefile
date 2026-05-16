BIN      := xray-waybar-ctl
PREFIX   ?= $(HOME)/.local
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)

.PHONY: build install uninstall install-system uninstall-system install-monitor uninstall-monitor test vet tidy clean run-status

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/xray-waybar-ctl

install: build
	install -Dm755 $(BIN) $(PREFIX)/bin/$(BIN)
	install -Dm644 configs/app.yaml.example $(PREFIX)/share/xray-waybar/app.yaml.example

uninstall:
	rm -f $(PREFIX)/bin/$(BIN)
	rm -f $(PREFIX)/share/xray-waybar/app.yaml.example

# Installs the TUN sidecar (system-wide mode). Requires root because it
# drops a systemd unit, a polkit rule and a config in /etc.
# Run after `pacman -S hev-socks5-tunnel`.
install-system:
	@if [ "$$(id -u)" != "0" ]; then \
	  echo "install-system must run as root: sudo make install-system"; exit 1; \
	fi
	install -Dm644 configs/systemd/xray-waybar-tun.service /etc/systemd/system/xray-waybar-tun.service
	install -Dm644 configs/polkit/50-xray-waybar.rules /etc/polkit-1/rules.d/50-xray-waybar.rules
	systemctl daemon-reload
	@echo
	@echo "✓ systemd unit and polkit rule installed."
	@echo "  Make sure tun2socks is on PATH: pacman -Qi tun2socks"
	@echo "  Add `system_wide: true` to ~/.config/xray-waybar/app.yaml,"
	@echo "  ensure your user is in the `wheel` group, then:"
	@echo "    xray-waybar-ctl connect"

# Installs the user-session monitoring:
#   - xray-waybar-ping.timer       (every 30s) — keeps state.Results fresh for menu/tooltip
#   - xray-waybar-watchdog.service (long-lived) — internal 10s loop, auto-reconnects when xray dies behind TUN
# Requires that `make install` has put the binary at $(PREFIX)/bin first.
install-monitor:
	install -Dm644 configs/systemd-user/xray-waybar-ping.service $(HOME)/.config/systemd/user/xray-waybar-ping.service
	install -Dm644 configs/systemd-user/xray-waybar-ping.timer $(HOME)/.config/systemd/user/xray-waybar-ping.timer
	install -Dm644 configs/systemd-user/xray-waybar-watchdog.service $(HOME)/.config/systemd/user/xray-waybar-watchdog.service
	systemctl --user daemon-reload
	systemctl --user enable --now xray-waybar-ping.timer
	systemctl --user enable --now xray-waybar-watchdog.service
	@echo
	@echo "✓ ping timer + watchdog daemon installed and enabled."
	@echo "  Inspect:"
	@echo "    systemctl --user status xray-waybar-watchdog.service"
	@echo "    systemctl --user list-timers xray-waybar-ping.timer"
	@echo "    journalctl --user -u xray-waybar-watchdog.service -f"

uninstall-monitor:
	-systemctl --user disable --now xray-waybar-ping.timer
	-systemctl --user disable --now xray-waybar-watchdog.service
	rm -f $(HOME)/.config/systemd/user/xray-waybar-ping.timer
	rm -f $(HOME)/.config/systemd/user/xray-waybar-ping.service
	rm -f $(HOME)/.config/systemd/user/xray-waybar-watchdog.service
	systemctl --user daemon-reload

uninstall-system:
	@if [ "$$(id -u)" != "0" ]; then \
	  echo "uninstall-system must run as root"; exit 1; \
	fi
	systemctl stop xray-waybar-tun.service 2>/dev/null || true
	rm -f /etc/systemd/system/xray-waybar-tun.service
	rm -f /etc/polkit-1/rules.d/50-xray-waybar.rules
	rm -rf /etc/xray-waybar
	systemctl daemon-reload

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f $(BIN)

run-status: build
	./$(BIN) status
