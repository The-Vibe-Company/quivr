"""Generate the public documentation site (Mintlify) into docs-site/.

docs-site/ is generated, never edited, and committed: Mintlify builds it from
the main branch (project subdirectory `/docs-site`). It holds docs.json, one MDX
page per living page of docs/inventory.toml at its route (scripts/mintlify_nav.py),
the images those pages show, and the bundled HTTP contract, nothing else, so
dated documents and source files are never published. The conversion:

- the page's first `# ` heading becomes the `title` front matter and is removed
  (Mintlify renders the title itself); the inventory summary, or the start
  page's introduction, becomes the `description`;
- outside code, `{`, `}` and `<` are written as HTML entities, because MDX reads
  them as expressions and tags (an expression silently vanishes); HTML comments
  are dropped, `<br>` is self-closed, `<details>` with a `<summary>` becomes an
  `<Accordion>`, and `<https://...>` becomes a Markdown link;
- headings keep their GitHub anchor as a custom ID, so `#anchors` keep working;
- a link to a living page points to its route; a link to the HTTP contract to
  the published `openapi.yaml`; a link to any other repository file (code,
  schemas, dated documents) becomes its path as code text, since public readers
  cannot open the private repository.

    python3 scripts/mintlify_site.py      # regenerate docs-site/ (make docs-site)

`make docs` fails with `stale-docs-site` when docs-site/ differs from what this
generates. The site settings (name, colours, menus) are SETTINGS below. Bundling
the contract needs PyYAML (contracts/http/v0/checks/requirements.txt).
"""
import argparse
import importlib.util
import json
import pathlib
import posixpath
import re
import sys

import docs
import mintlify_nav

SITE = 'docs-site'
CONTRACT = 'contracts/http/v0/openapi.yaml'
# docs.json without its navigation, which comes from the inventory.
SETTINGS = {
    '$schema': 'https://mintlify.com/docs.json',
    'theme': 'mint',
    'name': 'Quivr',
    'description': 'An open-source engine that turns continuous content streams into search and monitoring.',
    'colors': {'primary': '#5b3fd6', 'light': '#b29aff', 'dark': '#4b31be'},
    'api': {'playground': {'display': 'interactive'}},
    'contextual': {'options': ['copy', 'view', 'chatgpt', 'claude', 'mcp', 'cursor', 'vscode']},
}
IMAGES = ('.png', '.jpg', '.jpeg', '.gif', '.svg', '.webp')
# Inline HTML that MDX accepts once well formed; any other `<` is text.
TAGS = ('br', 'sup', 'sub', 'kbd', 'details', 'summary')

_TAG = re.compile(r'<(/?)([A-Za-z][A-Za-z0-9]*)\b[^<>]*?(/?)>')
_AUTOLINK = re.compile(r'<([A-Za-z][A-Za-z0-9+.-]*:[^\s<>]+)>')
_ESM = re.compile(r'^(\s{0,3})(import|export)\b')
_ATX = re.compile(r'^( {0,3}#{1,6}[ \t]+)(.*?)(?:[ \t]+#+)?[ \t]*$')
_SLUG = '\x00'  # marks where a heading's anchor goes until the heading is converted
_DETAILS = re.compile(r"^<details>[ \t]*\n<summary>(.*?)</summary>[ \t]*$", re.M)


class Site:
    """What a page may link to: the living pages and the repository files."""

    def __init__(self, root, pages):
        self.pages = pages
        self.tree = docs.Tree(root)
        self.images = {}  # repository path -> site path of an image a page shows

    def href(self, page, raw):
        """The site URL for link target `raw` of `page`, or None when it is not on the site."""
        target = docs._target(raw)
        if target is None:  # external, protocol-relative or same-page anchor
            return raw
        fragment = raw.split('#', 1)[1] if '#' in raw else ''
        resolved = docs._resolve(page, target)
        for candidate in (resolved, posixpath.join(resolved, 'README.md')):
            if candidate in self.pages:
                return mintlify_nav.url(candidate) + (f'#{fragment}' if fragment else '')
        if resolved == CONTRACT:
            return '/' + mintlify_nav.SPEC
        return None

    def image(self, page, raw):
        """The site URL of a repository image shown by `page`, published next to the pages."""
        target = docs._target(raw)
        if target is None:
            return raw
        resolved = docs._resolve(page, target)
        if resolved not in self.tree.files or not resolved.lower().endswith(IMAGES):
            return None
        path = 'images/' + (resolved[len('docs/'):] if resolved.startswith('docs/') else resolved)
        self.images[resolved] = path
        return '/' + path


