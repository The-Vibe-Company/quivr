#!/usr/bin/env python3
"""Collect git context for a local code review."""

from __future__ import annotations

import argparse
from fnmatch import fnmatchcase
import json
import os
import re
import stat
import subprocess
import sys
from pathlib import Path
from typing import Any


DEFAULT_MAX_DIFF_BYTES = 300_000
DEFAULT_MAX_UNTRACKED_TOTAL_BYTES = 80_000
DEFAULT_MAX_UNTRACKED_FILE_BYTES = 20_000
SECRET_FILENAME_PATTERNS = (
    ".env",
    ".env.*",
    "*.env",
    "*.env.*",
    ".npmrc",
    ".pypirc",
    ".netrc",
    "*credentials*.json",
    "*secret*",
    "*.pem",
    "*.key",
    "*.p12",
    "*.pfx",
    "id_rsa",
    "id_dsa",
    "id_ecdsa",
    "id_ed25519",
)
SECRET_ASSIGNMENT_RE = re.compile(
    r"(?i)(\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|"
    r"client[_-]?secret|private[_-]?key|secret|password|passwd|authorization)\b"
    r"\s*[:=]\s*[\"']?)([^\"'\s#]+)"
)
BEARER_RE = re.compile(r"(?i)(\bbearer\s+)([A-Za-z0-9._~+/=-]{8,})")
SECRET_VALUE_RE = re.compile(
    r"(?i)(AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9_]{20,}|"
    r"xox[baprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,})"
)


def run(cmd: list[str], cwd: Path, timeout: int = 60) -> dict[str, Any]:
    try:
        proc = subprocess.run(
            cmd,
            cwd=str(cwd),
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
        )
        return {
            "cmd": cmd,
            "exit_code": proc.returncode,
            "stdout": proc.stdout,
            "stderr": proc.stderr,
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "cmd": cmd,
            "exit_code": 124,
            "stdout": exc.stdout or "",
            "stderr": f"Timed out after {timeout}s",
        }


def git(args: list[str], cwd: Path, timeout: int = 60) -> dict[str, Any]:
    return run(["git", *args], cwd, timeout=timeout)


def require_ok(result: dict[str, Any]) -> str:
    if result["exit_code"] != 0:
        cmd = " ".join(result["cmd"])
        stderr = result.get("stderr", "").strip()
        raise SystemExit(f"{cmd} failed: {stderr}")
    return result.get("stdout", "")


def truncate_text(text: str, max_bytes: int) -> dict[str, Any]:
    raw = text.encode("utf-8", errors="replace")
    if len(raw) <= max_bytes:
        return {"text": text, "truncated": False, "byte_length": len(raw)}
    clipped = raw[:max_bytes].decode("utf-8", errors="replace")
    return {"text": clipped, "truncated": True, "byte_length": len(raw)}


def parse_status_entries(status_z: str) -> list[dict[str, str]]:
    entries: list[dict[str, str]] = []
    parts = status_z.split("\x00")
    index = 0
    while index < len(parts):
        item = parts[index]
        index += 1
        if not item or len(item) < 3:
            continue

        xy = item[:2]
        path = item[3:] if item[2:3] == " " else item[2:].lstrip()
        entry = {"xy": xy, "path": path}
        if ("R" in xy or "C" in xy) and index < len(parts):
            old_path = parts[index]
            index += 1
            if old_path:
                entry["old_path"] = old_path
        entries.append(entry)
    return entries


def format_status_entries(entries: list[dict[str, str]]) -> str:
    lines: list[str] = []
    for entry in entries:
        xy = entry["xy"]
        path = entry["path"]
        old_path = entry.get("old_path")
        if old_path:
            lines.append(f"{xy} {old_path} -> {path}")
        else:
            lines.append(f"{xy} {path}")
    return "\n".join(lines) + ("\n" if lines else "")


def parse_status_files(entries: list[dict[str, str]]) -> list[str]:
    files: list[str] = []
    for entry in entries:
        path = entry["path"]
        if path and path not in files:
            files.append(path)
    return files


def parse_untracked_files(entries: list[dict[str, str]]) -> list[str]:
    files: list[str] = []
    for entry in entries:
        if entry["xy"] != "??":
            continue
        path = entry["path"]
        if path and path not in files:
            files.append(path)
    return files


def split_names(text: str) -> list[str]:
    names: list[str] = []
    for line in text.splitlines():
        item = line.strip()
        if item and item not in names:
            names.append(item)
    return names


def detect_repo(cwd: Path) -> Path:
    result = git(["rev-parse", "--show-toplevel"], cwd)
    return Path(require_ok(result).strip())


