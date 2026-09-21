#!/usr/bin/env python3
"""Inspect project-level agent guidance files.

This script is intentionally heuristic and dependency-free. It helps an agent
see whether a repository has shared instructions, Claude adapters, symlinks,
imports, and adjacent assistant rule files.
"""

from __future__ import annotations

import argparse
import json
import os
import re
from pathlib import Path
from typing import Any


SKIP_DIRS = {
    ".git",
    "node_modules",
    ".next",
    "dist",
    "build",
    ".venv",
    "venv",
    "__pycache__",
}

GUIDANCE_NAMES = {
    "AGENTS.md",
    "CLAUDE.md",
    ".claude/CLAUDE.md",
    ".github/copilot-instructions.md",
}


def read_text(path: Path, limit: int = 20000) -> str:
    try:
        return path.read_text(encoding="utf-8", errors="replace")[:limit]
    except OSError:
        return ""


def file_info(root: Path, path: Path) -> dict[str, Any]:
    text = read_text(path)
    imports = re.findall(r"(?m)(?:^|\s)@([A-Za-z0-9_./-]+\.md)\b", text)
    return {
        "path": str(path.relative_to(root)),
        "exists": path.exists(),
        "is_symlink": path.is_symlink(),
        "realpath": str(path.resolve()) if path.exists() else None,
        "line_count": text.count("\n") + (1 if text else 0),
        "imports": imports,
        "imports_agents": any(item.endswith("AGENTS.md") for item in imports),
    }


def discover_guidance(root: Path, max_depth: int = 4) -> list[Path]:
    found: list[Path] = []
    for dirpath, dirnames, filenames in os.walk(root):
        rel = Path(dirpath).relative_to(root)
        depth = len(rel.parts)
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS and depth < max_depth]
        for filename in filenames:
            candidate = Path(dirpath) / filename
            rel_candidate = candidate.relative_to(root)
            rel_text = str(rel_candidate)
            if rel_text in GUIDANCE_NAMES:
                found.append(candidate)
            elif ".cursor/rules" in rel_text and filename.endswith((".md", ".mdc")):
                found.append(candidate)
            elif ".claude/rules" in rel_text and filename.endswith(".md"):
                found.append(candidate)
    return sorted(set(found))


def build_recommendations(root: Path, infos: list[dict[str, Any]]) -> list[str]:
    by_path = {item["path"]: item for item in infos}
    agents = by_path.get("AGENTS.md")
    claude = by_path.get("CLAUDE.md")
    recommendations: list[str] = []

    if claude and not agents:
        recommendations.append(
            "Portability gap: CLAUDE.md exists but AGENTS.md does not. "
            "Consider creating AGENTS.md as the shared source and converting CLAUDE.md into an adapter."
        )
    if agents and claude:
        same_target = agents.get("realpath") == claude.get("realpath")
        if same_target:
            recommendations.append("AGENTS.md and CLAUDE.md resolve to the same file; edit the shared target once.")
        elif claude.get("imports_agents"):
            recommendations.append("CLAUDE.md imports AGENTS.md; keep shared rules in AGENTS.md and Claude-only notes in CLAUDE.md.")
        else:
            recommendations.append(
                "AGENTS.md and CLAUDE.md are separate with no detected import. "
                "Check for duplicated shared rules and consider linking them."
            )
    if agents and not claude:
        recommendations.append(
            "AGENTS.md exists but CLAUDE.md does not. If Claude Code is used, consider adding a small CLAUDE.md adapter."
        )
    if not agents and not claude:
        recommendations.append(
            "No root AGENTS.md or CLAUDE.md detected. Consider adding shared project instructions if agents work in this repo."
        )

    if not recommendations:
        recommendations.append("No obvious guidance-file issue detected.")
    return recommendations


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cwd", default=".", help="Repository root to inspect.")
    parser.add_argument("--output", help="Optional JSON output path.")
    args = parser.parse_args()

    root = Path(args.cwd).resolve()
    files = discover_guidance(root)
    infos = [file_info(root, path) for path in files]
    result = {
        "root": str(root),
        "guidance_files": infos,
        "recommendations": build_recommendations(root, infos),
    }

    payload = json.dumps(result, indent=2, sort_keys=True)
    if args.output:
        Path(args.output).write_text(payload + "\n", encoding="utf-8")
    else:
        print(payload)


if __name__ == "__main__":
    main()
