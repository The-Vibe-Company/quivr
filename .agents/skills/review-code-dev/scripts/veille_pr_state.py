#!/usr/bin/env python3
"""Atomic local ledger for recurring pull-request review discovery."""

from __future__ import annotations

import argparse
from contextlib import contextmanager
import datetime as dt
import fcntl
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Any, Iterator


DEFAULT_HERMES_HOME = Path(os.environ.get("HERMES_HOME", str(Path.home() / ".hermes")))
DEFAULT_STATE = DEFAULT_HERMES_HOME / "state/veille-pr.json"
CLAIM_TTL = dt.timedelta(hours=2)


def now_utc() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)


def iso_now() -> str:
    return now_utc().isoformat().replace("+00:00", "Z")


def pr_key(repo: str, number: int) -> str:
    return f"{repo.lower()}#{number}"


def empty_state() -> dict[str, Any]:
    return {
        "schema_version": 1,
        "initialized_at": None,
        "baseline": {},
        "in_progress": {},
        "reviewed": {},
    }


def load_state(path: Path) -> dict[str, Any]:
    if not path.exists():
        return empty_state()
    data = json.loads(path.read_text(encoding="utf-8"))
    if data.get("schema_version") != 1:
        raise ValueError("Unsupported ledger schema")
    for field in ("baseline", "in_progress", "reviewed"):
        if not isinstance(data.setdefault(field, {}), dict):
            raise ValueError(f"Malformed ledger field: {field}")
    return data


def save_state(path: Path, data: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(data, handle, ensure_ascii=False, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temp_name, 0o600)
        os.replace(temp_name, path)
    finally:
        if os.path.exists(temp_name):
            os.unlink(temp_name)


@contextmanager
def exclusive_state(path: Path) -> Iterator[dict[str, Any]]:
    """Serialize read/modify/write operations so concurrent claims cannot win."""
    path.parent.mkdir(parents=True, exist_ok=True)
    lock_path = path.with_name(path.name + ".lock")
    fd = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o600)
    try:
        with os.fdopen(fd, "r+") as lock_handle:
            fcntl.flock(lock_handle.fileno(), fcntl.LOCK_EX)
            state = load_state(path)
            yield state
            save_state(path, state)
            fcntl.flock(lock_handle.fileno(), fcntl.LOCK_UN)
    except Exception:
        raise


def read_candidates(source: str) -> list[dict[str, Any]]:
    raw = json.load(sys.stdin) if source == "-" else json.loads(Path(source).read_text(encoding="utf-8"))
    if isinstance(raw, dict):
        raw = raw.get("pull_requests", raw.get("items", []))
    if not isinstance(raw, list):
        raise ValueError("Pull requests must be a JSON array")
    candidates: list[dict[str, Any]] = []
    for item in raw:
        if not isinstance(item, dict):
            raise ValueError("Each pull request must be an object")
        repo = item.get("repo") or item.get("repository")
        number = item.get("number")
        url = item.get("url")
        if not repo or not isinstance(number, int) or not url:
            raise ValueError("Each pull request needs repo, integer number, and url")
        candidates.append({
            "repo": repo,
            "number": number,
            "url": url,
            "title": item.get("title", ""),
            "author": item.get("author", ""),
            "created_at": item.get("created_at") or item.get("createdAt"),
        })
    return candidates


def claim_is_active(claim: dict[str, Any]) -> bool:
    claimed_at = claim.get("claimed_at")
    if not claimed_at:
        return False
    try:
        parsed = dt.datetime.fromisoformat(str(claimed_at).replace("Z", "+00:00"))
    except ValueError:
        return False
    return now_utc() - parsed < CLAIM_TTL


def cmd_init(args: argparse.Namespace) -> dict[str, Any]:
    path = Path(args.state)
    with exclusive_state(path) as state:
        if state.get("initialized_at") and not args.replace:
            return {"initialized": False, "reason": "already_initialized"}
        candidates = read_candidates(args.input)
        state.clear()
        state.update(empty_state())
        state["initialized_at"] = iso_now()
        state["baseline"] = {pr_key(item["repo"], item["number"]): item for item in candidates}
        return {"initialized": True, "baseline_count": len(candidates)}


