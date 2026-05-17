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
	install -Dm644 configs/pacman/xray-waybar.hook /etc/pacman.d/hooks/xray-waybar.hook
	# Suspend/resume is handled by the watchdog daemon talking to
	# logind over D-Bus (see internal/sleepwatch). Nothing to install
	# for the system-sleep path anymore — that machinery moved
	# entirely into the user-session watchdog.
	# xray needs CAP_NET_ADMIN to setsockopt(SO_MARK) on its outbounds.
	# Without it the freedom (direct) outbound dials with no mark,
	# falls into `default dev tun0`, loops back through tun2socks into
	# socks-in and burns CPU. Apply now; the pacman hook above
	# reapplies after every xray upgrade so it survives `pacman -Syu`.
	setcap cap_net_admin+ep /usr/bin/xray
	systemctl daemon-reload
	@echo
	@echo "✓ systemd unit, polkit rule, pacman hook installed."
	@echo "✓ cap_net_admin set on /usr/bin/xray (auto-reapplied on upgrade)."
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
	install -Dm644 configs/systemd-user/xray-waybar-geo.service $(HOME)/.config/systemd/user/xray-waybar-geo.service
	install -Dm644 configs/systemd-user/xray-waybar-geo.timer $(HOME)/.config/systemd/user/xray-waybar-geo.timer
	systemctl --user daemon-reload
	systemctl --user enable --now xray-waybar-ping.timer
	systemctl --user enable --now xray-waybar-watchdog.service
	systemctl --user enable --now xray-waybar-geo.timer
	@echo
	@echo "✓ ping timer + watchdog daemon + geo timer installed and enabled."
	@echo "  Inspect:"
	@echo "    systemctl --user status xray-waybar-watchdog.service"
	@echo "    systemctl --user list-timers xray-waybar-ping.timer xray-waybar-geo.timer"
	@echo "    journalctl --user -u xray-waybar-watchdog.service -f"
	@echo "  First-time setup: run \`xray-waybar-ctl update-geo\` once now"
	@echo "  so the geo lists are present before the timer fires."

uninstall-monitor:
	-systemctl --user disable --now xray-waybar-ping.timer
	-systemctl --user disable --now xray-waybar-watchdog.service
	-systemctl --user disable --now xray-waybar-geo.timer
	rm -f $(HOME)/.config/systemd/user/xray-waybar-ping.timer
	rm -f $(HOME)/.config/systemd/user/xray-waybar-ping.service
	rm -f $(HOME)/.config/systemd/user/xray-waybar-watchdog.service
	rm -f $(HOME)/.config/systemd/user/xray-waybar-geo.timer
	rm -f $(HOME)/.config/systemd/user/xray-waybar-geo.service
	# Drop the legacy resume helper unit if a previous install put it
	# here. The watchdog now owns suspend/resume directly.
	rm -f $(HOME)/.config/systemd/user/xray-waybar-resume.service
	systemctl --user daemon-reload

uninstall-system:
	@if [ "$$(id -u)" != "0" ]; then \
	  echo "uninstall-system must run as root"; exit 1; \
	fi
	systemctl stop xray-waybar-tun.service 2>/dev/null || true
	rm -f /etc/systemd/system/xray-waybar-tun.service
	rm -f /etc/polkit-1/rules.d/50-xray-waybar.rules
	rm -f /etc/pacman.d/hooks/xray-waybar.hook
	# Legacy: previous installs dropped a system-sleep hook here.
	# The watchdog daemon now owns suspend/resume — remove the
	# stale hook so it can't conflict.
	rm -f /usr/lib/systemd/system-sleep/xray-waybar
	rm -rf /etc/xray-waybar
	# Drop the capability so an uninstalled xray-waybar leaves no
	# unexpected privilege on the xray binary.
	-setcap -r /usr/bin/xray 2>/dev/null
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
