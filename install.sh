#!/usr/bin/env bash
set -Eeuo pipefail

# One-command installer for the Mihomo canary. It is intentionally
# self-contained: a clean Linux machine only needs curl/wget, gzip and
# systemd for the optional watchdog.

REPO="${XRAY_WAYBAR_REPO:-hollowgxd/xray-waybar-ctl}"
CTL_VERSION="${XRAY_WAYBAR_VERSION:-v0.1.0-canary.1}"
MIHOMO_VERSION="${XRAY_WAYBAR_MIHOMO_VERSION:-v1.19.29}"

PREFIX="${XRAY_WAYBAR_PREFIX:-${HOME}/.local}"
CONFIG_HOME="${XRAY_WAYBAR_CONFIG_HOME:-${XDG_CONFIG_HOME:-${HOME}/.config}}"
DATA_HOME="${XRAY_WAYBAR_DATA_HOME:-${XDG_DATA_HOME:-${HOME}/.local/share}}"
CACHE_HOME="${XRAY_WAYBAR_CACHE_HOME:-${XDG_CACHE_HOME:-${HOME}/.cache}}"
SYSTEMD_USER_DIR="${XRAY_WAYBAR_SYSTEMD_USER_DIR:-${CONFIG_HOME}/systemd/user}"
SYSTEM_MIHOMO_BIN="${XRAY_WAYBAR_SYSTEM_BIN:-/usr/local/lib/xray-waybar/mihomo}"

BIN_DIR="${PREFIX}/bin"
APP_CONFIG_DIR="${CONFIG_HOME}/xray-waybar"
APP_CONFIG="${APP_CONFIG_DIR}/app.yaml"
APP_DATA_DIR="${DATA_HOME}/xray-waybar"
APP_CACHE_DIR="${CACHE_HOME}/xray-waybar"
LOCAL_MIHOMO_BIN="${PREFIX}/lib/xray-waybar/mihomo"
MANIFEST="${APP_DATA_DIR}/install-manifest"

SUBSCRIPTION_URL="${XRAY_WAYBAR_SUBSCRIPTION:-}"
ENABLE_TUN=true
CONNECT_NOW=true
ASSUME_YES=false
UNINSTALL=false
PURGE=false

if [[ -t 1 ]]; then
  BOLD=$'\033[1m'
  GREEN=$'\033[32m'
  YELLOW=$'\033[33m'
  RED=$'\033[31m'
  RESET=$'\033[0m'
else
  BOLD=""
  GREEN=""
  YELLOW=""
  RED=""
  RESET=""
fi

info() { printf '%s\n' "${BOLD}›${RESET} $*"; }
ok() { printf '%s\n' "${GREEN}✓${RESET} $*"; }
warn() { printf '%s\n' "${YELLOW}!${RESET} $*" >&2; }
die() {
  printf '%s\n' "${RED}Ошибка:${RESET} $*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
xray-waybar-ctl — простой установщик Mihomo для Linux

Использование:
  bash install.sh [параметры]

Параметры:
  --subscription URL  URL подписки (без этого установщик спросит его)
  --no-tun            только локальный HTTP/SOCKS, без VPN для всей системы
  --no-connect        установить, но пока не подключаться
  --yes               не задавать вопрос подтверждения
  --uninstall         удалить программу, сохранив конфиг и данные
  --purge             с --uninstall также удалить конфиг и данные
  -h, --help          показать эту справку

Для автоматической установки URL безопаснее передать через окружение:
  XRAY_WAYBAR_SUBSCRIPTION='https://…' bash install.sh --yes
EOF
}

while (($# > 0)); do
  case "$1" in
    --subscription)
      (($# >= 2)) || die "--subscription требует URL"
      SUBSCRIPTION_URL="$2"
      shift 2
      ;;
    --no-tun)
      ENABLE_TUN=false
      shift
      ;;
    --no-connect)
      CONNECT_NOW=false
      shift
      ;;
    --yes|-y)
      ASSUME_YES=true
      shift
      ;;
    --uninstall)
      UNINSTALL=true
      shift
      ;;
    --purge)
      PURGE=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "неизвестный параметр: $1 (см. --help)"
      ;;
  esac
done

if "$PURGE" && ! "$UNINSTALL"; then
  die "--purge используется только вместе с --uninstall"
