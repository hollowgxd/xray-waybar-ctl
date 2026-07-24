#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
smoke_root="$(mktemp -d -t xray-waybar-smoke.XXXXXX)"
cleanup() { rm -rf -- "$smoke_root"; }
trap cleanup EXIT

ctl_binary="${smoke_root}/xray-waybar-ctl"
(
  cd "$repo_root"
  go build -o "$ctl_binary" ./cmd/xray-waybar-ctl
)

common_env=(
  "XRAY_WAYBAR_PREFIX=${smoke_root}/prefix"
  "XRAY_WAYBAR_CONFIG_HOME=${smoke_root}/config"
  "XRAY_WAYBAR_DATA_HOME=${smoke_root}/data"
  "XRAY_WAYBAR_CACHE_HOME=${smoke_root}/cache"
  "XRAY_WAYBAR_SYSTEMD_USER_DIR=${smoke_root}/systemd"
  "XRAY_WAYBAR_CTL_BINARY=${ctl_binary}"
  "XRAY_WAYBAR_MIHOMO_BINARY=/bin/true"
  "XRAY_WAYBAR_SKIP_SYSTEMD=1"
)

env "${common_env[@]}" bash "${repo_root}/install.sh" \
  --yes --no-tun --no-connect \
  --subscription "https://example.test/sub/secret-token"

config="${smoke_root}/config/xray-waybar/app.yaml"
test -x "${smoke_root}/prefix/bin/xray-waybar-ctl"
test -x "${smoke_root}/prefix/lib/xray-waybar/mihomo"
test -f "${smoke_root}/systemd/xray-waybar-watchdog.service"
test -f "${smoke_root}/data/xray-waybar/waybar-module.jsonc"
test "$(stat -c '%a' "$config")" = "600"
grep -Fq "core: mihomo" "$config"
grep -Fq "system_wide: false" "$config"
grep -Fq "https://example.test/sub/secret-token" "$config"

config_before="$(sha256sum "$config" | awk '{print $1}')"
env "${common_env[@]}" bash "${repo_root}/install.sh" \
  --yes --no-tun --no-connect \
  --subscription "https://must-not-overwrite.example/sub"
config_after="$(sha256sum "$config" | awk '{print $1}')"
test "$config_before" = "$config_after"

bash -n "${repo_root}/install.sh"
bash -n "${repo_root}/scripts/build-release.sh"
printf 'install smoke: ok\n'
