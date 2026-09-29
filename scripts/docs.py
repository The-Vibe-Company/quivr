"""Fail when the documentation inventory and the living docs disagree.

docs/inventory.toml declares every living documentation page with its audience
and kind, and names the dated and excluded Markdown that is not living. This
check fails when:

    unlisted-page   a Markdown file is neither declared, dated nor excluded
    missing-page    a declared page does not exist
    invalid-entry   the inventory is malformed or an entry is inconsistent
    broken-link     a relative link in a living page does not resolve
    missing-path    a repository path in inline code of a living page is absent

    python3 scripts/docs.py               # check the repository (make docs)
    python3 scripts/docs.py --root DIR    # check another checkout

Files are the tracked and untracked, non-ignored files, as for the denylist.
External URLs are never fetched and anchors are not checked. A path in inline
code is checked only when its first folder exists in the repository (or beside
the page), so `origin/main` or `application/json` are not mistaken for paths;
a path under a deleted top-level folder is not caught. To mention a path that
no longer exists on purpose, write it without backticks.
"""
import argparse
import dataclasses
import fnmatch
import pathlib
import posixpath
import re
import subprocess
import sys
import tomllib
import urllib.parse

ROOT = pathlib.Path(__file__).resolve().parent.parent
INVENTORY = 'docs/inventory.toml'
AUDIENCES = ('functional', 'plugin-author', 'contributor')
KINDS = ('guide', 'concept', 'generated-reference', 'index')
DECLARE = ('add `"{path}" = {{ audience = "{audiences}", kind = "{kinds}" }}` under [pages] in '
           + INVENTORY + ', or match it in `dated` or `excluded` there if it is not a living page')

_FENCE = re.compile(r'^\s{0,3}(`{3,}|~{3,})')
_CODE = re.compile(r'(`+)(.+?)\1')
_INLINE = re.compile(r'\]\(\s*')
_REFERENCE = re.compile(r'^[ \t]{0,3}\[(?!\^)[^\]\n]+\]:[ \t]*(<[^>\n]*>|\S+)', re.M)
_HTML = re.compile(r'''<(?:a|img)\b[^>]*?\b(?:href|src)\s*=\s*["']([^"']+)["']''', re.I | re.S)
_COMMENT = re.compile(r'<!--.*?-->', re.S)
_SCHEME = re.compile(r'^[A-Za-z][A-Za-z0-9+.-]*:')
_NOT_A_PATH = re.compile(r'[<>*{}$…\[\]|=\\]|\.\.\.|://')
_LINE_SUFFIX = re.compile(r'(:\d+)+$')


@dataclasses.dataclass(frozen=True, order=True)
class Finding:
    path: str
    line: int
    rule: str
    message: str
    fix: str

    def __str__(self):
        return f'{self.path}:{self.line}: [{self.rule}] {self.message}. Fix: {self.fix}.'


class Tree:
    """The repository files a doc may refer to."""

    def __init__(self, root):
        self.root = pathlib.Path(root)
        listed = _git(self.root, 'ls-files', '-z', '--cached', '--others', '--exclude-standard')
        staged = _git(self.root, 'ls-files', '-z', '--stage')
        self.gitlinks = {entry.split('\t', 1)[1] for entry in staged if entry.startswith('160000 ')}
        self.files = {name for name in listed
                      if name not in self.gitlinks and (self.root / name).is_file()}
        self.dirs = set(self.gitlinks)
        for name in self.files | self.gitlinks:
            parts = name.split('/')[:-1]
            for size in range(1, len(parts) + 1):
                self.dirs.add('/'.join(parts[:size]))
        # Every listed name and folder, even if deleted in the working tree: a
        # code span starting with one of them is meant as a repository path.
        self.known = set(listed) | self.dirs
        for name in listed:
            parts = name.split('/')
            for size in range(1, len(parts)):
                self.known.add('/'.join(parts[:size]))

    def exists(self, rel):
        rel = posixpath.normpath(rel)
        if rel in ('.', '') or rel.startswith('../'):
            return rel in ('.', '')
        if rel in self.files or rel in self.dirs:
            return True
        return any(rel.startswith(link + '/') for link in self.gitlinks)


def _git(root, *args):
    out = subprocess.run(['git', '-C', str(root), *args], check=True, capture_output=True).stdout
    return [name for name in out.decode().split('\0') if name]


def _matches(path, patterns):
    return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)


