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

_GO_TOKEN = re.compile(
    r'//[^\n]*|/\*.*?\*/|"(?:\\.|[^"\\])*"|`[^`]*`|\'(?:\\.|[^\'\\])*\'|\w+|[();.]',
    re.S,
)
_MOD_TOKEN = re.compile(r'"(?:\\.|[^"\\])*"|`[^`]*`|\S+')
_COMMENT = re.compile(r"//[^\n]*|/\*.*?\*/", re.S)


def _unquote(token: str) -> str:
    token = token.strip()
    if len(token) >= 2 and token[0] == token[-1] and token[0] in ('"', "`"):
        return token[1:-1]
    return token


def _find_unquoted(line: str, target: str) -> int:
    in_quote = ""
    escaped = False
    tlen = len(target)
    for i, ch in enumerate(line):
        if escaped:
            escaped = False
            continue
        if in_quote:
            if in_quote == '"' and ch == "\\":
                escaped = True
            elif ch == in_quote:
                in_quote = ""
            continue
        if ch in ('"', "`"):
            in_quote = ch
        elif line[i : i + tlen] == target:
            return i
    return -1


def imports(source: str) -> list[str]:
    """The import paths of a Go file (lexed so comments and string literals never match)."""
    tokens = [
        match.group(0)
        for match in _GO_TOKEN.finditer(source)
        if not match.group(0).startswith(("//", "/*"))
    ]
    found: list[str] = []
    i = 0
    n = len(tokens)
    while i < n:
        if tokens[i] != "import":
            i += 1
            continue
        i += 1
        if i >= n:
            break
        if tokens[i] == "(":
            i += 1
            while i < n and tokens[i] != ")":
                tok = tokens[i]
                if tok[0] in ('"', "`"):
                    found.append(_unquote(tok))
                i += 1
            if i < n and tokens[i] == ")":
                i += 1
        elif tokens[i][0] in ('"', "`"):
            found.append(_unquote(tokens[i]))
            i += 1
        elif i + 1 < n and tokens[i + 1][0] in ('"', "`"):
            found.append(_unquote(tokens[i + 1]))
            i += 2
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
            close_idx = _find_unquoted(line, ")")
            if close_idx != -1:
                spec = line[:close_idx].strip()
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
                close_idx = _find_unquoted(rest, ")")
                if close_idx != -1:
                    spec = rest[:close_idx].strip()
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
            arrow_idx = _find_unquoted(spec, "=>")
            lhs = spec[:arrow_idx] if arrow_idx != -1 else spec
            rhs = spec[arrow_idx + 2 :] if arrow_idx != -1 else ""
            lhs_words = [_unquote(w) for w in _MOD_TOKEN.findall(lhs)]
            if lhs_words and lhs_words[0] == ENGINE:
                return True
            if arrow_idx != -1:
                rhs_words = [_unquote(w) for w in _MOD_TOKEN.findall(rhs)]
                if rhs_words and (rhs_words[0] == ENGINE or (len(rhs_words) == 1 and _references_engine_dir(rhs_words[0], base_dir))):
                    return True
        elif directive == "require":
            words = [_unquote(w) for w in _MOD_TOKEN.findall(spec)]
            if words and words[0] == ENGINE:
                return True
        elif directive == "use" and is_work:
            words = [_unquote(w) for w in _MOD_TOKEN.findall(spec)]
            if len(words) == 1 and (words[0] == ENGINE or _references_engine_dir(words[0], base_dir)):
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
