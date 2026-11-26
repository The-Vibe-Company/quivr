"""Fail when plugin code reaches into the engine's private packages.

Plugins under plugins/, the multi-call bundle under deploy/plugins/, and the
public SDKs under sdks/go/ may import only public packages. Go's own internal
rule does not protect the engine here:
the SDK's module path sits under the engine's module path, so Go would let it
import github.com/The-Vibe-Company/quivr/internal/... once its go.mod
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

ENGINE = "github.com/The-Vibe-Company/quivr"
TREES = ("plugins", "sdks/go", "deploy/plugins")
SKIP = {".git", "node_modules", ".venv", "venv", "__pycache__", "testdata"}

_TOKEN_OR_COMMENT = re.compile(r'"(?:\\.|[^"\\])*"|`[^`]*`|//[^\n]*|/\*.*?\*/', re.S)
_BLOCK = re.compile(r"(?:^|;)\s*import\s*\((.*?)\)", re.S | re.M)
_SINGLE = re.compile(r'(?:^|;)\s*import\s+(?:[\w.]+\s+)?(?:"([^"]+)"|`([^`]+)`)', re.M)
_SPEC = re.compile(r'(?:^|;)\s*(?:[\w.]+\s+)?(?:"([^"]+)"|`([^`]+)`)', re.M)
_COMMENT = re.compile(r"//[^\n]*|/\*.*?\*/", re.S)


def _strip_comments(source: str) -> str:
    return _TOKEN_OR_COMMENT.sub(lambda m: m.group(0) if m.group(0)[0] in '"`' else " ", source)


def _unquote(token: str) -> str:
    token = token.strip()
    if len(token) >= 2 and token[0] == token[-1] and token[0] in ('"', "`"):
        return token[1:-1]
    return token


def imports(source: str) -> list[str]:
    """The import paths of a Go file (comments removed first)."""
    code = _strip_comments(source)
    found = [a or b for a, b in _SINGLE.findall(code)]
    for block in _BLOCK.findall(code):
        found += [a or b for a, b in _SPEC.findall(block)]
    return found


def _module_name(gomod_path: Path) -> str | None:
    if not gomod_path.is_file():
        return None
    for line in _COMMENT.sub("", gomod_path.read_text(encoding="utf-8", errors="replace")).splitlines():
        words = line.split()
        if len(words) >= 2 and words[0] == "module":
            return _unquote(words[1])
    return None


def _references_engine_dir(raw_target: str, base_dir: Path | None) -> bool:
    if base_dir is None:
        return False
    candidate = Path(raw_target)
    resolved = candidate.resolve() if candidate.is_absolute() else (base_dir / candidate).resolve()
    return _module_name(resolved / "go.mod") == ENGINE


def engine_dependency(gomod: str, *, base_dir: Path | None = None, is_work: bool = False) -> bool:
    """Whether a go.mod or go.work requires, uses, or replaces the engine module itself."""
    code = _COMMENT.sub("", gomod)
    in_block: str | None = None
    for raw_line in code.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        directive: str | None = None
        spec = ""
        if in_block:
            if ")" in line:
                spec = line.split(")", 1)[0].strip()
                directive = in_block
                in_block = None
            else:
                spec = line
                directive = in_block
        else:
            match = re.match(r"^(require|replace|use)\s*\((.*)$", line)
            if match:
                directive = match.group(1)
                rest = match.group(2).strip()
                if ")" in rest:
                    spec = rest.split(")", 1)[0].strip()
                else:
                    in_block = directive
                    spec = rest
            else:
                match = re.match(r"^(require|replace|use)\s+(.+)$", line)
                if match:
                    directive = match.group(1)
                    spec = match.group(2).strip()
        if not directive or not spec:
            continue
        if directive == "replace":
            lhs, sep, rhs = spec.partition("=>")
            lhs_words = [_unquote(w) for w in lhs.split()]
            if lhs_words and lhs_words[0] == ENGINE:
                return True
            if sep:
                rhs_words = [_unquote(w) for w in rhs.split()]
                if rhs_words and (rhs_words[0] == ENGINE or (len(rhs_words) == 1 and _references_engine_dir(rhs_words[0], base_dir))):
                    return True
        elif directive == "require":
            words = [_unquote(w) for w in spec.split()]
            if words and words[0] == ENGINE:
                return True
        elif directive == "use" and is_work:
            target = _unquote(spec)
            if target == ENGINE or _references_engine_dir(target, base_dir):
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
        if engine_dependency(path.read_text(encoding="utf-8", errors="replace"), base_dir=path.parent):
            found.append(f"{path.relative_to(root)}: requires or replaces the engine module {ENGINE}; depend on {ENGINE}/sdks/go instead")
    for path in files(root, "go.work"):
        if engine_dependency(path.read_text(encoding="utf-8", errors="replace"), base_dir=path.parent, is_work=True):
            found.append(f"{path.relative_to(root)}: uses or replaces the engine module {ENGINE}; depend on {ENGINE}/sdks/go instead")
    return found


def main(argv: list[str]) -> int:
    root = Path(argv[0]) if argv else Path(__file__).resolve().parents[1]
    found = violations(root)
    for line in found:
        print(line, file=sys.stderr)
    if found:
        print(f"plugin boundary: {len(found)} violation(s)", file=sys.stderr)
        return 1
    trees = ", ".join(tree + "/" for tree in TREES)
    print(f"plugin boundary: {trees} import no engine internal/ package")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
