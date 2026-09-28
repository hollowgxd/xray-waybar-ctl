#!/usr/bin/env bash
# curl -fsSL https://raw.githubusercontent.com/hollowgxd/xray-waybar-ctl/v0.1.2/install.sh | bash
set -euo pipefail

REPO="hollowgxd/xray-waybar-ctl"
REF="${XRAY_WAYBAR_REF:-master}"
REPLACE_HAPP=0
RELOAD=1

usage() {
  cat <<'EOF'
Usage: bash install.sh [--replace-happ] [--no-reload]

Installs the CLI into ~/.local/bin and adds custom/xray to your existing
Waybar config. No sudo and no changes to VPN/network settings. Existing
app.yaml is never overwritten. Changed Waybar files get timestamped backups.

--replace-happ  Replace custom/happ in the Waybar module list (keeps its files)
--no-reload     Do not signal a running Waybar process
EOF
}

while (($#)); do
  case "$1" in
    --replace-happ) REPLACE_HAPP=1 ;;
    --no-reload) RELOAD=0 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'Unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

if [[ $(id -u) -eq 0 ]]; then
  echo 'Run as your desktop user, without sudo.' >&2
  exit 1
fi

for cmd in python3 xray waybar curl tar; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    printf 'Missing %s. Install it with your distribution package manager, then rerun.\n' "$cmd" >&2
    exit 1
  fi
done

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT
SOURCE_DIR=""
USE_RELEASE=0
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi
if [[ -z "$SOURCE_DIR" || ! -f "$SOURCE_DIR/go.mod" || ! -f "$SOURCE_DIR/scripts/waybar-integrate.py" ]]; then
  if [[ ! "$REF" =~ ^[a-zA-Z0-9._/-]+$ || "$REF" == *..* ]]; then
    echo 'XRAY_WAYBAR_REF contains invalid characters.' >&2
    exit 1
  fi
  if [[ "$REF" == master ]]; then
    case "$(uname -s)-$(uname -m)" in
      Linux-x86_64) RELEASE_ARCH=amd64 ;;
      Linux-aarch64|Linux-arm64) RELEASE_ARCH=arm64 ;;
      *) RELEASE_ARCH="" ;;
    esac
    if [[ -n "$RELEASE_ARCH" ]] && command -v sha256sum >/dev/null 2>&1 && \
       curl --fail --location --silent --show-error --retry 2 \
         "https://api.github.com/repos/$REPO/releases/latest" -o "$WORK_DIR/release.json"; then
      RELEASE_INFO="$(python3 - "$WORK_DIR/release.json" "$RELEASE_ARCH" <<'PY_RELEASE'
import json, sys
try:
    release = json.load(open(sys.argv[1]))
    name = "xray-waybar-ctl-linux-" + sys.argv[2]
    asset = next(a for a in release["assets"] if a["name"] == name)
    digest = asset["digest"]
    print(release["tag_name"] + "\t" + (digest[7:] if digest.startswith("sha256:") else ""))
except (KeyError, ValueError, StopIteration):
    pass
PY_RELEASE
)"
      RELEASE_TAG="${RELEASE_INFO%%$'\t'*}"
      RELEASE_DIGEST="${RELEASE_INFO#*$'\t'}"
      if [[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ &&
            "$RELEASE_DIGEST" =~ ^[0-9a-f]{64}$ ]]; then
        REF="$RELEASE_TAG"
        USE_RELEASE=1
        RELEASE_ASSET="xray-waybar-ctl-linux-$RELEASE_ARCH"
      fi
    fi
  fi
  SOURCE_DIR="$WORK_DIR/source"
  mkdir -p "$SOURCE_DIR"
  echo "Downloading $REPO ($REF)..."
  curl --fail --location --silent --show-error --retry 3 \
    "https://codeload.github.com/$REPO/tar.gz/$REF" \
    | tar -xz -C "$SOURCE_DIR" --strip-components=1
  test -f "$SOURCE_DIR/go.mod" && test -f "$SOURCE_DIR/scripts/waybar-integrate.py"
fi

TARGET_BIN="$HOME/.local/bin/xray-waybar-ctl"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/xray-waybar"
CONFIG_FILE="$CONFIG_DIR/app.yaml"
INTEGRATE=(python3 "$SOURCE_DIR/scripts/waybar-integrate.py" --home "$HOME" --binary "$TARGET_BIN")
if ((REPLACE_HAPP)); then INTEGRATE+=(--replace-happ); fi
"${INTEGRATE[@]}" --check

if ((USE_RELEASE)); then
  RELEASE_URL="https://github.com/$REPO/releases/download/$REF"
  echo "Installing verified release $REF ($RELEASE_ARCH)..."
  curl --fail --location --silent --show-error --connect-timeout 10 --max-time 120 --retry 2 \
    "$RELEASE_URL/$RELEASE_ASSET" -o "$WORK_DIR/$RELEASE_ASSET"
  (cd "$WORK_DIR" && printf '%s  %s\n' "$RELEASE_DIGEST" "$RELEASE_ASSET" \
    | sha256sum --check --status) || {
    echo 'Release checksum verification failed.' >&2
    exit 1
  }
  mv "$WORK_DIR/$RELEASE_ASSET" "$WORK_DIR/xray-waybar-ctl"
  chmod 755 "$WORK_DIR/xray-waybar-ctl"
else
  command -v go >/dev/null 2>&1 || {
    echo 'No compatible release found; install Go for source build, then rerun.' >&2
    exit 1
  }
  echo 'Building xray-waybar-ctl from source...'
  (cd "$SOURCE_DIR" && go build -trimpath -ldflags "-X main.version=$REF" \
    -o "$WORK_DIR/xray-waybar-ctl" ./cmd/xray-waybar-ctl)
fi
"$WORK_DIR/xray-waybar-ctl" version
mkdir -p "$(dirname "$TARGET_BIN")" "$CONFIG_DIR" "$HOME/.local/share/xray-waybar"
install -m 755 "$WORK_DIR/xray-waybar-ctl" "$TARGET_BIN"
install -m 644 "$SOURCE_DIR/configs/app.yaml.example" \
  "$HOME/.local/share/xray-waybar/app.yaml.example"
if [[ ! -e "$CONFIG_FILE" ]]; then
  (umask 077; printf '# Paste your subscription URL below, then run: xray-waybar-ctl update\nsubscription_url: ""\nxray_bin: "%s"\n' "$(command -v xray)" > "$CONFIG_FILE")
  echo "Created $CONFIG_FILE (subscription URL still needed)."
else
  echo "Kept existing $CONFIG_FILE."
fi
"${INTEGRATE[@]}"
if ((RELOAD)) && pgrep -u "$(id -u)" -x waybar >/dev/null 2>&1; then
  pkill -u "$(id -u)" -SIGUSR2 -x waybar || true
  echo 'Reloaded Waybar.'
fi
printf '\nInstalled: %s\n' "$TARGET_BIN"
if ! command -v walker >/dev/null 2>&1 && ! command -v wofi >/dev/null 2>&1 && ! command -v rofi >/dev/null 2>&1; then
  echo 'Install walker, wofi or rofi for the right-click server menu.'
fi
if grep -Eq '^subscription_url:[[:space:]]*(""|https://example\.com|"https://example\.com)' "$CONFIG_FILE"; then
  echo "Next: edit $CONFIG_FILE and set subscription_url, then run: $TARGET_BIN update"
else
  echo "Ready: $TARGET_BIN status"
fi