def _entry_lines(text):
    lines = {}
    for number, line in enumerate(text.splitlines(), 1):
        match = re.match(r'''\s*(?:"([^"]+)"|'([^']+)')\s*=''', line)
        if match:
            lines.setdefault(match.group(1) or match.group(2), number)
    return lines


def load_inventory(root):
    """Return (pages, dated, excluded, line numbers, findings); pages is None when unreadable."""
    path = pathlib.Path(root) / INVENTORY
    if not path.is_file():
        return None, [], [], {}, [Finding(INVENTORY, 1, 'invalid-entry', 'the documentation inventory is missing',
                                        f'create {INVENTORY} with a [pages] table; see scripts/docs.py')]
    text = path.read_text()
    try:
        data = tomllib.loads(text)
    except tomllib.TOMLDecodeError as error:
        match = re.search(r'line (\d+)', str(error))
        return None, [], [], {}, [Finding(INVENTORY, int(match.group(1)) if match else 1, 'invalid-entry',
                                        f'the inventory is not valid TOML ({error})', 'fix the TOML syntax')]
    lines = _entry_lines(text)
    findings = []
    for key in sorted(set(data) - {'pages', 'dated', 'excluded'}):
        findings.append(Finding(INVENTORY, 1, 'invalid-entry', f'unknown top-level key "{key}"',
                                'use only [pages], dated and excluded'))
    pages = data.get('pages', {})
    if not isinstance(pages, dict):
        findings.append(Finding(INVENTORY, 1, 'invalid-entry', '"pages" must be a table',
                                'write [pages] followed by one "path.md" = { audience = ..., kind = ... } line per page'))
        pages = {}
    patterns = {}
    for key in ('dated', 'excluded'):
        value = data.get(key, [])
        if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
            findings.append(Finding(INVENTORY, 1, 'invalid-entry', f'"{key}" must be a list of glob strings',
                                    f'write {key} = ["folder/**"]'))
            value = []
        patterns[key] = value
    valid = {}
    for page, entry in pages.items():
        line = lines.get(page, 1)
        if not isinstance(entry, dict) or set(entry) != {'audience', 'kind'}:
            findings.append(Finding(INVENTORY, line, 'invalid-entry', f'"{page}" must set exactly audience and kind',
                                    f'write "{page}" = {{ audience = "...", kind = "..." }}'))
            continue
        for field, allowed in (('audience', AUDIENCES), ('kind', KINDS)):
            if entry[field] not in allowed:
                findings.append(Finding(INVENTORY, line, 'invalid-entry',
                                        f'"{page}" has unknown {field} "{entry[field]}"',
                                        f'use one of {", ".join(allowed)}'))
        for key in ('dated', 'excluded'):
            if _matches(page, patterns[key]):
                findings.append(Finding(INVENTORY, line, 'invalid-entry',
                                        f'"{page}" is declared living but also matches {key}',
                                        f'remove it from [pages] or narrow the {key} pattern'))
        if posixpath.normpath(page) != page or page.startswith(('/', '../')):
            findings.append(Finding(INVENTORY, line, 'invalid-entry', f'"{page}" is not a repository-relative path',
                                    f'write it as "{posixpath.normpath(page).lstrip("/")}"'))
            continue
        valid[page] = entry
    return valid, patterns['dated'], patterns['excluded'], lines, findings


def _target(raw):
    raw = raw.strip()
    if raw.startswith('<') and raw.endswith('>'):
        raw = raw[1:-1].strip()
    if not raw or raw.startswith('#') or raw.startswith('//') or _SCHEME.match(raw):
        return None
    raw = re.split(r'[?#]', raw, maxsplit=1)[0]
    return urllib.parse.unquote(raw) or None


def _resolve(page, target):
    if target.startswith('/'):
        return posixpath.normpath(target.lstrip('/'))
    return posixpath.normpath(posixpath.join(posixpath.dirname(page), target))


def _lines_outside_fences(text):
    fence = None
    for number, line in enumerate(text.splitlines(), 1):
        match = _FENCE.match(line)
        if fence:
            if match and match.group(1)[0] == fence[0] and len(match.group(1)) >= len(fence) \
                    and not line.strip()[len(match.group(1)):].strip():
                fence = None
            continue
        if match:
            fence = match.group(1)
            continue
        yield number, line


