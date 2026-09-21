#!/usr/bin/env python3
"""Prepare a non-committable artifact directory for Review Code."""

from __future__ import annotations

import argparse
from datetime import datetime
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any


ARTIFACT_ROOT = Path("plans/review-code-dev")
EXCLUDE_MARKER = "# Review Code artifacts"
EXCLUDE_LINES = ("/plans/review-code-dev/",)


def run(cmd: list[str], cwd: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        cmd,
        cwd=str(cwd),
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )


def require_ok(result: subprocess.CompletedProcess[str]) -> str:
    if result.returncode != 0:
        raise SystemExit(result.stderr.strip() or f"{result.args} failed")
    return result.stdout


def slugify(value: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9._-]+", "-", value.strip().lower())
    slug = re.sub(r"-+", "-", slug).strip("-._")
    return slug or "repo"


def detect_repo(cwd: Path) -> Path | None:
    result = run(["git", "rev-parse", "--show-toplevel"], cwd)
    if result.returncode != 0:
        return None
    return Path(result.stdout.strip()).resolve()


def git_path(repo: Path, path: str) -> Path:
    result = run(["git", "rev-parse", "--git-path", path], repo)
    return Path(require_ok(result).strip()).resolve()


def tracked_artifacts(repo: Path) -> list[str]:
    result = run(["git", "ls-files", "--", ARTIFACT_ROOT.as_posix()], repo)
    if result.returncode != 0:
        raise SystemExit(result.stderr.strip() or "git ls-files failed")
    return [line for line in result.stdout.splitlines() if line.strip()]


def ensure_local_exclude(repo: Path) -> dict[str, Any]:
    exclude_path = git_path(repo, "info/exclude")
    exclude_path.parent.mkdir(parents=True, exist_ok=True)
    before = exclude_path.read_text(encoding="utf-8") if exclude_path.exists() else ""

    missing = [line for line in EXCLUDE_LINES if line not in before.splitlines()]
    added = False
    if missing:
        suffix = "" if before.endswith("\n") or not before else "\n"
        block = suffix + EXCLUDE_MARKER + "\n" + "\n".join(missing) + "\n"
        exclude_path.write_text(before + block, encoding="utf-8")
        added = True

    probe = ARTIFACT_ROOT / ".review-code-dev-ignore-check"
    check = run(["git", "check-ignore", "-q", "--", probe.as_posix()], repo)
    return {
        "path": str(exclude_path),
        "added": added,
        "verified": check.returncode == 0,
        "probe": probe.as_posix(),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cwd", default=".", help="Repository working directory")
    parser.add_argument("--repo-slug", help="Slug to use in the run directory name")
    parser.add_argument("--timestamp", help="YYYYMMDD-HHMMSS timestamp override")
    parser.add_argument("--output", help="Write metadata JSON to this path")
    args = parser.parse_args()

    cwd = Path(args.cwd).resolve()
    repo = detect_repo(cwd)
    if repo is None:
        raise SystemExit("Review Code requires a Git repository for non-committable artifacts.")

    tracked = tracked_artifacts(repo)
    if tracked:
        raise SystemExit(
            "Refusing to write artifacts because plans/review-code-dev is already tracked by Git: "
            + ", ".join(tracked[:10])
        )

    exclude = ensure_local_exclude(repo)
    if not exclude["verified"]:
        raise SystemExit(
            "Refusing to write artifacts because Git ignore verification failed for "
            + exclude["probe"]
        )

    timestamp = args.timestamp or datetime.now().strftime("%Y%m%d-%H%M%S")
    repo_slug = slugify(args.repo_slug or repo.name)
    run_dir = repo / ARTIFACT_ROOT / "runs" / f"{timestamp}-{repo_slug}"
    run_dir.mkdir(parents=True, exist_ok=False)

    rel_run_dir = run_dir.relative_to(repo).as_posix()
    metadata = {
        "repo_root": str(repo),
        "run_dir": str(run_dir),
        "run_dir_relative": rel_run_dir,
        "artifact_root": ARTIFACT_ROOT.as_posix(),
        "git_exclude": exclude,
        "tracked_artifacts": tracked,
        "non_committable": True,
    }

    metadata_path = Path(args.output).resolve() if args.output else run_dir / "run-metadata.json"
    metadata_path.parent.mkdir(parents=True, exist_ok=True)
    metadata_path.write_text(json.dumps(metadata, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(json.dumps(metadata, indent=2, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