# Inline scanning ---------------------------------------------------------------

def _code_end(text, start):
    """The end of the code span opening at `start`, or None when its backticks are literal."""
    run = len(text) - len(text[start:].lstrip('`'))
    ticks = run - start
    stop = text.find('\n\n', start)
    stop = len(text) if stop == -1 else stop
    closing = re.compile(r'(?<!`)`{%d}(?!`)' % ticks)
    match = closing.search(text, run, stop)
    return match.end() if match else None


def _bracket_end(text, start):
    """The index of the `]` closing the `[` at `start` within its paragraph, or None."""
    depth, index = 0, start
    while index < len(text):
        char = text[index]
        if text.startswith('\n\n', index):
            return None
        if char == '\\':
            index += 2
            continue
        if char == '`':
            end = _code_end(text, index)
            if end:
                index = end
                continue
        if char == '[':
            depth += 1
        elif char == ']':
            depth -= 1
            if depth == 0:
                return index
        index += 1
    return None


def _destination_end(text, start):
    """(target, index after the closing parenthesis) of a link destination starting at `start`."""
    target = docs._inline_target(text, start)
    if not target:
        return None
    index = start + len(target)
    title = re.compile(r'''\s+("[^"\n]*"|'[^'\n]*')''').match(text, index)
    if title:
        index = title.end()
    while text.startswith(' ', index):
        index += 1
    if not text.startswith(')', index):
        return None
    return target, index + 1


def _entity(char):
    return {'{': '&#123;', '}': '&#125;', '<': '&lt;'}.get(char, char)


def _plain(text):
    """Link text as plain words, for comparing it with a path."""
    return re.sub(r'[`*_]', '', text).strip().rstrip('/')


class Converter:
    def __init__(self, site, page):
        self.site, self.page = site, page

    def inline(self, text):
        """`text` (no fenced code) with links rewritten and MDX-unsafe characters escaped."""
        out, index = [], 0
        while index < len(text):
            char = text[index]
            if char == '\\' and index + 1 < len(text):
                out.append(text[index:index + 2])
                index += 2
                continue
            if char == '`':
                end = _code_end(text, index)
                if end:
                    out.append(text[index:end])
                    index = end
                    continue
                run = len(text) - len(text[index:].lstrip('`'))
                out.append(text[index:run])
                index = run
                continue
            if char == '[' or text.startswith('![', index):
                link = self.link(text, index)
                if link:
                    out.append(link[0])
                    index = link[1]
                    continue
            if text.startswith('<!--', index):
                end = text.find('-->', index + 4)
                index = len(text) if end == -1 else end + 3
                continue
            if char == '<':
                auto = _AUTOLINK.match(text, index)
                if auto:
                    out.append(f'[{auto.group(1)}]({auto.group(1)})')
                    index = auto.end()
                    continue
                tag = _TAG.match(text, index)
                if tag and (tag.group(2).lower() in TAGS or tag.group(2) == 'Accordion'):
                    name = tag.group(2).lower()
                    out.append('<br />' if name == 'br' else tag.group(0))
                    index = tag.end()
                    continue
            out.append(_entity(char))
            index += 1
        return ''.join(out)

    def link(self, text, start):
        """(replacement, end) for the link or image at `start`, or None when there is none."""
        image = text.startswith('!', start)
        opening = start + 1 if image else start
        close = _bracket_end(text, opening)
        if close is None or not text.startswith('(', close + 1):
            return None
        destination = _destination_end(text, close + 2)
        if destination is None:
            return None
        raw, end = destination
        label = self.inline(text[opening + 1:close])
        suffix = text[close + 2 + len(raw):end - 1]  # an optional title, kept as written
        if image:
            href = self.site.image(self.page, raw)
            if href is None:
                return (label or 'image'), end
            return f'![{label}]({href}{suffix})', end
        href = self.site.href(self.page, raw)
        if href is not None:
            return f'[{label}]({href}{suffix})', end
        target = docs._target(raw)
        path = docs._resolve(self.page, target)
        if raw.rstrip().endswith('/') or path in self.site.tree.dirs:
            path += '/'
        if '![' in label or '`' in label:
            return label, end
        if _plain(label) in (path.rstrip('/'), posixpath.basename(path.rstrip('/'))):
            return f'`{path}`', end
        return f'{label} (`{path}`)', end


