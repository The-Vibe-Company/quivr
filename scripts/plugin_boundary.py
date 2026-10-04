"""Fail when plugin code reaches into the engine's private packages.

Plugins under plugins/ and the public SDKs under sdks/go/ may import only
public packages. Go's own internal rule does not protect the engine here:
the SDK's module path sits under the engine's module path, so Go would let it
import github.com/The-Vibe-Company/quivr-v2/internal/... once its go.mod
requires the engine. This check fails on:

  - any Go import of the engine's internal/ packages, in any build-tagged file;
  - a go.mod under those trees that requires or replaces the engine module,
    the route to such an import.

Usage: python3 scripts/plugin_boundary.py [<repository root>]
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ENGINE = "github.com/The-Vibe-Company/quivr-v2"
TREES = ("plugins", "sdks/go")
SKIP = {".git", "node_modules", ".venv", "venv", "__pycache__", "testdata"}

_BLOCK = re.compile(r"^\s*import\s*\((.*?)\)", re.S | re.M)
_SINGLE = re.compile(r'^\s*import\s+(?:[\w.]+\s+)?"([^"]+)"', re.M)
_SPEC = re.compile(r'^\s*(?:[\w.]+\s+)?"([^"]+)"', re.M)
_COMMENT = re.compile(r"//[^\n]*|/\*.*?\*/", re.S)


def imports(source: str) -> list[str]:
    """The import paths of a Go file (comments removed first)."""
    code = _COMMENT.sub("", source)
    found = _SINGLE.findall(code)
    for block in _BLOCK.findall(code):
        found += _SPEC.findall(block)
    return found


def engine_dependency(gomod: str) -> bool:
    """Whether a go.mod requires or replaces the engine module itself."""
    code = _COMMENT.sub("", gomod)
    for line in code.splitlines():
        words = line.replace("(", " ").split()
        if words[:1] in (["require"], ["replace"]):
            words = words[1:]
        if words and words[0] == ENGINE:
            return True
    return False


def files(root: Path, pattern: str):
    for tree in TREES:
        base = root / tree
        if not base.is_dir():
            continue
        for path in sorted(base.rglob(pattern)):
            if not SKIP.intersection(path.relative_to(root).parts):
                yield path


def violations(root: Path) -> list[str]:
    found = []
    for path in files(root, "*.go"):
        for imported in imports(path.read_text(encoding="utf-8", errors="replace")):
            if imported == ENGINE + "/internal" or imported.startswith(ENGINE + "/internal/"):
                found.append(f"{path.relative_to(root)}: imports {imported}; plugins and SDKs use only public packages")
    for path in files(root, "go.mod"):
        if engine_dependency(path.read_text(encoding="utf-8", errors="replace")):
            found.append(f"{path.relative_to(root)}: requires or replaces the engine module {ENGINE}; depend on {ENGINE}/sdks/go instead")
    return found


def main(argv: list[str]) -> int:
    root = Path(argv[0]) if argv else Path(__file__).resolve().parents[1]
    found = violations(root)
    for line in found:
        print(line, file=sys.stderr)
    if found:
        print(f"plugin boundary: {len(found)} violation(s)", file=sys.stderr)
        return 1
    print("plugin boundary: plugins/ and sdks/go/ import no engine internal/ package")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