def current_branch(repo: Path) -> str:
    result = git(["rev-parse", "--abbrev-ref", "HEAD"], repo)
    if result["exit_code"] == 0:
        return result["stdout"].strip()
    return ""


def github_repo_from_origin(repo: Path) -> dict[str, str] | None:
    result = git(["remote", "get-url", "origin"], repo)
    if result["exit_code"] != 0:
        return None
    remote = result["stdout"].strip()
    match = re.search(r"github\.com[:/]([^/]+)/(.+?)(?:\.git)?$", remote, re.IGNORECASE)
    if not match:
        return None
    return {"owner": match.group(1), "repo": match.group(2)}


def detect_base_branch(repo: Path) -> str:
    upstream = git(["rev-parse", "--abbrev-ref", "@{upstream}"], repo)
    if upstream["exit_code"] == 0:
        branch = upstream["stdout"].strip()
        if branch.startswith("origin/"):
            return branch.removeprefix("origin/")
        if branch:
            return branch
    default = git(["symbolic-ref", "refs/remotes/origin/HEAD"], repo)
    if default["exit_code"] == 0:
        ref = default["stdout"].strip()
        if ref.startswith("refs/remotes/origin/"):
            return ref.removeprefix("refs/remotes/origin/")
    return "main"


def resolve_diff_ref(repo: Path, base: str) -> str:
    if "/" in base or base.startswith("refs/"):
        return base
    origin_ref = f"origin/{base}"
    verify = git(["rev-parse", "--verify", "--quiet", origin_ref], repo)
    if verify["exit_code"] == 0:
        return origin_ref
    return base


def git_status_entries(repo: Path) -> list[dict[str, str]]:
    status_z = require_ok(git(["status", "--porcelain=v1", "-z", "-uall"], repo))
    return parse_status_entries(status_z)


def is_secret_like_path(name: str) -> bool:
    lowered = name.lower()
    basename = Path(name).name.lower()
    for pattern in SECRET_FILENAME_PATTERNS:
        lowered_pattern = pattern.lower()
        if fnmatchcase(lowered, lowered_pattern) or fnmatchcase(basename, lowered_pattern):
            return True
    return False


def redact_secret_text(text: str) -> str:
    text = SECRET_ASSIGNMENT_RE.sub(lambda match: f"{match.group(1)}<redacted>", text)
    text = BEARER_RE.sub(lambda match: f"{match.group(1)}<redacted>", text)
    text = SECRET_VALUE_RE.sub("<redacted-secret>", text)
    return text


def collect_untracked_previews(repo: Path, files: list[str]) -> list[dict[str, Any]]:
    previews: list[dict[str, Any]] = []
    total = 0
    repo_root = repo.resolve()
    for name in files:
        item: dict[str, Any] = {"path": name}
        try:
            raw_path = repo_root / name
            path = raw_path.resolve()
            path.relative_to(repo_root)
            st = raw_path.lstat()
        except (FileNotFoundError, OSError, ValueError) as exc:
            item["error"] = str(exc)
            previews.append(item)
            continue

        mode = st.st_mode
        if stat.S_ISLNK(mode):
            item["type"] = "symlink"
            try:
                item["target"] = os.readlink(raw_path)
            except OSError as exc:
                item["error"] = str(exc)
            previews.append(item)
            continue
        if not stat.S_ISREG(mode):
            item["type"] = "non-file"
            previews.append(item)
            continue
        item["byte_length"] = st.st_size
        if is_secret_like_path(name):
            item["skipped"] = "secret-like filename"
            previews.append(item)
            continue
        if total >= DEFAULT_MAX_UNTRACKED_TOTAL_BYTES:
            item["skipped"] = "untracked preview byte limit reached"
            previews.append(item)
            continue

        limit = min(DEFAULT_MAX_UNTRACKED_FILE_BYTES, DEFAULT_MAX_UNTRACKED_TOTAL_BYTES - total)
        try:
            data = raw_path.read_bytes()
        except OSError as exc:
            item["error"] = str(exc)
            previews.append(item)
            continue

        item["byte_length"] = len(data)
        if b"\x00" in data[: min(len(data), limit)]:
            item["type"] = "binary"
            previews.append(item)
            continue

        clipped = data[:limit]
        item["text"] = redact_secret_text(clipped.decode("utf-8", errors="replace"))
        item["truncated"] = len(data) > limit
        total += len(clipped)
        previews.append(item)
    return previews


def collect_worktree_state(
    repo: Path,
    max_diff_bytes: int,
    entries: list[dict[str, str]] | None = None,
) -> dict[str, Any]:
    status_entries = entries if entries is not None else git_status_entries(repo)
    untracked_names = parse_untracked_files(status_entries)
    return {
        "status_porcelain": format_status_entries(status_entries),
        "worktree_changed_files": parse_status_files(status_entries),
        "untracked_files": untracked_names,
        "untracked_file_previews": collect_untracked_previews(repo, untracked_names),
        "staged_diff": truncate_text(require_ok(git(["diff", "--cached"], repo, timeout=120)), max_diff_bytes),
    }


