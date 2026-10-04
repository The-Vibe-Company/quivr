#!/usr/bin/env python3
"""Classify a lesson into likely project improvement destinations."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


RULE_KEYWORDS = ("rule", "instruction", "agent", "claude", "codex", "agents.md", "claude.md", "convention")
TEST_KEYWORDS = ("bug", "regression", "expected", "actual", "schema", "frontmatter", "fixture", "unit", "e2e")
CI_KEYWORDS = ("ci", "workflow", "github actions", "check", "pull request", "runner", "minutes")
SCRIPT_KEYWORDS = ("manual", "repeat", "preflight", "validate", "script", "command")
DOC_KEYWORDS = ("setup", "runbook", "readme", "documentation", "how to", "deploy")
ADR_KEYWORDS = ("architecture", "decision", "tradeoff", "why", "adr")


def score(text: str, keywords: tuple[str, ...]) -> int:
    lower = text.lower()
    return sum(1 for keyword in keywords if keyword in lower)


def classify(text: str) -> list[dict[str, object]]:
    candidates = [
        ("agent_instructions", score(text, RULE_KEYWORDS), "Put reusable project rules in shared agent instructions."),
        ("test", score(text, TEST_KEYWORDS), "Add a regression or validation test when behavior is machine-checkable."),
        ("ci", score(text, CI_KEYWORDS), "Wire existing checks into CI when review needs automatic protection."),
        ("script", score(text, SCRIPT_KEYWORDS), "Create a deterministic command for repeated manual validation."),
        ("docs", score(text, DOC_KEYWORDS), "Document setup or process knowledge for future contributors."),
        ("adr", score(text, ADR_KEYWORDS), "Record durable architecture decisions and tradeoffs."),
    ]
    ranked = [
        {"destination": name, "score": value, "reason": reason}
        for name, value, reason in sorted(candidates, key=lambda item: item[1], reverse=True)
        if value > 0
    ]
    if not ranked:
        ranked.append(
            {
                "destination": "no_automation_yet",
                "score": 0,
                "reason": "No strong signal found. Treat as a one-off unless recurrence or risk is clear.",
            }
        )
    return ranked


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--text", help="Lesson text to classify.")
    parser.add_argument("--file", help="File containing lesson text.")
    parser.add_argument("--output", help="Optional JSON output path.")
    args = parser.parse_args()

    if args.file:
        text = Path(args.file).read_text(encoding="utf-8", errors="replace")
    elif args.text:
        text = args.text
    else:
        parser.error("Provide --text or --file")

    result = {"input_excerpt": text[:500], "ranked_destinations": classify(text)}
    payload = json.dumps(result, indent=2, sort_keys=True)
    if args.output:
        Path(args.output).write_text(payload + "\n", encoding="utf-8")
    else:
        print(payload)


if __name__ == "__main__":
    main()
