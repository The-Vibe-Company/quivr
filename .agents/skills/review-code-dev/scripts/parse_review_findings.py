#!/usr/bin/env python3
"""Parse P0-P3 markdown review findings into JSON."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any


HEADER_RE = re.compile(r"^\s*\*{0,2}\[?P([0-3])\]?\s+(.+?)\s+[-\u2013\u2014]\s+(.+?)\*{0,2}\s*$")
LOCATION_RE = re.compile(r"^(.*):(\d+)(?:-\d+)?$")


def parse(text: str) -> list[dict[str, Any]]:
    issues: list[dict[str, Any]] = []
    current: dict[str, Any] | None = None
    desc_lines: list[str] = []

    def flush() -> None:
        nonlocal current, desc_lines
        if not current:
            return
        current["description"] = "\n".join(line.rstrip() for line in desc_lines).strip()
        key = (current["file"], current.get("line"), current["title"])
        if not any((i["file"], i.get("line"), i["title"]) == key for i in issues):
            issues.append(current)
        current = None
        desc_lines = []

    for raw_line in text.splitlines():
        line = raw_line.strip()
        normalized = line[2:-2] if line.startswith("**") and line.endswith("**") else line
        match = HEADER_RE.match(normalized)
        if match:
            flush()
            priority, location, title = match.groups()
            location = location.strip()
            loc_match = LOCATION_RE.match(location)
            file_name = loc_match.group(1).strip() if loc_match else location
            line_no = int(loc_match.group(2)) if loc_match else None
            current = {
                "priority": f"P{priority}",
                "file": file_name,
                "line": line_no,
                "title": title.strip(),
            }
            continue
        if current is not None:
            desc_lines.append(raw_line)

    flush()
    return issues


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("review", nargs="?", help="Markdown review file. Reads stdin when omitted.")
    parser.add_argument("--output", help="Write JSON to this path instead of stdout.")
    parser.add_argument("--fail-on-issues", action="store_true", help="Exit 1 when one or more issues are found.")
    args = parser.parse_args()

    if args.review:
        text = Path(args.review).read_text(encoding="utf-8")
    else:
        text = sys.stdin.read()

    payload = {"issues": parse(text)}
    rendered = json.dumps(payload, indent=2, ensure_ascii=False)
    if args.output:
        Path(args.output).write_text(rendered + "\n", encoding="utf-8")
    else:
        print(rendered)
    return 1 if args.fail_on_issues and payload["issues"] else 0


if __name__ == "__main__":
    sys.exit(main())