fi

[[ "$(uname -s)" == "Linux" ]] || die "сейчас поддерживается только Linux"
[[ ${EUID} -ne 0 ]] || die "не запускай весь установщик через sudo; он сам запросит права для TUN"

case "$(uname -m)" in
  x86_64|amd64)
    ARCH="amd64"
    MIHOMO_SHA256="60de76a35a6cbf7b4fa4a20f5c257c24345d1d635ab1aa3877022a1997ef413c"
    ;;
  aarch64|arm64)
    ARCH="arm64"
    MIHOMO_SHA256="9a868b5e4e0ad91d9d71e1b41b0cfce78aaba44360c30df74a723f8e3926a86c"
    ;;
  *)
    die "архитектура $(uname -m) пока не поддерживается установщиком"
    ;;
esac

read_tty() {
  local prompt="$1"
  local reply
  if [[ -r /dev/tty ]]; then
    IFS= read -r -p "$prompt" reply </dev/tty || true
  else
    IFS= read -r -p "$prompt" reply || true
  fi
  printf '%s' "$reply"
}

confirm() {
  local prompt="$1"
  local default="${2:-yes}"
  local suffix="[Y/n]"
  [[ "$default" == "no" ]] && suffix="[y/N]"
  if "$ASSUME_YES"; then
    return 0
  fi
  local reply
  reply="$(read_tty "$prompt $suffix ")"
  reply="${reply,,}"
  if [[ "$default" == "yes" ]]; then
    [[ -z "$reply" || "$reply" == "y" || "$reply" == "yes" || "$reply" == "д" || "$reply" == "да" ]]
  else
    [[ "$reply" == "y" || "$reply" == "yes" || "$reply" == "д" || "$reply" == "да" ]]
  fi
}

download() {
  local url="$1"
  local output="$2"
  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --silent --show-error \
      --retry 3 --retry-delay 1 --connect-timeout 15 \
      --output "$output" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --tries=3 --timeout=30 --output-document="$output" "$url"
  else
    die "нужен curl или wget"
  fi
}

verify_sha256() {
  local file="$1"
  local expected="$2"
  local actual
  command -v sha256sum >/dev/null 2>&1 || die "не найден sha256sum (пакет coreutils)"
  actual="$(sha256sum "$file" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || die "контрольная сумма не совпала для $(basename "$file")"
}

privileged() {
  if command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  elif command -v doas >/dev/null 2>&1; then
    doas "$@"
  else
    die "для системного TUN нужен sudo или doas (либо запусти с --no-tun)"
  fi
}

ensure_setcap() {
  command -v setcap >/dev/null 2>&1 && return 0
  info "Устанавливаю утилиту setcap…"
  if command -v pacman >/dev/null 2>&1; then
    privileged pacman -S --needed --noconfirm libcap
  elif command -v apt-get >/dev/null 2>&1; then
    privileged apt-get update
    privileged apt-get install -y libcap2-bin
  elif command -v dnf >/dev/null 2>&1; then
    privileged dnf install -y libcap
  elif command -v zypper >/dev/null 2>&1; then
    privileged zypper --non-interactive install libcap-progs
  else
    die "не найден setcap; установи пакет libcap/libcap2-bin и повтори"
  fi
  command -v setcap >/dev/null 2>&1 || die "setcap не появился после установки пакета"
}

write_user_units() {
  mkdir -p "$SYSTEMD_USER_DIR"
  cat >"${SYSTEMD_USER_DIR}/xray-waybar-ping.service" <<EOF
[Unit]
Description=xray-waybar — periodic Mihomo liveness check

[Service]
Type=oneshot
ExecStart=${BIN_DIR}/xray-waybar-ctl ping
Nice=10
IOSchedulingClass=idle
EOF

  cat >"${SYSTEMD_USER_DIR}/xray-waybar-ping.timer" <<'EOF'
[Unit]
Description=Run xray-waybar liveness check every 30 seconds

[Timer]
OnBootSec=15s
OnActiveSec=15s
OnUnitActiveSec=30s
AccuracySec=1s

[Install]
WantedBy=timers.target
EOF

  cat >"${SYSTEMD_USER_DIR}/xray-waybar-watchdog.service" <<EOF
[Unit]
Description=xray-waybar — Mihomo auto-reconnect watchdog
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=${BIN_DIR}/xray-waybar-ctl watchdog --loop 10s
Restart=on-failure
RestartSec=5s
CPUQuota=50%
Nice=5

[Install]
WantedBy=default.target
EOF
}

