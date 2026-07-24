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
  "XRAY_WAYBAR_SKIP_RELOAD=1"
)

mkdir -p "${smoke_root}/waybar"
cat >"${smoke_root}/waybar/config.jsonc" <<'EOF'
{
  // smoke comment must survive
  "modules-right": [
    "clock",
  ],
}
EOF

env "${common_env[@]}" bash "${repo_root}/install.sh" \
  --yes --no-tun --no-connect \
  --waybar-config "${smoke_root}/waybar/config.jsonc" \
  --subscription "https://example.test/sub/secret-token"

config="${smoke_root}/config/xray-waybar/app.yaml"
test -x "${smoke_root}/prefix/bin/xray-waybar-ctl"
test -x "${smoke_root}/prefix/lib/xray-waybar/mihomo"
test -f "${smoke_root}/systemd/xray-waybar-watchdog.service"
test -f "${smoke_root}/data/xray-waybar/waybar-module.jsonc"
grep -Fq "// smoke comment must survive" "${smoke_root}/waybar/config.jsonc"
grep -Fq '"custom/vpn"' "${smoke_root}/waybar/config.jsonc"
test "$(stat -c '%a' "$config")" = "600"
grep -Fq "core: mihomo" "$config"
grep -Fq "system_wide: false" "$config"
grep -Fq "https://example.test/sub/secret-token" "$config"

config_before="$(sha256sum "$config" | awk '{print $1}')"
env "${common_env[@]}" bash "${repo_root}/install.sh" \
  --yes --no-tun --no-connect \
  --waybar-config "${smoke_root}/waybar/config.jsonc" \
  --subscription "https://must-not-overwrite.example/sub"
config_after="$(sha256sum "$config" | awk '{print $1}')"
test "$config_before" = "$config_after"
test "$(find "${smoke_root}/waybar" -maxdepth 1 -name 'config.jsonc.xray-waybar-backup-*' | wc -l)" = "1"

# Simulate an installation over the legacy Xray config that caused a
# graphical polkit password prompt on every Waybar click. The updater
# must migrate only the backend fields while retaining the secret URL.
sed -i \
  -e 's/^core: mihomo$/core: xray/' \
  -e 's|^mihomo_bin:.*$|mihomo_bin: /usr/bin/mihomo|' \
  -e 's/^system_wide: false$/system_wide: true/' \
  "$config"
env "${common_env[@]}" bash "${repo_root}/install.sh" \
  --yes --no-tun --no-connect \
  --waybar-config "${smoke_root}/waybar/config.jsonc"
grep -Fq "core: mihomo" "$config"
grep -Fq "mihomo_bin: '${smoke_root}/prefix/lib/xray-waybar/mihomo'" "$config"
grep -Fq "system_wide: false" "$config"
grep -Fq "https://example.test/sub/secret-token" "$config"
test "$(find "${smoke_root}/config/xray-waybar" -maxdepth 1 -name 'app.yaml.before-mihomo-*' | wc -l)" = "1"

bash -n "${repo_root}/install.sh"
bash -n "${repo_root}/scripts/build-release.sh"
printf 'install smoke: ok\n'