def cmd_pending(args: argparse.Namespace) -> dict[str, Any]:
    path = Path(args.state)
    with exclusive_state(path) as state:
        if not state.get("initialized_at"):
            raise RuntimeError("Ledger is not initialized")
        pending = []
        for item in read_candidates(args.input):
            key = pr_key(item["repo"], item["number"])
            if key in state["baseline"] or key in state["reviewed"]:
                continue
            claim = state["in_progress"].get(key)
            if claim and claim_is_active(claim):
                continue
            pending.append(item)
        return {"pull_requests": pending}


def cmd_claim(args: argparse.Namespace) -> dict[str, Any]:
    path = Path(args.state)
    with exclusive_state(path) as state:
        key = pr_key(args.repo, args.number)
        if key in state["baseline"] or key in state["reviewed"]:
            return {"claimed": False, "reason": "already_handled"}
        current = state["in_progress"].get(key)
        if current and claim_is_active(current):
            return {"claimed": False, "reason": "already_in_progress"}
        state["in_progress"][key] = {
            "repo": args.repo, "number": args.number, "url": args.url,
            "claimed_at": iso_now(), "run_id": args.run_id,
        }
        return {"claimed": True, "key": key}


def cmd_mark_reviewed(args: argparse.Namespace) -> dict[str, Any]:
    path = Path(args.state)
    with exclusive_state(path) as state:
        key = pr_key(args.repo, args.number)
        claim = state["in_progress"].pop(key, {})
        state["reviewed"][key] = {
            "repo": args.repo, "number": args.number, "url": args.url or claim.get("url"),
            "reviewed_at": iso_now(), "review_session": args.review_session,
            "delivery_receipt": args.delivery_receipt,
        }
        return {"reviewed": True, "key": key}


def cmd_release(args: argparse.Namespace) -> dict[str, Any]:
    path = Path(args.state)
    with exclusive_state(path) as state:
        key = pr_key(args.repo, args.number)
        return {"released": state["in_progress"].pop(key, None) is not None, "key": key}


def cmd_status(args: argparse.Namespace) -> dict[str, Any]:
    path = Path(args.state)
    with exclusive_state(path) as state:
        return {
            "initialized_at": state.get("initialized_at"),
            "baseline_count": len(state["baseline"]),
            "in_progress_count": len(state["in_progress"]),
            "reviewed_count": len(state["reviewed"]),
        }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    parser.add_argument("--state", default=str(DEFAULT_STATE))
    sub = parser.add_subparsers(dest="command", required=True)
    init = sub.add_parser("init"); init.add_argument("--input", default="-"); init.add_argument("--replace", action="store_true"); init.set_defaults(func=cmd_init)
    pending = sub.add_parser("pending"); pending.add_argument("--input", default="-"); pending.set_defaults(func=cmd_pending)
    claim = sub.add_parser("claim"); claim.add_argument("--repo", required=True); claim.add_argument("--number", required=True, type=int); claim.add_argument("--url", required=True); claim.add_argument("--run-id", default=""); claim.set_defaults(func=cmd_claim)
    reviewed = sub.add_parser("mark-reviewed"); reviewed.add_argument("--repo", required=True); reviewed.add_argument("--number", required=True, type=int); reviewed.add_argument("--url"); reviewed.add_argument("--review-session", default=""); reviewed.add_argument("--delivery-receipt", default=""); reviewed.set_defaults(func=cmd_mark_reviewed)
    release = sub.add_parser("release"); release.add_argument("--repo", required=True); release.add_argument("--number", required=True, type=int); release.set_defaults(func=cmd_release)
    status = sub.add_parser("status"); status.set_defaults(func=cmd_status)
    return parser


def main() -> int:
    args = build_parser().parse_args()
    try:
        print(json.dumps(args.func(args), ensure_ascii=False))
        return 0
    except Exception as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