def _path_candidates(token):
    token = token.strip('(\'"').rstrip(',.;:)\'"')
    if '/' not in token or _NOT_A_PATH.search(token) or token.startswith(('/', '~')):
        return None
    token = _LINE_SUFFIX.sub('', token.split('#', 1)[0])
    if token.startswith('./'):
        token = token[2:]
    return token or None


def _blank(match):
    return re.sub(r'[^\n]', ' ', match.group())


def _prose(text):
    """The page with fenced code, code spans, HTML comments and escapes blanked; offsets are kept."""
    keep = dict(_lines_outside_fences(text))
    lines = [_CODE.sub(_blank, line) if number in keep else ''
             for number, line in enumerate(text.splitlines(), 1)]
    prose = _COMMENT.sub(_blank, '\n'.join(lines))
    return prose.replace('\\[', '  ').replace('\\]', '  ')


def _inline_target(prose, start):
    """The destination of an inline link opened at `start`, allowing balanced parentheses."""
    if prose.startswith('<', start):
        end = prose.find('>', start)
        return prose[start:end + 1] if end != -1 else None
    depth, index = 0, start
    while index < len(prose) and not prose[index].isspace():
        char = prose[index]
        if char == '(':
            depth += 1
        elif char == ')':
            if depth == 0:
                break
            depth -= 1
        index += 1
    return prose[start:index] or None


def _links(text):
    prose = _prose(text)
    found = [(m.end(), _inline_target(prose, m.end())) for m in _INLINE.finditer(prose)]
    found += [(m.start(1), m.group(1)) for m in _REFERENCE.finditer(prose)]
    found += [(m.start(1), m.group(1)) for m in _HTML.finditer(prose)]
    for offset, raw in sorted(found, key=lambda item: item[0]):
        if raw:
            yield prose.count('\n', 0, offset) + 1, raw


def check_page(tree, page):
    findings = []
    text = (tree.root / page).read_text(encoding='utf-8', errors='replace')
    here = posixpath.dirname(page)
    for number, raw in _links(text):
        target = _target(raw)
        if target is None:
            continue
        resolved = _resolve(page, target)
        if not tree.exists(resolved):
            findings.append(Finding(page, number, 'broken-link', f'link "{raw}" points to {resolved}, which does not exist',
                                    'point the link at the current file, or remove it if the target is gone'))
    for number, line in _lines_outside_fences(text):
        for span in (match.group(2) for match in _CODE.finditer(line)):
            for token in span.split():
                path = _path_candidates(token)
                if path is None:
                    continue
                first = path.split('/', 1)[0]
                candidates = []
                if first in tree.known:
                    candidates.append(posixpath.normpath(path))
                if here and posixpath.join(here, first) in tree.known:
                    candidates.append(_resolve(page, path))
                if candidates and not any(tree.exists(c) for c in candidates):
                    findings.append(Finding(page, number, 'missing-path',
                                            f'repository path `{path}` does not exist',
                                            'update the path to the renamed file, or remove the reference'))
    return findings


def check(root):
    try:
        tree = Tree(root)
    except subprocess.CalledProcessError:
        return [Finding(str(root), 1, 'invalid-entry', 'not a git checkout', 'run the check from a clone of the repository')]
    pages, dated, excluded, lines, findings = load_inventory(root)
    if pages is None:
        return findings
    for page in sorted(pages):
        if page not in tree.files:
            findings.append(Finding(INVENTORY, lines.get(page, 1), 'missing-page',
                                    f'declared page {page} does not exist',
                                    'restore the page, or remove its line from [pages] (declare its new path if it moved)'))
    for name in sorted(tree.files):
        if not name.lower().endswith('.md') or name in pages or _matches(name, dated) or _matches(name, excluded):
            continue
        fix = DECLARE.format(path=name, audiences='|'.join(AUDIENCES), kinds='|'.join(KINDS))
        findings.append(Finding(name, 1, 'unlisted-page', 'this Markdown page is not in the documentation inventory', fix))
    for page in sorted(pages):
        if page in tree.files:
            findings.extend(check_page(tree, page))
    return sorted(findings)


def main(argv=None, stdout=sys.stdout):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--root', default=str(ROOT))
    args = parser.parse_args(argv)
    findings = check(args.root)
    for finding in findings:
        print(finding, file=stdout)
    if findings:
        print(f'{len(findings)} documentation problem(s); see scripts/docs.py and {INVENTORY}', file=stdout)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
