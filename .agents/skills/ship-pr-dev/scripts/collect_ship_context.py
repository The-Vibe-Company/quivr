#!/usr/bin/env python3
"""Collect deterministic branch context for ship-pr-dev."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
from pathlib import Path


FRONTEND_RE = re.compile(
    r"(\.(tsx|jsx|vue|svelte|css|scss|less)$|(^|/)(components|pages|routes|app|styles|theme|tokens|assets)/)",
    re.IGNORECASE,
)
BACKEND_RE = re.compile(
    r"(\.(py|rb|go|rs|java|kt|php|cs)$|(^|/)(api|server|backend|services|jobs|workers|controllers|models|routes)/)",
    re.IGNORECASE,
)
DB_RE = re.compile(r"(^|/)(migrations?|schema|prisma|db)/|schema\.(sql|rb|prisma)$", re.IGNORECASE)
SECURITY_RE = re.compile(
    r"(auth|permission|policy|role|token|secret|privacy|pii|billing|export|upload|download)",
    re.IGNORECASE,
)
AGENT_SKILL_RE = re.compile(r"(^|/)(\.agents|\.claude|\.codex)/skills/", re.IGNORECASE)
MANIFEST_NAMES = {
    "package.json",
    "pnpm-lock.yaml",
    "yarn.lock",
    "package-lock.json",
    "pyproject.toml",
    "requirements.txt",
    "Cargo.toml",
    "go.mod",
    "Gemfile",
    "mix.exs",
    "pom.xml",
    "build.gradle",
    "Makefile",
}
CI_PARTS = {".github/workflows", ".gitlab-ci.yml", "circle.yml", ".circleci", "azure-pipelines.yml"}


def run(cwd: Path, args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        args,
        cwd=str(cwd),
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )


def git(cwd: Path, args: list[str]) -> subprocess.CompletedProcess[str]:
    return run(cwd, ["git", *args])


def lines(text: str) -> list[str]:
    return [line for line in text.splitlines() if line.strip()]


def detect_base(repo_root: Path) -> str:
    origin_head = git(repo_root, ["symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"])
    if origin_head.returncode == 0 and origin_head.stdout.strip():
        return origin_head.stdout.strip()

    for candidate in ("origin/main", "origin/master", "main", "master"):
        if git(repo_root, ["rev-parse", "--verify", "--quiet", candidate]).returncode == 0:
            return candidate
    return "origin/main"


def parse_name_status(output: str) -> list[dict[str, str]]:
    changed = []
    for line in lines(output):
        parts = line.split("\t")
        if len(parts) >= 2:
            path = parts[-1]
            changed.append({"status": parts[0], "path": path})
    return changed


def merge_changed_files(*groups: list[dict[str, str]]) -> list[dict[str, str]]:
    """Combine branch, staged, unstaged, and untracked paths without hiding local work."""
    merged: dict[str, dict[str, str]] = {}
    for group in groups:
        for item in group:
            path = item["path"]
            existing = merged.get(path)
            if existing is None:
                merged[path] = dict(item)
            elif item["status"] not in existing["status"].split("+"):
                existing["status"] = f'{existing["status"]}+{item["status"]}'
    return [merged[path] for path in sorted(merged)]


def parse_untracked_status(output: str) -> list[dict[str, str]]:
    """Keep Git's collapsed untracked directory entries instead of expanding every file."""
    return [
        {"status": "??", "path": line[3:]}
        for line in output.splitlines()
        if line.startswith("?? ") and line[3:].strip()
    ]


def classify_impact(paths: list[str]) -> dict[str, bool]:
    """Classify application risk without treating agent-package internals as product code."""
    agent_paths = [path for path in paths if AGENT_SKILL_RE.search(path)]
    application_paths = [path for path in paths if not AGENT_SKILL_RE.search(path)]
    return {
        "agent_workflow": bool(agent_paths),
        "frontend": any(FRONTEND_RE.search(path) for path in application_paths),
        "backend": any(BACKEND_RE.search(path) for path in application_paths),
        "db": any(DB_RE.search(path) for path in application_paths),
        "security_or_privacy": any(SECURITY_RE.search(path) for path in application_paths),
        "docs_only": bool(application_paths)
        and all(
            re.search(r"(\.md$|^docs/|^README|^CHANGELOG)", path, re.IGNORECASE)
            for path in application_paths
        ),
    }


def find_project_files(repo_root: Path) -> tuple[list[str], list[str]]:
    manifests: list[str] = []
    ci_files: list[str] = []
    skip = {
        ".git",
        ".kilocode",
        ".next",
        ".venv",
        "__pycache__",
        "build",
        "dist",
        "node_modules",
        "output",
        "outputs",
        "plans",
        "tmp",
        "vendor",
    }
    for path in repo_root.rglob("*"):
        if any(part in skip for part in path.relative_to(repo_root).parts):
            continue
        rel = str(path.relative_to(repo_root))
        if path.is_file() and path.name in MANIFEST_NAMES:
            manifests.append(rel)
        if path.is_file() and (
            any(rel.startswith(part) for part in CI_PARTS if "/" in part)
            or rel in CI_PARTS
        ):
            ci_files.append(rel)
    return sorted(manifests), sorted(ci_files)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cwd", default=".")
    parser.add_argument("--base")
    parser.add_argument("--output")
    args = parser.parse_args()

    cwd = Path(args.cwd).resolve()
    root = git(cwd, ["rev-parse", "--show-toplevel"])
    if root.returncode != 0:
        raise SystemExit("ship-pr-dev requires a Git repository")
    repo_root = Path(root.stdout.strip()).resolve()

    branch = git(repo_root, ["branch", "--show-current"]).stdout.strip() or "DETACHED"
    base = args.base or detect_base(repo_root)
    merge_base_result = git(repo_root, ["merge-base", base, "HEAD"])
    merge_base = merge_base_result.stdout.strip() if merge_base_result.returncode == 0 else None
    diff_range = f"{base}...HEAD"

    name_status = git(repo_root, ["diff", "--name-status", diff_range])
    staged_status = git(repo_root, ["diff", "--cached", "--name-status"])
    unstaged_status = git(repo_root, ["diff", "--name-status"])
    branch_stat = git(repo_root, ["diff", "--stat", diff_range])
    local_stat = git(repo_root, ["diff", "--stat", "HEAD"])
    status = git(repo_root, ["status", "--short"])
    untracked_files = parse_untracked_status(status.stdout)
    changed_files = merge_changed_files(
        parse_name_status(name_status.stdout),
        parse_name_status(staged_status.stdout),
        parse_name_status(unstaged_status.stdout),
        untracked_files,
    )
    paths = [item["path"] for item in changed_files]
    manifests, ci_files = find_project_files(repo_root)

    context = {
        "repo_root": str(repo_root),
        "branch": branch,
        "base": base,
        "merge_base": merge_base,
        "diff_range": diff_range,
        "git_status_short": lines(status.stdout),
        "diff_stat": "\n".join(
            part for part in (branch_stat.stdout.strip(), local_stat.stdout.strip()) if part
        ),
        "changed_files": changed_files,
        "impact": classify_impact(paths),
        "project_files": {
            "manifests": manifests,
            "ci": ci_files,
        },
    }

    output = json.dumps(context, indent=2, sort_keys=True) + "\n"
    if args.output:
        Path(args.output).write_text(output, encoding="utf-8")
    print(output, end="")


if __name__ == "__main__":
    main()
