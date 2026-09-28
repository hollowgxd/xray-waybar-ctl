#!/usr/bin/env python3
"""Print a newly published stable Xray version, or nothing if the pin is current."""

import json
import re
from pathlib import Path
import sys


def main(pin_path, release_path):
    pin = json.loads(Path(pin_path).read_text())
    release = json.loads(Path(release_path).read_text())
    if release.get("draft") or release.get("prerelease"):
        return
    tag = release["tag_name"]
    if not re.fullmatch(r"v\d+\.\d+\.\d+", tag):
        raise ValueError(f"unexpected Xray tag: {tag}")
    if tuple(map(int, tag[1:].split("."))) > tuple(map(int, pin["version"][1:].split("."))):
        print(tag)


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
