"""Fail when the documentation inventory and the living docs disagree.

docs/inventory.toml declares every living documentation page with its audience
and kind, and names the dated and excluded Markdown that is not living. This
check fails when:

    unlisted-page   a Markdown file is neither declared, dated nor excluded
    missing-page    a declared page does not exist
    invalid-entry   the inventory is malformed or an entry is inconsistent
    broken-link     a relative link in a living page does not resolve
    missing-path    a repository path in inline code of a living page is absent
    over-budget     a page has more lines than its budget in [budgets]
    missing-budget  AGENTS.md, CONTEXT.md or a guide has no budget in [budgets]
    glossary-term   a CONTEXT.md term lacks a definition or an `_Avoid_:` line
    frozen-document a dated document (ADRs included) differs from the base or
                    was removed, unless the base version's status is proposed
    dated-header    a new dated document has no Date: or Status: line

    python3 scripts/docs.py               # check the repository (make docs)
    python3 scripts/docs.py --root DIR    # check another checkout
    python3 scripts/docs.py --base REF    # compare dated documents with REF

Dated documents are compared with the commit where the checkout forked from
the base (default origin/main, or the DOCS_BASE variable): the merge base, so a
branch behind main is not blamed for main's later documents. That needs the
history back to the fork point, not a shallow clone. The working tree is
compared, so uncommitted edits are caught too. A dated document is
superseded by a new one, never edited; see docs/adr/0004.

Files are the tracked and untracked, non-ignored files, as for the denylist.
External URLs are never fetched and anchors are not checked. A path in inline
code is checked only when its first folder exists in the repository (or beside
the page), so `origin/main` or `application/json` are not mistaken for paths;
a path under a deleted top-level folder is not caught. To mention a path that
no longer exists on purpose, write it without backticks.
"""
import argparse
import dataclasses
import difflib
import fnmatch
import os
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
    lines = _table_lines(text, 'pages')
    findings = []
    for key in sorted(set(data) - {'pages', 'dated', 'excluded', 'budgets'}):
        findings.append(Finding(INVENTORY, 1, 'invalid-entry', f'unknown top-level key "{key}"',
                                'use only [pages], [budgets], dated and excluded'))
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


BUDGETED = ('AGENTS.md', 'CONTEXT.md')
GLOSSARY = 'CONTEXT.md'
SIGNAL = ('a pull request that changes the documented behaviour, a bug or recurring agent error traced to a '
          'documentation gap, a review comment, or a user question')
_TERM = re.compile(r'^\*\*([^*\n]+)\*\*(.*)$')
_AVOID = re.compile(r'^_Avoid_:(.*)$')


