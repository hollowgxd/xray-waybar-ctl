#!/usr/bin/env python3
"""Install the official stable Xray binary for this user, without pacman or sudo."""

import argparse
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import zipfile

SOURCE = "XTLS/Xray-core"
TAG = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")
DIGEST = re.compile(r"^[0-9a-f]{64}$")
ARCH = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}


def version_tuple(tag):
    match = TAG.fullmatch(tag)
    if not match:
        raise ValueError(f"invalid Xray release tag: {tag}")
    return tuple(int(part) for part in match.groups())


def release_from_pin(pin, arch):
    if pin.get("source") != SOURCE:
        raise ValueError("unexpected Xray pin source")
    tag = pin["version"]
    version_tuple(tag)
    asset = pin["assets"][arch]
    if not DIGEST.fullmatch(asset["sha256"]):
        raise ValueError("invalid pinned Xray digest")
    return tag, asset["name"], asset["sha256"]


def release_from_api(release, arch):
    if release.get("draft") or release.get("prerelease"):
        raise ValueError("GitHub latest is not a stable release")
    tag = release["tag_name"]
    version_tuple(tag)
    expected_name = {"amd64": "Xray-linux-64.zip", "arm64": "Xray-linux-arm64-v8a.zip"}[arch]
    asset = next(a for a in release["assets"] if a["name"] == expected_name)
    raw_digest = asset["digest"]
    if not raw_digest.startswith("sha256:") or not DIGEST.fullmatch(raw_digest[7:]):
        raise ValueError("official release has no SHA-256 digest")
    return tag, expected_name, raw_digest[7:]


def curl(url, output):
    subprocess.run(
        ["curl", "--fail", "--location", "--silent", "--show-error",
         "--connect-timeout", "10", "--max-time", "180", "--retry", "2",
         url, "-o", str(output)],
        check=True,
    )


def select_release(pin, arch, work):
    pinned = release_from_pin(pin, arch)
    metadata = work / "release.json"
    try:
        curl(f"https://api.github.com/repos/{SOURCE}/releases/latest", metadata)
        current = release_from_api(json.loads(metadata.read_text()), arch)
        if version_tuple(current[0]) < version_tuple(pinned[0]):
            raise ValueError("GitHub latest is older than repository pin")
        return current, "GitHub stable"
    except (subprocess.CalledProcessError, OSError, ValueError, KeyError, StopIteration) as exc:
        print(f"Using pinned Xray {pinned[0]} ({exc})", file=sys.stderr)
        return pinned, "repository pin"


def verify_archive(path, expected):
    sha = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            sha.update(chunk)
    if sha.hexdigest() != expected:
        raise ValueError("official Xray archive failed SHA-256 verification")


def extract_binary(archive_path, binary_path, tag):
    with zipfile.ZipFile(archive_path) as archive:
        with archive.open("xray") as source, binary_path.open("wb") as target:
            shutil.copyfileobj(source, target)
    binary_path.chmod(0o755)
    result = subprocess.run([str(binary_path), "version"], capture_output=True,
                            text=True, timeout=15, check=True)
    expected = "Xray " + tag[1:]
    if not result.stdout.startswith(expected + " "):
        raise ValueError(f"Xray binary version does not match {tag}")


def configure(config_path, binary_path):
    if config_path.is_symlink():
        config_path = config_path.resolve(strict=True)
    value = json.dumps(str(binary_path))
    config_path.parent.mkdir(parents=True, exist_ok=True)
    if not config_path.exists():
        fd = os.open(config_path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write('# Paste your subscription URL, then run: xray-waybar-ctl update\n')
            stream.write('subscription_url: ""\n')
            stream.write(f'xray_bin: {value}\n')
        print(f"Created {config_path} (subscription URL still needed).")
        return
    old = config_path.read_text()
    matches = list(re.finditer(r'^xray_bin:[^\n]*$', old, re.MULTILINE))
    if len(matches) > 1:
        raise ValueError(f"multiple xray_bin keys in {config_path}")
    if matches:
        current = matches[0].group().split(":", 1)[1].strip().split("#", 1)[0].strip().strip("\"'")
        if current not in ("", "/usr/bin/xray", "/usr/local/bin/xray", str(binary_path)):
            print(f"Keeping custom xray_bin in {config_path}; managed core is at {binary_path}")
            return
        new = old[:matches[0].start()] + f'xray_bin: {value}' + old[matches[0].end():]
    else:
        new = old.rstrip("\n") + f'\nxray_bin: {value}\n'
    if new == old:
        print(f"Kept {config_path}.")
        return
    backup = config_path.with_name(config_path.name + ".xray-waybar-backup-" +
                                   datetime.now().strftime("%Y%m%d-%H%M%S-%f"))
    shutil.copy2(config_path, backup)
    fd, tmp = tempfile.mkstemp(prefix=f".{config_path.name}.", dir=config_path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            stream.write(new)
        os.chmod(tmp, config_path.stat().st_mode & 0o777)
        os.replace(tmp, config_path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)
    print(f"Updated {config_path}; backup: {backup}")


def install(home, pin_path, config_path):
    if platform.system() != "Linux" or platform.machine() not in ARCH:
        raise ValueError("managed Xray supports Linux x86_64 and aarch64 only")
    arch = ARCH[platform.machine()]
    pin = json.loads(pin_path.read_text())
    core_dir = home / ".local/share/xray-waybar/core"
    core_dir.mkdir(parents=True, exist_ok=True)
    binary = core_dir / "xray"
    version_file = core_dir / "version"
    with tempfile.TemporaryDirectory(prefix=".xray-install-", dir=core_dir) as tmp:
        work = Path(tmp)
        (tag, asset_name, digest), source = select_release(pin, arch, work)
        if binary.is_file() and version_file.is_file() and version_file.read_text().strip() == tag:
            try:
                result = subprocess.run([str(binary), "version"], capture_output=True,
                                        text=True, timeout=15, check=True)
                if result.stdout.startswith("Xray " + tag[1:] + " "):
                    print(f"Xray {tag} already installed ({source}).")
                    configure(config_path, binary)
                    return
            except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
                pass
        archive = work / asset_name
        print(f"Downloading official Xray {tag} ({arch}, {source})...")
        curl(f"https://github.com/{SOURCE}/releases/download/{tag}/{asset_name}", archive)
        verify_archive(archive, digest)
        staged = work / "xray"
        extract_binary(archive, staged, tag)
        if binary.exists():
            shutil.copy2(binary, core_dir / "xray.previous")
            if version_file.exists():
                shutil.copy2(version_file, core_dir / "version.previous")
        os.replace(staged, binary)
        version_file.write_text(tag + "\n")
        print(f"Installed verified Xray {tag}: {binary}")
    configure(config_path, binary)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--home", type=Path, required=True)
    parser.add_argument("--pin", type=Path, required=True)
    parser.add_argument("--config", type=Path, required=True)
    args = parser.parse_args()
    install(args.home, args.pin, args.config)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, zipfile.BadZipFile, subprocess.CalledProcessError,
            subprocess.TimeoutExpired) as exc:
        print(f"Xray installation failed: {exc}", file=sys.stderr)
        sys.exit(1)
