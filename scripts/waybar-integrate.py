#!/usr/bin/env python3
"""Add the xray module to an existing Waybar JSON/JSONC config without reformatting it."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile
from datetime import datetime

MODULE = "custom/xray"
HAPP = "custom/happ"
MARKER = "/* xray-waybar-ctl: managed block */"
STYLE = '''/* xray-waybar-ctl: managed block */
#custom-xray {
  padding: 0 10px;
  border-radius: 10px;
  color: #a78bfa;
  font-family: "Symbols Nerd Font", "JetBrainsMono Nerd Font", sans-serif;
  background: rgba(167, 139, 250, 0.10);
  transition: color 150ms ease, background 150ms ease;
}
#custom-xray:hover { background: rgba(167, 139, 250, 0.22); }
#custom-xray.connected { color: #72d7a6; }
#custom-xray.loading { color: #f2c879; }
#custom-xray.error, #custom-xray.degraded { color: #f18698; }
#custom-xray.disconnected { color: #a78bfa; }
/* end xray-waybar-ctl */
'''
TOKEN = re.compile(r'"(?:\\.|[^"\\])*"|//[^\n]*|/\*[\s\S]*?\*/|[{}\[\]:,]')


def tokens(source):
    return [(m.group(), m.start(), m.end()) for m in TOKEN.finditer(source)
            if not m.group().startswith(("//", "/*"))]


def jsonc_value(source):
    # Mask comments without changing offsets; then drop trailing commas.
    masked = list(source)
    for m in TOKEN.finditer(source):
        if m.group().startswith(("//", "/*")):
            masked[m.start():m.end()] = ["\n" if c == "\n" else " " for c in m.group()]
    ts = tokens(source)
    for current, following in zip(ts, ts[1:]):
        if current[0] == "," and following[0] in ("}", "]"):
            masked[current[1]] = " "
    return json.loads("".join(masked))


def closing(ts, start):
    pairs = {"{": "}", "[": "]"}
    stack = []
    for i in range(start, len(ts)):
        val = ts[i][0]
        if val in pairs:
            stack.append(pairs[val])
        elif val in ("}", "]"):
            if not stack or stack.pop() != val:
                raise ValueError("unbalanced Waybar config")
            if not stack:
                return i
    raise ValueError("unterminated Waybar config")


def root_properties(ts):
    if not ts or ts[0][0] != "{":
        raise ValueError("Waybar config must be a single JSON/JSONC object")
    end = closing(ts, 0)
    if end != len(ts) - 1:
        raise ValueError("Waybar config must be a single JSON/JSONC object")
    result = {}
    i = 1
    while i < end:
        if ts[i][0] == ",":
            i += 1
            continue
        key = json.loads(ts[i][0])
        if ts[i + 1][0] != ":":
            raise ValueError("invalid Waybar property")
        value = i + 2
        result[key] = value
        if ts[value][0] in ("[", "{"):
            i = closing(ts, value) + 1
        else:
            i = value + 1
    return result


def module_config(binary):
    return {
        "exec": f"{binary} status",
        "return-type": "json",
        "interval": 5,
        "format": "{}",
        "on-click": f"{binary} toggle",
        "on-click-right": f"{binary} menu",
        "on-click-middle": f"{binary} reconnect",
        "on-scroll-up": f"{binary} use-next",
        "on-scroll-down": f"{binary} use-prev",
    }


def update_config(source, binary, replace_happ):
    parsed = jsonc_value(source)
    if not isinstance(parsed, dict):
        raise ValueError("Waybar config must be a single JSON/JSONC object")
    ts = tokens(source)
    props = root_properties(ts)
    edits = []
    root_additions = []
    module_lists = [name for name in ("modules-left", "modules-center", "modules-right")
                    if isinstance(parsed.get(name), list)]
    has_module = any(MODULE in parsed[name] for name in module_lists)
    if replace_happ:
        for name in module_lists:
            if HAPP not in parsed[name]:
                continue
            arr = props[name]
            end = closing(ts, arr)
            for i in range(arr + 1, end):
                token, start, stop = ts[i]
                if token != json.dumps(HAPP):
                    continue
                if not has_module:
                    edits.append((start, stop, json.dumps(MODULE)))
                    has_module = True
                elif i + 1 < end and ts[i + 1][0] == ",":
                    edits.append((start, ts[i + 1][2], ""))
                elif i > arr + 1 and ts[i - 1][0] == ",":
                    edits.append((ts[i - 1][1], stop, ""))
                else:
                    edits.append((start, stop, ""))
    if not has_module:
        if "modules-left" in props:
            arr = props["modules-left"]
            if ts[arr][0] != "[":
                raise ValueError("modules-left is not an array")
            end = closing(ts, arr)
            nonempty = end > arr + 1
            edits.append((ts[arr][2], ts[arr][2], json.dumps(MODULE) + (", " if nonempty else "")))
        else:
            root_additions.append('"modules-left": ["custom/xray"]')
    if MODULE not in props:
        content = json.dumps(module_config(binary), ensure_ascii=False, indent=2)
        content = content.replace("\n", "\n  ")
        root_additions.insert(0, f'"{MODULE}": {content}')
    else:
        existing = parsed.get(MODULE)
        if not isinstance(existing, dict):
            raise ValueError(f"{MODULE} exists but is not an object")
        print(f"Keeping existing {MODULE} settings", file=sys.stderr)
    if root_additions:
        insertion = "\n  " + ",\n  ".join(root_additions) + ("," if props else "") + "\n"
        edits.append((ts[0][2], ts[0][2], insertion))
    for start, stop, replacement in sorted(edits, key=lambda e: e[0], reverse=True):
        source = source[:start] + replacement + source[stop:]
    jsonc_value(source)  # refuse to write broken syntax
    return source


def write_changed(path, content, check):
    old = path.read_text() if path.exists() else None
    if old == content:
        return False
    if check:
        return True
    path.parent.mkdir(parents=True, exist_ok=True)
    if old is not None:
        stamp = datetime.now().strftime("%Y%m%d-%H%M%S-%f")
        backup = path.with_name(path.name + ".xray-waybar-backup-" + stamp)
        shutil.copy2(path, backup)
        print(f"Backup: {backup}")
    fd, tmp = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as out:
            out.write(content)
        if old is not None:
            os.chmod(tmp, path.stat().st_mode & 0o777)
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)
    return True


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--home", type=Path, required=True)
    p.add_argument("--binary", type=Path, required=True)
    p.add_argument("--replace-happ", action="store_true")
    p.add_argument("--check", action="store_true")
    args = p.parse_args()
    waybar = Path(os.environ.get("XDG_CONFIG_HOME", str(args.home / ".config"))) / "waybar"
    config = next((waybar / name for name in ("config", "config.jsonc")
                   if (waybar / name).exists() or (waybar / name).is_symlink()), waybar / "config.jsonc")
    if config.is_symlink():
        config = config.resolve(strict=True)
    source = config.read_text() if config.exists() else "{}\n"
    new_config = update_config(source, str(args.binary), args.replace_happ)
    style = waybar / "style.css"
    if style.is_symlink():
        style = style.resolve(strict=True)
    current_style = style.read_text() if style.exists() else ""
    if MARKER in current_style:
        # Keep user modifications to our marked CSS block.
        new_style = current_style
    else:
        new_style = current_style.rstrip() + ("\n\n" if current_style.strip() else "") + STYLE
    config_changed = write_changed(config, new_config, args.check)
    style_changed = write_changed(style, new_style, args.check)
    if not args.check:
        print(f"Waybar: {config} ({'updated' if config_changed else 'unchanged'})")
        print(f"Style: {style} ({'updated' if style_changed else 'unchanged'})")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, json.JSONDecodeError) as exc:
        print(f"Waybar integration failed: {exc}", file=sys.stderr)
        sys.exit(1)