write_waybar_snippets() {
  mkdir -p "$APP_DATA_DIR"
  cat >"${APP_DATA_DIR}/waybar-module.jsonc" <<EOF
"custom/vpn": {
    "exec": "${BIN_DIR}/xray-waybar-ctl status",
    "interval": 5,
    "return-type": "json",
    "on-click": "${BIN_DIR}/xray-waybar-ctl toggle",
    "on-click-right": "${BIN_DIR}/xray-waybar-ctl reconnect",
    "on-scroll-up": "${BIN_DIR}/xray-waybar-ctl use-next",
    "on-scroll-down": "${BIN_DIR}/xray-waybar-ctl use-prev"
}
EOF
  cat >"${APP_DATA_DIR}/waybar-style.css" <<'EOF'
#custom-vpn { padding: 0 8px; }
#custom-vpn.connected { color: #a6e3a1; }
#custom-vpn.loading { color: #f9e2af; }
#custom-vpn.error, #custom-vpn.degraded { color: #f38ba8; }
EOF
}

yaml_quote() {
  local value="${1//\'/\'\'}"
  printf "'%s'" "$value"
}

write_config_if_missing() {
  if [[ -f "$APP_CONFIG" ]]; then
    warn "Существующий конфиг сохранён без изменений: $APP_CONFIG"
    return 0
  fi

  if [[ -z "$SUBSCRIPTION_URL" ]]; then
    SUBSCRIPTION_URL="$(read_tty "Вставь URL подписки: ")"
  fi
  [[ "$SUBSCRIPTION_URL" =~ ^https?:// ]] || die "URL подписки должен начинаться с http:// или https://"

  mkdir -p "$APP_CONFIG_DIR" "$APP_DATA_DIR" "$APP_CACHE_DIR"
  umask 077
  cat >"$APP_CONFIG" <<EOF
# Создано install.sh. Подписка и локальные управляющие порты не публикуются.
subscription_url: $(yaml_quote "$SUBSCRIPTION_URL")
subscription_update_interval: 3600

core: mihomo
mihomo_bin: $(yaml_quote "$MIHOMO_BIN")
mihomo_home: $(yaml_quote "${APP_DATA_DIR}/mihomo")
mihomo_config: $(yaml_quote "${APP_DATA_DIR}/mihomo/config.yaml")
mihomo_subscription_file: $(yaml_quote "${APP_CACHE_DIR}/mihomo-subscription.yaml")
mihomo_secret_file: $(yaml_quote "${APP_DATA_DIR}/mihomo-secret")
mihomo_controller: "127.0.0.1:9090"
mihomo_tun_stack: mixed

xray_port: 1080
pid_file: $(yaml_quote "${APP_CACHE_DIR}/core.pid")
log_file: $(yaml_quote "${APP_DATA_DIR}/core.log")
cache_file: $(yaml_quote "${APP_CACHE_DIR}/servers.json")
state_file: $(yaml_quote "${APP_CACHE_DIR}/state.json")
hwid_file: $(yaml_quote "${APP_DATA_DIR}/hwid")

test_url: "http://www.gstatic.com/generate_204"
test_timeout: 6000
test_concurrency: 3
system_wide: ${ENABLE_TUN}
EOF
  chmod 600 "$APP_CONFIG"
  ok "Создан приватный конфиг: $APP_CONFIG"
}

stop_user_units() {
  if command -v systemctl >/dev/null 2>&1 && [[ "${XRAY_WAYBAR_SKIP_SYSTEMD:-0}" != "1" ]]; then
    systemctl --user disable --now xray-waybar-ping.timer >/dev/null 2>&1 || true
    systemctl --user disable --now xray-waybar-watchdog.service >/dev/null 2>&1 || true
  fi
}

uninstall_all() {
  printf '%s\n' "${BOLD}Удаление xray-waybar-ctl${RESET}"
  if ! confirm "Остановить VPN и удалить установленные бинарники?" "no"; then
    info "Отменено."
    exit 0
  fi

  if [[ -x "${BIN_DIR}/xray-waybar-ctl" ]]; then
    "${BIN_DIR}/xray-waybar-ctl" disconnect >/dev/null 2>&1 || true
  fi
  stop_user_units
  rm -f \
    "${SYSTEMD_USER_DIR}/xray-waybar-ping.service" \
    "${SYSTEMD_USER_DIR}/xray-waybar-ping.timer" \
    "${SYSTEMD_USER_DIR}/xray-waybar-watchdog.service" \
    "${BIN_DIR}/xray-waybar-ctl" \
    "$LOCAL_MIHOMO_BIN"
  if command -v systemctl >/dev/null 2>&1 && [[ "${XRAY_WAYBAR_SKIP_SYSTEMD:-0}" != "1" ]]; then
    systemctl --user daemon-reload >/dev/null 2>&1 || true
  fi

  if [[ -f "$MANIFEST" ]] && grep -Fxq "mihomo_path=${SYSTEM_MIHOMO_BIN}" "$MANIFEST"; then
    privileged rm -f "$SYSTEM_MIHOMO_BIN"
  fi

  if "$PURGE"; then
    rm -rf -- "$APP_CONFIG_DIR" "$APP_DATA_DIR" "$APP_CACHE_DIR"
    ok "Программа, конфиг и локальные данные удалены."
  else
    rm -f "$MANIFEST" "${APP_DATA_DIR}/waybar-module.jsonc" "${APP_DATA_DIR}/waybar-style.css"
    ok "Программа удалена; конфиг и данные сохранены."
  fi
}

if "$UNINSTALL"; then
  uninstall_all
  exit 0
fi

printf '%s\n' "${BOLD}xray-waybar-ctl + Mihomo${RESET}"
printf '%s\n' "Готовая тестовая установка для Linux (${ARCH})."
printf '%s\n' "  • xray-waybar-ctl ${CTL_VERSION}"
printf '%s\n' "  • официальный Mihomo ${MIHOMO_VERSION}"
if "$ENABLE_TUN"; then
  printf '%s\n' "  • весь трафик через native TUN (нужен один запрос sudo)"
else
  printf '%s\n' "  • только локальный HTTP/SOCKS на 127.0.0.1:1080"
fi
printf '\n'

if ! confirm "Продолжить установку?"; then
  info "Отменено."
  exit 0
fi

TMP_DIR="$(mktemp -d -t xray-waybar-install.XXXXXX)"
cleanup() { rm -rf -- "$TMP_DIR"; }
trap cleanup EXIT

mkdir -p "$BIN_DIR" "$APP_DATA_DIR" "$APP_CACHE_DIR"

info "Устанавливаю xray-waybar-ctl…"
CTL_TMP="${TMP_DIR}/xray-waybar-ctl"
if [[ -n "${XRAY_WAYBAR_CTL_BINARY:-}" ]]; then
  cp "${XRAY_WAYBAR_CTL_BINARY}" "$CTL_TMP"
else
  CTL_NAME="xray-waybar-ctl-linux-${ARCH}"
  RELEASE_BASE="https://github.com/${REPO}/releases/download/${CTL_VERSION}"
  download "${RELEASE_BASE}/${CTL_NAME}" "$CTL_TMP"
  download "${RELEASE_BASE}/checksums.txt" "${TMP_DIR}/checksums.txt"
  CTL_SHA256="$(awk -v name="$CTL_NAME" '$2 == name || $2 == "*" name {print $1; exit}' "${TMP_DIR}/checksums.txt")"
  [[ -n "$CTL_SHA256" ]] || die "в checksums.txt нет $CTL_NAME"
  verify_sha256 "$CTL_TMP" "$CTL_SHA256"
fi
install -m 755 "$CTL_TMP" "${BIN_DIR}/xray-waybar-ctl"
ok "Контроллер установлен."

info "Устанавливаю Mihomo…"
MIHOMO_TMP="${TMP_DIR}/mihomo"
if [[ -n "${XRAY_WAYBAR_MIHOMO_BINARY:-}" ]]; then
  cp "${XRAY_WAYBAR_MIHOMO_BINARY}" "$MIHOMO_TMP"
else
  MIHOMO_ASSET="mihomo-linux-${ARCH}-${MIHOMO_VERSION}.gz"
  MIHOMO_GZ="${TMP_DIR}/${MIHOMO_ASSET}"
  download "https://github.com/MetaCubeX/mihomo/releases/download/${MIHOMO_VERSION}/${MIHOMO_ASSET}" "$MIHOMO_GZ"
  verify_sha256 "$MIHOMO_GZ" "$MIHOMO_SHA256"
  gzip -dc "$MIHOMO_GZ" >"$MIHOMO_TMP"
fi
chmod 755 "$MIHOMO_TMP"

if "$ENABLE_TUN"; then
  ensure_setcap
  if [[ -e "$SYSTEM_MIHOMO_BIN" ]] &&
     { [[ ! -f "$MANIFEST" ]] || ! grep -Fxq "mihomo_path=${SYSTEM_MIHOMO_BIN}" "$MANIFEST"; }; then
    die "$SYSTEM_MIHOMO_BIN уже существует и не принадлежит этому установщику; задай другой XRAY_WAYBAR_SYSTEM_BIN"
  fi
  privileged install -Dm755 "$MIHOMO_TMP" "$SYSTEM_MIHOMO_BIN"
  privileged setcap cap_net_admin+ep "$SYSTEM_MIHOMO_BIN"
  MIHOMO_BIN="$SYSTEM_MIHOMO_BIN"
  ok "Mihomo установлен с минимальной capability cap_net_admin."
else
  install -Dm755 "$MIHOMO_TMP" "$LOCAL_MIHOMO_BIN"
  MIHOMO_BIN="$LOCAL_MIHOMO_BIN"
  ok "Mihomo установлен без системных прав."
fi

write_config_if_missing
write_user_units
write_waybar_snippets

cat >"$MANIFEST" <<EOF
ctl_version=${CTL_VERSION}
mihomo_version=${MIHOMO_VERSION}
mihomo_path=${MIHOMO_BIN}
EOF
chmod 600 "$MANIFEST"

if command -v systemctl >/dev/null 2>&1 && [[ "${XRAY_WAYBAR_SKIP_SYSTEMD:-0}" != "1" ]]; then
  info "Включаю проверку соединения и автореконнект…"
  if systemctl --user daemon-reload &&
     systemctl --user enable --now xray-waybar-ping.timer xray-waybar-watchdog.service; then
    ok "User-systemd мониторинг включён."
  else
    warn "Не удалось включить user-systemd сейчас. После входа в графическую сессию выполни:"
    warn "systemctl --user enable --now xray-waybar-ping.timer xray-waybar-watchdog.service"
  fi
else
  warn "systemd user units установлены, но не включены в этом окружении."
fi

if [[ ":${PATH}:" != *":${BIN_DIR}:"* ]]; then
  warn "${BIN_DIR} отсутствует в PATH этой оболочки."
  warn "Добавь в shell profile: export PATH=\"${BIN_DIR}:\$PATH\""
fi

if "$CONNECT_NOW"; then
  info "Проверяю подписку и подключаюсь…"
  if "${BIN_DIR}/xray-waybar-ctl" connect; then
    ok "VPN подключён."
    "${BIN_DIR}/xray-waybar-ctl" status || true
  else
    warn "Установка завершена, но первое подключение не удалось."
    warn "Лог: ${APP_DATA_DIR}/core.log"
    warn "Повтор: ${BIN_DIR}/xray-waybar-ctl connect"
    exit 1
  fi
fi

printf '\n%s\n' "${GREEN}${BOLD}Готово.${RESET}"
printf '%s\n' "Команды:"
printf '%s\n' "  ${BIN_DIR}/xray-waybar-ctl toggle      # включить/выключить"
printf '%s\n' "  ${BIN_DIR}/xray-waybar-ctl test        # проверить узлы"
printf '%s\n' "  ${BIN_DIR}/xray-waybar-ctl status      # состояние для Waybar"
printf '\n%s\n' "Для значка в Waybar:"
printf '%s\n' "  1. Добавь custom/vpn в modules-left/center/right."
printf '%s\n' "  2. Вставь модуль из ${APP_DATA_DIR}/waybar-module.jsonc в конфиг Waybar."
printf '%s\n' "  3. Добавь ${APP_DATA_DIR}/waybar-style.css в свой style.css и перезапусти Waybar."
