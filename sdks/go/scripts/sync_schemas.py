"""Copy the Plugin Protocol schemas the Go SDK embeds from the contracts.

go:embed cannot reach outside the sdks/go module, so the SDK keeps
byte-identical copies under quivrplugin/schemas, in the same relative layout
as contracts/ so that $refs between schema files resolve unchanged.

Usage:
  python3 sdks/go/scripts/sync_schemas.py          # rewrite the copies
  python3 sdks/go/scripts/sync_schemas.py --check  # fail when a copy is stale
"""
from __future__ import annotations

import sys
from pathlib import Path

SDK = Path(__file__).resolve().parents[1]
CONTRACTS = SDK.parents[1] / "contracts"
TARGET = SDK / "quivrplugin" / "schemas"


def sources() -> dict[str, bytes]:
    files = {"shared/v0/manifest.schema.json": (CONTRACTS / "shared/v0/manifest.schema.json").read_bytes()}
    for path in sorted((CONTRACTS / "plugins/v0").glob("*.schema.json")):
        files[f"plugins/v0/{path.name}"] = path.read_bytes()
    return files


def main() -> int:
    check = sys.argv[1:] == ["--check"]
    want = sources()
    have = {p.relative_to(TARGET).as_posix(): p.read_bytes() for p in TARGET.rglob("*.json")} if TARGET.exists() else {}
    if check:
        stale = sorted(name for name in want.keys() | have.keys() if want.get(name) != have.get(name))
        if stale:
            print("sdks/go schema copies are stale; run python3 sdks/go/scripts/sync_schemas.py:", ", ".join(stale), file=sys.stderr)
            return 1
        return 0
    for name in have.keys() - want.keys():
        (TARGET / name).unlink()
    for name, data in want.items():
        (TARGET / name).parent.mkdir(parents=True, exist_ok=True)
        (TARGET / name).write_bytes(data)
    return 0


if __name__ == "__main__":
    sys.exit(main())
