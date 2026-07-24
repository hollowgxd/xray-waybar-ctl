#!/usr/bin/env bash
set -Eeuo pipefail

version="${1:-}"
[[ -n "$version" ]] || {
  printf 'usage: %s <version>\n' "$0" >&2
  exit 2
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist_dir="${repo_root}/dist"
mkdir -p "$dist_dir"
rm -f \
  "${dist_dir}/xray-waybar-ctl-linux-amd64" \
  "${dist_dir}/xray-waybar-ctl-linux-arm64" \
  "${dist_dir}/checksums.txt"

for arch in amd64 arm64; do
  output="${dist_dir}/xray-waybar-ctl-linux-${arch}"
  printf 'building %s\n' "$(basename "$output")"
  (
    cd "$repo_root"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w -X main.version=${version}" \
      -o "$output" ./cmd/xray-waybar-ctl
  )
done

(
  cd "$dist_dir"
  sha256sum xray-waybar-ctl-linux-amd64 xray-waybar-ctl-linux-arm64 >checksums.txt
)

printf 'release files:\n'
ls -lh \
  "${dist_dir}/xray-waybar-ctl-linux-amd64" \
  "${dist_dir}/xray-waybar-ctl-linux-arm64" \
  "${dist_dir}/checksums.txt"
