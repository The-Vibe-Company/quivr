#!/usr/bin/env python3
"""Inspect a repository's visible CI and local verification surface."""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path
from typing import Any


def read_text(path: Path, limit: int = 40000) -> str:
    try:
        return path.read_text(encoding="utf-8", errors="replace")[:limit]
    except OSError:
        return ""


def load_package(root: Path) -> dict[str, Any]:
    path = root / "package.json"
    if not path.exists():
        return {}
    try:
        return json.loads(read_text(path))
    except json.JSONDecodeError:
        return {"_parse_error": True}


def workflow_info(root: Path, path: Path) -> dict[str, Any]:
    text = read_text(path)
    return {
        "path": str(path.relative_to(root)),
        "has_pull_request": "pull_request" in text,
        "has_push": bool(re.search(r"(?m)^\s*push\s*:", text)),
        "has_schedule": "schedule:" in text or "- cron:" in text,
        "has_workflow_dispatch": "workflow_dispatch" in text,
        "has_paths_filter": "paths:" in text or "paths-ignore:" in text,
        "has_concurrency": "concurrency:" in text,
        "has_permissions": "permissions:" in text,
        "mentions_cache": "cache" in text.lower(),
        "job_count_guess": len(re.findall(r"(?m)^  [A-Za-z0-9_-]+:\s*$", text)),
    }


def discover_workflows(root: Path) -> list[dict[str, Any]]:
    workflows = sorted((root / ".github" / "workflows").glob("*.yml")) + sorted(
        (root / ".github" / "workflows").glob("*.yaml")
    )
    return [workflow_info(root, path) for path in workflows]


def build_recommendations(workflows: list[dict[str, Any]], package: dict[str, Any], root: Path) -> list[str]:
    recommendations: list[str] = []
    scripts = package.get("scripts", {}) if isinstance(package.get("scripts"), dict) else {}
    has_fast_script = any(name in scripts for name in ("test", "lint", "typecheck", "check", "build"))

    if not workflows:
        if has_fast_script:
            recommendations.append(
                "No GitHub Actions workflows detected, but local verification scripts exist. "
                "Consider a lightweight PR workflow if the project is shared or regressions repeat."
            )
        else:
            recommendations.append(
                "No CI workflows or obvious package scripts detected. Start with a local preflight command before adding CI."
            )
    else:
        if not any(item["has_pull_request"] for item in workflows):
            recommendations.append("No pull_request trigger detected; PR regressions may reach review late.")
        if not any(item["has_paths_filter"] for item in workflows):
            recommendations.append("No path filters detected; consider them for expensive checks in larger or private repositories.")
        if not all(item["has_permissions"] for item in workflows):
            recommendations.append("Some workflows lack explicit permissions; consider least-privilege permissions.")
        if not any(item["mentions_cache"] for item in workflows):
            recommendations.append("No dependency cache signal detected; caching may reduce CI time for dependency-heavy projects.")

    package_private = package.get("private")
    if package_private is True:
        recommendations.append(
            "package.json has private=true. Treat CI recommendations as budget-aware unless repository hosting proves it is public."
        )

    if not recommendations:
        recommendations.append("No obvious CI surface issue detected.")
    return recommendations


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cwd", default=".", help="Repository root to inspect.")
    parser.add_argument("--output", help="Optional JSON output path.")
    args = parser.parse_args()

    root = Path(args.cwd).resolve()
    package = load_package(root)
    workflows = discover_workflows(root)
    scripts = package.get("scripts", {}) if isinstance(package.get("scripts"), dict) else {}
    result = {
        "root": str(root),
        "package_private_flag": package.get("private") if package else None,
        "package_scripts": sorted(scripts.keys()),
        "workflow_count": len(workflows),
        "workflows": workflows,
        "recommendations": build_recommendations(workflows, package, root),
    }
    payload = json.dumps(result, indent=2, sort_keys=True)
    if args.output:
        Path(args.output).write_text(payload + "\n", encoding="utf-8")
    else:
        print(payload)


if __name__ == "__main__":
    main()