def suggested_budget(lines):
    """About 5% above the current size, rounded up to a multiple of 5."""
    return max(5, -(-lines * 21 // 100) * 5)


def _table_lines(text, table):
    """Line numbers of the keys of one [table] in the inventory text."""
    lines, inside = {}, False
    for number, line in enumerate(text.splitlines(), 1):
        header = re.match(r'\s*\[([^\]]+)\]\s*(#.*)?$', line)
        if header:
            inside = header.group(1).strip() == table
            continue
        match = re.match(r'''\s*(?:"([^"]+)"|'([^']+)')\s*=''', line)
        if inside and match:
            lines.setdefault(match.group(1) or match.group(2), number)
    return lines


def load_budgets(root, pages):
    """Return ({page: max lines}, [pages] line numbers, findings) from the inventory."""
    text = (pathlib.Path(root) / INVENTORY).read_text()
    data = tomllib.loads(text)
    budgets, declared = data.get('budgets', {}), data.get('pages', {})
    declared = declared if isinstance(declared, dict) else {}
    lines, page_lines = _table_lines(text, 'budgets'), _table_lines(text, 'pages')
    if not isinstance(budgets, dict):
        return {}, page_lines, [Finding(INVENTORY, 1, 'invalid-entry', '"budgets" must be a table',
                            'write [budgets] followed by one "path.md" = <max lines> line per page')]
    valid, findings = {}, []
    for page, budget in budgets.items():
        line = lines.get(page, 1)
        if isinstance(budget, bool) or not isinstance(budget, int) or budget < 1:
            findings.append(Finding(INVENTORY, line, 'invalid-entry',
                                    f'the budget of "{page}" must be a positive number of lines, not {budget!r}',
                                    f'write "{page}" = <max lines> under [budgets]'))
        elif page not in declared:
            findings.append(Finding(INVENTORY, line, 'invalid-entry',
                                    f'"{page}" has a budget but is not declared in [pages]',
                                    'declare the page in [pages], or remove its budget'))
        elif page in pages:
            valid[page] = budget
    return valid, page_lines, findings


def check_budgets(tree, pages, budgets, page_lines):
    """AGENTS.md, CONTEXT.md and every guide have a line budget, and stay within it."""
    findings = []
    for page in sorted(pages):
        if page not in tree.files:
            continue
        size = len((tree.root / page).read_text(encoding='utf-8', errors='replace').splitlines())
        budget = budgets.get(page)
        if budget is None:
            if page in BUDGETED or pages[page]['kind'] == 'guide':
                findings.append(Finding(INVENTORY, page_lines.get(page, 1), 'missing-budget',
                                        f'{page} is a guide or agent-steering page without a line budget',
                                        f'add `"{page}" = {suggested_budget(size)}` under [budgets] '
                                        f'(its {size} lines plus about 5%)'))
        elif size > budget:
            findings.append(Finding(page, budget + 1, 'over-budget',
                                    f'the page has {size} lines, over its budget of {budget} in {INVENTORY}',
                                    'shorten it by linking to the authoritative source instead of restating it; '
                                    f'raise the budget only in a pull request whose signal needs it ({SIGNAL})'))
    return findings


def check_glossary(tree):
    """Each paragraph starting in bold in CONTEXT.md is a `**Term**:` with a definition and an `_Avoid_:` line."""
    text = (tree.root / GLOSSARY).read_text(encoding='utf-8', errors='replace')
    terms, current = [], None
    for number, line in _lines_outside_fences(text):
        term = _TERM.match(line) if current is None else None  # a term starts a paragraph
        if term:
            name, rest = term.group(1).strip(), term.group(2)
            well_formed = rest.startswith(':') and not name.endswith(':')
            current = {'name': name.rstrip(':'), 'line': number, 'well_formed': well_formed,
                       'definition': well_formed and bool(rest[1:].strip()), 'avoid': False}
            terms.append(current)
        elif not line.strip() or line.lstrip().startswith('#'):
            current = None
        elif current is not None:
            avoid = _AVOID.match(line.strip())
            if avoid:
                current['avoid'] = current['avoid'] or bool(avoid.group(1).strip())
            else:
                current['definition'] = True
    findings = []
    for term in terms:
        if not term['well_formed']:
            findings.append(Finding(GLOSSARY, term['line'], 'glossary-term',
                                    f'term "{term["name"]}" is not written as `**{term["name"]}**:`',
                                    f'write the term alone in bold followed by a colon, `**{term["name"]}**:`, '
                                    'and put any alias in its definition'))
        if not term['definition']:
            findings.append(Finding(GLOSSARY, term['line'], 'glossary-term', f'term "{term["name"]}" has no definition',
                                    'write one or two sentences defining it on the lines right after the term'))
        if not term['avoid']:
            findings.append(Finding(GLOSSARY, term['line'], 'glossary-term',
                                    f'term "{term["name"]}" has no `_Avoid_:` line',
                                    'end its paragraph with `_Avoid_: <words not to use for it>`, '
                                    'with no blank line before it'))
    return findings


# Frozen dated documents -----------------------------------------------------

BASE = 'origin/main'
HEADER_LINES = 20
_STATUS = re.compile(r'^[ \t]*(?:>[ \t]*)?Status:[ \t]*(\S.*)$', re.M)
_DATE = re.compile(r'^[ \t]*(?:>[ \t]*)?Date:[ \t]*\d{4}-\d{2}-\d{2}\b', re.M)
SUPERSEDE = ('restore it with `git checkout {commit} -- {path}`; a dated document is never edited, '
             'so add a new document (or ADR) that supersedes it; see docs/adr/0004')


def _header(text):
    return '\n'.join(text.splitlines()[:HEADER_LINES])


def _status(text):
    match = _STATUS.search(_header(text))
    return match.group(1).strip() if match else None


def _first_difference(old, new):
    old_lines, new_lines = old.splitlines(), new.splitlines()
    for tag, _, _, first, _ in difflib.SequenceMatcher(None, old_lines, new_lines, autojunk=False).get_opcodes():
        if tag != 'equal':
            return min(first + 1, max(len(new_lines), 1))
    return 1


def check_frozen(tree, dated, base):
    """Findings for dated documents that differ from `base`, and new ones without a header."""
    root = tree.root
    resolved = subprocess.run(['git', '-C', str(root), 'rev-parse', '--verify', '--quiet', f'{base}^{{commit}}'],
                              capture_output=True, text=True)
    if resolved.returncode != 0:
        return [Finding(INVENTORY, 1, 'frozen-document',
                        f'cannot resolve {base} to compare dated documents against',
                        'run `git fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main` '
                        '(add --unshallow in a shallow clone; verify.yml fetches full history) '
                        'or pass --base / set DOCS_BASE')]
    # Compare with the commit this checkout forked from, not the base's tip, so that a
    # branch behind main is not blamed for documents main added or changed since.
    forked = subprocess.run(['git', '-C', str(root), 'merge-base', 'HEAD', resolved.stdout.strip()],
                            capture_output=True, text=True)
    if forked.returncode != 0:
        return [Finding(INVENTORY, 1, 'frozen-document',
                        f'cannot find where this checkout forked from {base}',
                        'fetch the full history (`git fetch --unshallow`, or `fetch-depth: 0` in CI) '
                        'or pass --base / set DOCS_BASE')]
    commit = forked.stdout.strip()
    blobs = {}
    for entry in _git(root, 'ls-tree', '-r', '-z', commit):
        meta, path = entry.split('\t', 1)
        mode, kind, sha = meta.split()
        if kind == 'blob' and _matches(path, dated):
            blobs[path] = sha
    findings = []
    present = sorted(path for path in blobs if path in tree.files)
    hashed = subprocess.run(['git', '-C', str(root), 'hash-object', '--', *present],
                            check=True, capture_output=True, text=True).stdout.split() if present else []
    current = dict(zip(present, hashed))
    for path, sha in sorted(blobs.items()):
        if current.get(path) == sha:
            continue
        old = subprocess.run(['git', '-C', str(root), 'cat-file', 'blob', sha],
                             check=True, capture_output=True).stdout.decode('utf-8', errors='replace')
        status = _status(old)
        if status and status.lower().startswith('proposed'):
            continue
        state = f'status "{status}"' if status else 'no status'
        if path not in tree.files:
            findings.append(Finding(path, 1, 'frozen-document',
                                    f'dated document ({state} where this branch forked from {base}) was removed or moved',
                                    SUPERSEDE.format(commit=commit[:12], path=path)))
            continue
        new = (root / path).read_text(encoding='utf-8', errors='replace')
        findings.append(Finding(path, _first_difference(old, new), 'frozen-document',
                                f'dated document ({state} where this branch forked from {base}) was edited',
                                SUPERSEDE.format(commit=commit[:12], path=path)))
    for path in sorted(tree.files):
        if path in blobs or not path.lower().endswith('.md') or not _matches(path, dated):
            continue
        header = _header((root / path).read_text(encoding='utf-8', errors='replace'))
        missing = [name for name, pattern in (('Date: YYYY-MM-DD', _DATE), ('Status: <status>', _STATUS))
                   if not pattern.search(header)]
        if missing:
            findings.append(Finding(path, 1, 'dated-header',
                                    f'new dated document has no {" or ".join(missing)} line',
                                    f'add {" and ".join(missing)} lines under the title, within the first '
                                    f'{HEADER_LINES} lines (status "proposed" keeps it editable)'))
    return findings


def check(root, base=None):
    """All findings; dated documents are compared with `base` only when one is given."""
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
    budgets, page_lines, budget_findings = load_budgets(root, pages)
    findings.extend(budget_findings)
    findings.extend(check_budgets(tree, pages, budgets, page_lines))
    if GLOSSARY in pages and GLOSSARY in tree.files:
        findings.extend(check_glossary(tree))
    if base is not None:
        findings.extend(check_frozen(tree, dated, base))
    return sorted(findings)


def main(argv=None, stdout=sys.stdout):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--root', default=str(ROOT))
    parser.add_argument('--base', default=os.environ.get('DOCS_BASE') or BASE,
                        help='git ref dated documents must match (default: $DOCS_BASE or origin/main)')
    args = parser.parse_args(argv)
    findings = check(args.root, args.base)
    for finding in findings:
        print(finding, file=stdout)
    if findings:
        print(f'{len(findings)} documentation problem(s); see scripts/docs.py and {INVENTORY}', file=stdout)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