def _blocks(lines):
    """(is_fenced, [lines]) runs of the page, fenced code kept apart."""
    blocks, fence, current = [], None, []
    for line in lines:
        match = docs._FENCE.match(line)
        if fence:
            current.append(line)
            if match and match.group(1)[0] == fence[0] and len(match.group(1)) >= len(fence) \
                    and not line.strip()[len(match.group(1)):].strip():
                blocks.append((True, current))
                fence, current = None, []
            continue
        if match:
            if current:
                blocks.append((False, current))
            fence, current = match.group(1), [line]
            continue
        current.append(line)
    if current:
        blocks.append((bool(fence), current))
    return blocks


def _accordions(text):
    def open_(match):
        title = re.sub(r'<[^>]+>', '', match.group(1)).strip().replace('"', '&quot;')
        return f'<Accordion title="{title}">'
    text = _DETAILS.sub(open_, text)
    return re.sub(r'^</details>[ \t]*$', '</Accordion>', text, flags=re.M)


def github_slug(heading, seen):
    """The anchor GitHub gives a heading, so links written for GitHub keep working on the site."""
    text = re.sub(r'!?\[([^\]]*)\]\([^)]*\)', r'\1', heading).replace('`', '')
    slug = ''.join(char for char in text.lower() if char.isalnum() or char in '_- ').replace(' ', '-')
    count = seen.get(slug, 0)
    seen[slug] = count + 1
    return f'{slug}-{count}' if count else slug


def _front_matter(title, description):
    lines = ['---', f'title: {json.dumps(title, ensure_ascii=False)}']
    if description:
        lines.append(f'description: {json.dumps(description, ensure_ascii=False)}')
    return '\n'.join(lines + ['---', ''])


def title_of(text):
    """(title, line index) of the page's first level-one heading outside code, or (None, None)."""
    for number, line in docs._lines_outside_fences(text):
        match = docs._HEADING.match(line)
        if match:
            return re.sub(r'`|\\(?=[\[\]])', '', match.group(1)).strip(), number - 1
    return None, None


def convert(site, page, text, description=None):
    """The MDX page for living page `page` whose Markdown is `text`."""
    lines = text.splitlines()
    title, heading = title_of(text)
    out, seen = [], {}
    if heading is not None:  # GitHub counts the title among the page's anchors
        github_slug(docs._HEADING.match(lines[heading]).group(1), seen)
        del lines[heading]
    for fenced, block in _blocks(lines):
        if fenced:
            out.extend(block)
            continue
        prepared = []
        for line in block:
            line = _ESM.sub(lambda m: f'{m.group(1)}&#{ord(m.group(2)[0])};{m.group(2)[1:]}', line)
            heading = _ATX.match(line)
            if heading and heading.group(2):
                line = heading.group(1) + heading.group(2) + _SLUG + github_slug(heading.group(2), seen)
            prepared.append(line)
        converted = Converter(site, page).inline(_accordions('\n'.join(prepared)))
        out.extend(re.sub(_SLUG + r'(\S*)$', r' {#\1}', line) for line in converted.split('\n'))
    body = '\n'.join(out).strip('\n')
    body = re.sub(r'\n{3,}', '\n\n', body)
    return _front_matter(title or mintlify_nav.route(page), description) + '\n' + body + '\n'


METHODS = ('get', 'put', 'post', 'delete', 'patch', 'head', 'options', 'trace')


def resource(path):
    """The endpoint group, as in docs/reference/http-api.md: the first path segment after the version."""
    segments = path.strip('/').split('/')
    if re.fullmatch(r'v\d+', segments[0]):
        segments = segments[1:] or ['']
    name = segments[0].replace('-', ' ')
    return name[:1].upper() + name[1:] if name else 'Root'


def humanize(operation_id):
    """`listRecords` as `List records`, the endpoint's title on the site."""
    words = re.findall(r'[A-Z]+[0-9]*(?![a-z])|[A-Z]?[a-z]+[0-9]*|[0-9]+', operation_id)
    text = ' '.join(word if word.isupper() and len(word) > 1 else word.lower() for word in words)
    return text[:1].upper() + text[1:]


