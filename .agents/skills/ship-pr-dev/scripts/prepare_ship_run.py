#!/usr/bin/env python3
"""Prepare a non-committable artifact directory for ship-pr-dev."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path


def run_git(cwd: Path, args: list[str], check: bool = False) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args],
        cwd=str(cwd),
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=check,
    )


def slugify(value: str) -> str:
    value = re.sub(r"[^a-zA-Z0-9._-]+", "-", value.strip())
    value = value.strip("-._").lower()
    return value or "repo"


def ensure_git_ignore(repo_root: Path) -> None:
    git_dir = run_git(repo_root, ["rev-parse", "--git-dir"], check=True).stdout.strip()
    git_path = Path(git_dir)
    if not git_path.is_absolute():
        git_path = repo_root / git_path
    exclude_path = git_path / "info" / "exclude"
    exclude_path.parent.mkdir(parents=True, exist_ok=True)
    exclude = exclude_path.read_text(encoding="utf-8") if exclude_path.exists() else ""
    ignore_line = "/plans/ship-pr-dev/"
    if ignore_line not in exclude.splitlines():
        with exclude_path.open("a", encoding="utf-8") as handle:
            if exclude and not exclude.endswith("\n"):
                handle.write("\n")
            handle.write(f"{ignore_line}\n")

    check_ignore = run_git(repo_root, ["check-ignore", "-q", "plans/ship-pr-dev/"])
    if check_ignore.returncode != 0:
        raise SystemExit("plans/ship-pr-dev/ is not ignored by Git; refusing to write artifacts")

    tracked = run_git(repo_root, ["ls-files", "plans/ship-pr-dev/"]).stdout.strip()
    if tracked:
        raise SystemExit("plans/ship-pr-dev/ is tracked by Git; refusing to write artifacts")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cwd", default=".")
    args = parser.parse_args()

    cwd = Path(args.cwd).resolve()
    root_result = run_git(cwd, ["rev-parse", "--show-toplevel"])
    if root_result.returncode != 0:
        raise SystemExit("ship-pr-dev requires a Git repository")

    repo_root = Path(root_result.stdout.strip()).resolve()
    ensure_git_ignore(repo_root)

    timestamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    repo_slug = slugify(repo_root.name)
    run_dir = repo_root / "plans" / "ship-pr-dev" / "runs" / f"{timestamp}-{repo_slug}"
    run_dir.mkdir(parents=True, exist_ok=False)

    metadata = {
        "skill": "ship-pr-dev",
        "timestamp_utc": timestamp,
        "cwd": str(cwd),
        "repo_root": str(repo_root),
        "repo_slug": repo_slug,
        "run_dir": str(run_dir),
        "non_committable": True,
        "git_ignore_rule": "/plans/ship-pr-dev/",
    }
    (run_dir / "run-metadata.json").write_text(
        json.dumps(metadata, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(metadata))


if __name__ == "__main__":
    main()