def collect_uncommitted(
    repo: Path,
    max_diff_bytes: int,
    worktree: dict[str, Any] | None = None,
) -> dict[str, Any]:
    worktree_state = worktree if worktree is not None else collect_worktree_state(repo, max_diff_bytes)
    unstaged_names = split_names(require_ok(git(["diff", "--name-only"], repo)))
    staged_names = split_names(require_ok(git(["diff", "--cached", "--name-only"], repo)))
    changed = []
    for name in [*unstaged_names, *staged_names, *worktree_state["worktree_changed_files"]]:
        if name not in changed:
            changed.append(name)
    result = {
        "mode": "uncommitted",
        "changed_files": changed,
        "diff": truncate_text(require_ok(git(["diff"], repo, timeout=120)), max_diff_bytes),
    }
    result.update(worktree_state)
    return result


def collect_base(repo: Path, base: str, max_diff_bytes: int) -> dict[str, Any]:
    diff_ref = resolve_diff_ref(repo, base)
    return {
        "mode": "base",
        "base_branch": base,
        "diff_ref": diff_ref,
        "changed_files": split_names(require_ok(git(["diff", "--name-only", diff_ref], repo))),
        "diff_stat": require_ok(git(["diff", diff_ref, "--stat"], repo, timeout=120)),
        "diff": truncate_text(require_ok(git(["diff", diff_ref], repo, timeout=120)), max_diff_bytes),
    }


def collect_commit(repo: Path, commit: str, max_diff_bytes: int) -> dict[str, Any]:
    info = require_ok(git(["log", "-1", "--format=%H%x00%h%x00%s", commit], repo)).strip()
    full_hash, short_hash, subject = (info.split("\x00") + ["", "", ""])[:3]
    names = split_names(require_ok(git(["show", "--name-only", "--format=", commit], repo)))
    return {
        "mode": "commit",
        "commit": {"hash": full_hash, "short_hash": short_hash, "subject": subject},
        "changed_files": names,
        "diff_stat": require_ok(git(["show", "--stat", "--format=fuller", commit], repo, timeout=120)),
        "diff": truncate_text(require_ok(git(["show", "--format=fuller", "--patch", commit], repo, timeout=120)), max_diff_bytes),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=["auto", "uncommitted", "base", "commit", "custom"], default="auto")
    parser.add_argument("--base", help="Base branch or ref for base mode")
    parser.add_argument("--commit", help="Commit/ref for commit mode")
    parser.add_argument("--prompt", help="Custom review instruction, stored as metadata only")
    parser.add_argument("--cwd", default=".", help="Repository working directory")
    parser.add_argument("--max-diff-bytes", type=int, default=DEFAULT_MAX_DIFF_BYTES)
    parser.add_argument("--output", help="Write JSON to this path instead of stdout")
    args = parser.parse_args()

    cwd = Path(args.cwd).resolve()
    repo = detect_repo(cwd)
    status_entries = git_status_entries(repo)
    worktree = collect_worktree_state(repo, args.max_diff_bytes, status_entries)
    mode = args.mode
    if mode == "auto":
        if args.commit:
            mode = "commit"
        elif args.base is not None:
            mode = "base"
        elif worktree["status_porcelain"].strip():
            mode = "uncommitted"
        elif args.prompt:
            mode = "custom"
        else:
            mode = "base"

    payload: dict[str, Any] = {
        "repo_root": str(repo),
        "current_branch": current_branch(repo),
        "github_repo": github_repo_from_origin(repo),
        "requested_mode": args.mode,
        "custom_prompt": args.prompt or "",
        "collector": "review-code-dev/scripts/collect_review_context.py",
    }
    payload.update(worktree)

    if mode == "uncommitted":
        payload.update(collect_uncommitted(repo, args.max_diff_bytes, worktree))
    elif mode == "base":
        base = args.base or detect_base_branch(repo)
        payload.update(collect_base(repo, base, args.max_diff_bytes))
    elif mode == "commit":
        if not args.commit:
            raise SystemExit("--commit is required for commit mode")
        payload.update(collect_commit(repo, args.commit, args.max_diff_bytes))
    else:
        payload.update({
            "mode": "custom",
            "changed_files": worktree["worktree_changed_files"],
        })

    output = json.dumps(payload, indent=2, ensure_ascii=False)
    if args.output:
        Path(args.output).write_text(output + "\n", encoding="utf-8")
    else:
        print(output)
    return 0


if __name__ == "__main__":
    sys.exit(main())