def bundle(root):
    """The HTTP contract as one YAML document: Mintlify rejects references to other files.

    contracts/http/v0/bundle.py inlines the shared Manifest schemas. Operations are
    then grouped like docs/reference/http-api.md (a tag per resource) and titled
    from their operationId when they have no summary.
    """
    import yaml  # PyYAML, pinned in contracts/http/v0/checks/requirements.txt

    loader = importlib.util.spec_from_file_location(
        'contract_bundle', pathlib.Path(root) / posixpath.dirname(CONTRACT) / 'bundle.py')
    module = importlib.util.module_from_spec(loader)
    loader.loader.exec_module(module)
    spec = module.load()
    named = [(path, item, resource(path)) for path, item in (spec.get('paths') or {}).items()]
    named += [(name, item, 'Webhooks') for name, item in (spec.get('webhooks') or {}).items()]
    for path, item, group in named:
        for method, operation in item.items():
            if method in METHODS and isinstance(operation, dict):
                operation.setdefault('tags', [group])
                if 'summary' not in operation and operation.get('operationId'):
                    operation['summary'] = humanize(operation['operationId'])
    return yaml.safe_dump(spec, sort_keys=False, allow_unicode=True, width=120)


def description_of(page, entry):
    if entry['kind'] == 'start-page' and entry['audience'] in docs.READERS:
        return docs.READERS[entry['audience']][1]
    summary = entry.get('summary')
    if not summary:
        return None
    summary = re.sub(r'`', '', summary)
    return summary[:1].upper() + summary[1:]


def render(root, pages, spec=True):
    """({site path: text or bytes}, [(page, rule, message, fix)]) of the site for the living `pages`.

    `spec=False` leaves out the bundled contract (it needs PyYAML).
    """
    root = pathlib.Path(root)
    files, problems, routes = {}, [], {}
    for page in sorted(pages):
        key = mintlify_nav.route(page)
        if key in routes:
            problems.append((page, 'site-route', f'{page} and {routes[key]} would both be published at /{key}',
                             'rename one of them'))
        routes[key] = page
    site = Site(root, pages)
    for page in sorted(pages):
        path = root / page
        if not path.is_file():
            continue  # reported as missing-page
        text = path.read_text(encoding='utf-8')
        if title_of(text)[0] is None:
            problems.append((page, 'missing-title', 'the page has no `# ` title, which the site uses as its title',
                             'start the page with a `# Title` line'))
        files[mintlify_nav.route(page) + '.mdx'] = convert(site, page, text, description_of(page, pages[page]))
    for source, published in sorted(site.images.items()):
        files[published] = (root / source).read_bytes()
    config = dict(SETTINGS)
    config['navigation'] = mintlify_nav.navigation(pages, docs.READERS, docs.SECTIONS, docs.KINDS)
    files['docs.json'] = json.dumps(config, indent=2, ensure_ascii=False) + '\n'
    if spec:
        files[mintlify_nav.SPEC] = bundle(root)
    return files, problems


def committed(root):
    """The files under docs-site/ of the checkout, as site paths."""
    tree = docs.Tree(root)
    return sorted(name[len(SITE) + 1:] for name in tree.files if name.startswith(SITE + '/'))


def write(root):
    """Regenerate docs-site/; return (written or removed site paths, problems)."""
    root = pathlib.Path(root)
    pages, _, _, _, findings = docs.load_inventory(root)
    if pages is None:
        return [], [(docs.INVENTORY, f.rule, f.message, f.fix) for f in findings]
    files, problems = render(root, pages)
    changed = []
    out = root / SITE
    for name in committed(root):
        if name not in files:
            (out / name).unlink()
            changed.append(name)
    for name, content in sorted(files.items()):
        path = out / name
        data = content.encode('utf-8') if isinstance(content, str) else content
        if path.is_file() and path.read_bytes() == data:
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        changed.append(name)
    for folder in sorted((p for p in out.rglob('*') if p.is_dir()), reverse=True):
        if not any(folder.iterdir()):
            folder.rmdir()
    return changed, problems


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--root', default=str(docs.ROOT))
    args = parser.parse_args(argv)
    changed, problems = write(args.root)
    for name in changed:
        print(f'wrote {SITE}/{name}')
    for page, rule, message, fix in problems:
        print(f'{page}: [{rule}] {message}. Fix: {fix}.')
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main())
