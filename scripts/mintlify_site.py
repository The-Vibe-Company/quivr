"""The generated pages of the documentation site (Mintlify), and the checks of the whole site.

docs-site/ is the public documentation site: Mintlify builds it from the main
branch (project subdirectory `/docs-site`). Its MDX pages, images and
`docs.json` (settings and navigation) are written by hand and reviewed like
code. A few files are generated from contracts so they cannot drift; never edit
them, run `make docs-site` instead:

    openapi.yaml                    the HTTP contract, bundled into one file (the API reference)
    reference/cli.mdx               docs/reference/cli.md, from `make generate`
    reference/mcp.mdx               docs/reference/mcp.md, from `make generate`
    reference/plugin-manifest.mdx   contracts/plugins/v0/plugin-manifest.schema.json
    reference/plugin-protocol.mdx   the operation, error and fixture schemas of contracts/plugins/v0/

`make docs` (scripts/docs.py) runs `check`, which fails when:

    stale-docs-site      a generated file differs from what this script writes
    site-navigation      a page is missing from docs.json, or docs.json names a missing page
    site-link            a root-relative link or image of a page does not resolve
    site-configuration   a configuration key of the engine is missing from reference/configuration.mdx

    python3 scripts/mintlify_site.py      # regenerate the generated files (make docs-site)

A page may also carry runnable blocks, replayed by `make verify` (scripts/guides.py).
Bundling the contract needs PyYAML (contracts/http/v0/checks/requirements.txt).
"""
import argparse
import importlib.util
import json
import pathlib
import posixpath
import re
import sys

import docs

SITE = 'docs-site'
CONTRACT = 'contracts/http/v0/openapi.yaml'
SPEC = 'openapi.yaml'  # the bundled contract, in the site
SITE_URL = 'https://docs.quivr.thevibecompany.co'
PLUGIN_SCHEMAS = 'contracts/plugins/v0'
# Repository pages published on the site after conversion, by site path.
CONVERTED = {'reference/cli.mdx': 'docs/reference/cli.md', 'reference/mcp.mdx': 'docs/reference/mcp.md'}
CONVERTED_DESCRIPTIONS = {'reference/cli.mdx': 'Every quivr command, what it needs to run, its flags and exit codes.',
                          'reference/mcp.mdx': 'Every MCP profile and tool an AI agent can use, with its arguments.'}
# How the site introduces the HTTP contract: its info block is written for contributors.
API_INFO = {'title': 'Quivr HTTP API',
            'description': 'Every endpoint of the Quivr v0 HTTP API. Send an API key as a bearer token; '
                           'the key decides the Organization, the actions and the Corpora a request may reach.'}
IMAGES = ('.png', '.jpg', '.jpeg', '.gif', '.svg', '.webp')
# Inline HTML that MDX accepts once well formed; any other `<` is text.
TAGS = ('br', 'sup', 'sub', 'kbd', 'details', 'summary')

_TAG = re.compile(r'<(/?)([A-Za-z][A-Za-z0-9]*)\b[^<>]*?(/?)>')
_AUTOLINK = re.compile(r'<([A-Za-z][A-Za-z0-9+.-]*:[^\s<>]+)>')
_ESM = re.compile(r'^(\s{0,3})(import|export)\b')
_ATX = re.compile(r'^( {0,3}#{1,6}[ \t]+)(.*?)(?:[ \t]+#+)?[ \t]*$')
_SLUG = '\x00'  # marks where a heading's anchor goes until the heading is converted
_DETAILS = re.compile(r"^<details>[ \t]*\n<summary>(.*?)</summary>[ \t]*$", re.M)
_NOTICE = re.compile(r'^> Generated from .*\n\n?', re.M)  # the contributor notice of a generated page


class Site:
    """What a converted page may link to: the other converted pages and the site itself."""

    def __init__(self, root):
        self.tree = docs.Tree(root)
        self.routes = {source: '/' + path[:-len('.mdx')] for path, source in CONVERTED.items()}
        self.images = {}  # repository path -> site path of an image a page shows

    def href(self, page, raw):
        """The site URL for link target `raw` of `page`, or None when it is not on the site."""
        if raw.startswith(SITE_URL):
            return raw[len(SITE_URL):] or '/'
        target = docs._target(raw)
        if target is None:  # external, protocol-relative or same-page anchor
            return raw
        fragment = raw.split('#', 1)[1] if '#' in raw else ''
        resolved = docs._resolve(page, target)
        if resolved in self.routes:
            return self.routes[resolved] + (f'#{fragment}' if fragment else '')
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
    return _front_matter(title or page, description) + '\n' + body + '\n'


# The HTTP contract ----------------------------------------------------------------

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
    spec['info'] = {**spec.get('info', {}), **API_INFO}
    named = [(path, item, resource(path)) for path, item in (spec.get('paths') or {}).items()]
    named += [(name, item, 'Webhooks') for name, item in (spec.get('webhooks') or {}).items()]
    for path, item, group in named:
        for method, operation in item.items():
            if method in METHODS and isinstance(operation, dict):
                operation.setdefault('tags', [group])
                if 'summary' not in operation and operation.get('operationId'):
                    operation['summary'] = humanize(operation['operationId'])
    return yaml.safe_dump(spec, sort_keys=False, allow_unicode=True, width=120)


# The plugin contract ---------------------------------------------------------------

_ROUTE = re.compile(r'^(?:200 (?:body|answer) of )?(GET|POST) (/v0/[a-z_/]+)')
# Headings of the manifest sections, in page order; other keys follow under their own name.
MANIFEST_SECTIONS = [
    ('identity', 'Identity and compatibility', ('id', 'version', 'description', 'compatibility')),
    ('contributions.normalizer', 'Normalizer', ()),
    ('contributions.subscription', 'Alert rule (`subscription`)', ()),
    ('contributions.connector', 'Connector', ()),
    ('contributions.ingestion', 'Ingestion', ()),
    ('contributions.retrieval', 'Retrieval', ()),
    ('contributions', 'Reserved contributions', ()),
    ('configuration', 'Configuration, secrets and extensions', ('configuration', 'secrets', 'extensions')),
    ('run', 'Local development', ('run',)),
]
PROTOCOL_SECTIONS = [('', 'Discovery and health'), ('normalizer', 'Normalizer'),
                     ('subscription', 'Alert rule (`subscription`)'), ('connector', 'Connector'),
                     ('ingestion', 'Ingestion'), ('retrieval', 'Retrieval')]
GENERATED_NOTE = ('{{/* Generated by scripts/mintlify_site.py from {source}. Do not edit: '
                  'change the source and run `make docs-site`. */}}')


def _load(root, name):
    return json.loads((pathlib.Path(root) / PLUGIN_SCHEMAS / name).read_text(encoding='utf-8'))


def _deref(node, defs):
    """`node` with a local `$ref` replaced by what it points at (sibling keywords win)."""
    if isinstance(node, dict) and str(node.get('$ref', '')).startswith('#/$defs/'):
        target = defs
        for part in node['$ref'][len('#/$defs/'):].split('/'):
            target = target.get(part, {}) if isinstance(target, dict) else {}
        target = _deref(target, defs)
        return {**target, **{k: v for k, v in node.items() if k != '$ref'}} if isinstance(target, dict) else target
    return node


def _type(node, defs):
    node = _deref(node, defs)
    if node is False:
        return 'reserved'
    if isinstance(node, dict) and '#/$defs/' in str(node.get('$ref', '')) and not node['$ref'].startswith('#'):
        return f'`{node["$ref"].rsplit("/", 1)[1]}` (shared Manifest schema)'
    if not isinstance(node, dict) or node is True:
        return 'any'
    if 'const' in node:
        return f'`{json.dumps(node["const"])}`'
    if 'enum' in node:
        return ', '.join(f'`{json.dumps(v) if not isinstance(v, str) else v}`' for v in node['enum'])
    kind = node.get('type')
    if isinstance(kind, list):
        return ' or '.join(kind)
    if kind == 'array':
        return f'array of {_type(node.get("items", True), defs)}'
    if kind == 'object' and isinstance(node.get('additionalProperties'), dict) and not node.get('properties'):
        return f'map of {_type(node["additionalProperties"], defs)}'
    for key in ('oneOf', 'anyOf'):
        if key in node:
            kinds = []
            for option in node[key]:
                name = _type(option, defs)
                if name not in kinds:
                    kinds.append(name)
            return ' or '.join(kinds)
    return kind or 'any'


def _limits(node):
    """The bounds and default of a field, in words."""
    words = []
    pairs = [('minimum', 'maximum', ''), ('minLength', 'maxLength', ' characters'), ('minItems', 'maxItems', ' items'),
             ('minProperties', 'maxProperties', ' entries')]
    for low, high, unit in pairs:
        if low in node and high in node:
            words.append(f'{node[low]} to {node[high]}{unit}.')
        elif high in node:
            words.append(f'At most {node[high]}{unit}.')
        elif low in node and node[low] not in (0, 1):
            words.append(f'At least {node[low]}{unit}.')
    if 'default' in node:
        words.append(f'Default `{json.dumps(node["default"])}`.')
    return words


def _fields(schema, defs, prefix='', depth=0, max_depth=6):
    """Rows (path, type, required, description) of every property of `schema`, nested ones included."""
    schema = _deref(schema, defs)
    if not isinstance(schema, dict):
        return
    required = set(schema.get('required', ()))
    for name, raw in (schema.get('properties') or {}).items():
        node = _deref(raw, defs)
        path = prefix + name
        if node is False:
            yield path, 'reserved', False, 'Reserved: a document that sets it is rejected.'
            continue
        node = node if isinstance(node, dict) else {}
        text = (node.get('description') or '').strip()
        if text and text[-1] not in '.:)':
            text += '.'
        about = ' '.join(filter(None, [text] + _limits(node)))
        yield path, _type(node, defs), name in required, about
        if depth >= max_depth:
            continue
        if node.get('properties'):
            yield from _fields(node, defs, path + '.', depth + 1, max_depth)
        elif node.get('type') == 'object' and isinstance(node.get('additionalProperties'), dict):
            value = _deref(node['additionalProperties'], defs)
            if isinstance(value, dict) and value.get('properties'):
                yield from _fields(value, defs, f'{path}.<name>.', depth + 1, max_depth)
        elif node.get('type') == 'array':
            item = _deref(node.get('items', {}), defs)
            if isinstance(item, dict) and item.get('properties'):
                yield from _fields(item, defs, path + '[].', depth + 1, max_depth)


def _cell(text):
    """Table-cell text: MDX-safe outside code, pipes escaped, on one line."""
    parts = re.split(r'(`[^`]*`)', ' '.join(str(text).split()))
    safe = ''.join(part if part.startswith('`') else ''.join(_entity(char) for char in part) for part in parts)
    return safe.replace('|', '\\|')


def _table(rows):
    out = ['| Field | Type | Required | Description |', '| --- | --- | --- | --- |']
    for path, kind, required, about in rows:
        out.append(f'| `{path}` | {_cell(kind)} | {"yes" if required else "no"} | {_cell(about) or "—"} |')
    return out


def _page(title, description, source, body):
    return '\n'.join([_front_matter(title, description), GENERATED_NOTE.format(source=source), '', *body]).rstrip() + '\n'


def plugin_manifest(root):
    """reference/plugin-manifest.mdx: every field of quivr-plugin.yaml, from its JSON Schema."""
    schema = _load(root, 'plugin-manifest.schema.json')
    defs = schema.get('$defs', {})
    rows = list(_fields(schema, defs))
    body = ['Every field of `quivr-plugin.yaml`, the manifest a plugin ships. It is written in YAML and validated '
            'as the equivalent JSON value against `plugin-manifest.schema.json`; `quivr plugin inspect` checks it '
            'the way the engine does. Unknown fields are rejected.', '']
    used = set()
    for key, heading, tops in MANIFEST_SECTIONS:
        if tops:
            section = [row for row in rows if row[0].split('.')[0] in tops]
        elif key == 'contributions':
            section = [row for row in rows if row[0].startswith('contributions.') and row[0].count('.') == 1
                       and row[1] == 'reserved']
        else:
            section = [row for row in rows if row[0] == key or row[0].startswith(key + '.')]
        section = [row for row in section if row[0] not in used]
        if not section:
            continue
        used.update(row[0] for row in section)
        body += [f'## {heading}', '', *_table(section), '']
    rest = [row for row in rows if row[0] not in used and row[0] != 'contributions']
    if rest:
        body += ['## Other fields', '', *_table(rest), '']
    return _page('Plugin manifest', 'Every field of quivr-plugin.yaml, generated from its JSON Schema.',
                 f'{PLUGIN_SCHEMAS}/plugin-manifest.schema.json', body)


def plugin_protocol(root):
    """reference/plugin-protocol.mdx: the routes a plugin serves, their request and response fields,
    the error envelope and the fixtures, from the schemas."""
    folder = pathlib.Path(root) / PLUGIN_SCHEMAS
    names = sorted(path.name for path in folder.glob('*.schema.json'))
    operations = {}  # contribution -> [(method, route, request schema or None, response schema or None)]
    for name in names:
        if name.endswith('-request.schema.json') or name in ('discovery.schema.json', 'health.schema.json'):
            schema = _load(root, name)
            match = _ROUTE.match(schema.get('description', ''))
            if not match:
                continue
            method, route = match.groups()
            route = route.rstrip('/.')
            response = name.replace('-request.', '-response.') if name.endswith('-request.schema.json') else name
            request = name if name.endswith('-request.schema.json') else None
            contribution = route.split('/')[3] if route.startswith('/v0/contributions/') else ''
            operations.setdefault(contribution, []).append(
                (method, route, request, response if (folder / response).is_file() else None))
    body = ['The HTTP routes a plugin serves, with every field of their JSON bodies, the error envelope and the '
            'fixture files `quivr plugin test` reads. A plugin serves JSON over HTTP; every route is under `/v0`, '
            'the major version of the Plugin API. The schemas in `contracts/plugins/v0/` are the source of truth; '
            'the SDKs and `quivr plugin test` implement them.', '']
    history = (folder / 'README.md').read_text().split('<!-- plugin-api: generated by make generate -->', 1)[1].split('<!-- /plugin-api -->', 1)[0].strip()
    body += ['## Plugin API versions', '', history, '']
    _, trace_marker, propagation = (folder / 'README.md').read_text().partition('<!-- trace-context: published in the generated protocol reference -->')
    if trace_marker:
        body += ['## Trace context', '', propagation.split('<!-- /trace-context -->', 1)[0].strip(), '']
    _, separator, authentication = (folder / 'README.md').read_text().partition('<!-- engine-auth: published in the generated protocol reference -->')
    if separator:
        body += ['## Signed engine requests', '', authentication.split('<!-- /engine-auth -->', 1)[0].strip(), '']
    for key, heading in PROTOCOL_SECTIONS:
        if key not in operations:
            continue
        body += [f'## {heading}', '']
        for method, route, request, response in sorted(operations[key], key=lambda op: op[1]):
            body += [f'### `{method} {route}`', '']
            about = _load(root, request or response).get('description', '')
            about = _ROUTE.sub('', about).lstrip(' .,:')
            if about and not re.match(r'\w*_', about):
                about = about[:1].upper() + about[1:]
            if about:
                body += [_cell(about), '']
            for label, name in (('Request body', request), ('Response body (200)', response)):
                if not name:
                    continue
                schema = _load(root, name)
                rows = list(_fields(schema, schema.get('$defs', {}), max_depth=2))
                body += [f'**{label}** (`{name}`)', '', *(_table(rows) if rows else ['No fields.']), '']
    error = _load(root, 'error.schema.json')
    body += ['## Errors', '', _cell(error.get('description', '')), '',
             *_table(list(_fields(error, error.get('$defs', {})))), '']
    fixtures = [name for name in names if name.endswith('fixture.schema.json')]
    if fixtures:
        body += ['## Fixtures', '', 'Local test inputs that `quivr plugin dev` and `quivr plugin test` turn into '
                 'requests. A plugin keeps its own in `fixtures/*.json`.', '']
        for name in fixtures:
            schema = _load(root, name)
            title = name[:-len('.schema.json')].replace('plugin-fixture', 'normalizer-fixture').replace('-', ' ')
            body += [f'### {title[:1].upper() + title[1:]}', '', _cell(schema.get('description', '')), '',
                     f'Schema: `{name}`.', '', *_table(list(_fields(schema, schema.get('$defs', {}), max_depth=1))), '']
    return _page('Plugin protocol', 'The routes a plugin serves, their fields, errors and fixtures, generated from the '
                 'schemas.', f'{PLUGIN_SCHEMAS}/*.schema.json', body)


# The site --------------------------------------------------------------------------

def render(root, spec=True):
    """{site path: text} of every generated file. `spec=False` leaves out the bundled contract (it needs PyYAML)."""
    root = pathlib.Path(root)
    files = {}
    site = Site(root)
    for path, source in CONVERTED.items():
        text = (root / source).read_text(encoding='utf-8') if (root / source).is_file() else None
        if text is not None:
            page = convert(site, source, _NOTICE.sub('', text), CONVERTED_DESCRIPTIONS.get(path))
            head, _, rest = page.partition('---\n\n')
            files[path] = head + '---\n\n' + GENERATED_NOTE.format(source=source) + '\n\n' + rest
    if (root / PLUGIN_SCHEMAS / 'plugin-manifest.schema.json').is_file():
        files['reference/plugin-manifest.mdx'] = plugin_manifest(root)
        files['reference/plugin-protocol.mdx'] = plugin_protocol(root)
    for source, published in sorted(site.images.items()):
        files[published] = (root / source).read_bytes()
    if spec:
        files[SPEC] = bundle(root)
    return files


def _navigation(value):
    """Every page named in a docs.json navigation value."""
    if isinstance(value, str):
        yield value
    elif isinstance(value, list):
        for item in value:
            yield from _navigation(item)
    elif isinstance(value, dict):
        for key in ('pages', 'groups', 'tabs', 'anchors', 'dropdowns'):
            if key in value:
                yield from _navigation(value[key])


_LINK = re.compile(r'\]\((/[^)\s]*)\)|\b(?:href|src)="(/[^"]*)"')


def _resolves(out, target):
    path = target.split('#', 1)[0].split('?', 1)[0].strip('/')
    if path in ('', 'index'):
        return (out / 'index.mdx').is_file()
    return any((out / candidate).is_file() for candidate in (path + '.mdx', path + '/index.mdx', path))


def site_pages(root):
    """The site path, without `.mdx`, of every page in docs-site/ (snippets are not pages)."""
    out = pathlib.Path(root) / SITE
    return sorted(path.relative_to(out).with_suffix('').as_posix() for path in out.rglob('*.mdx')
                  if not path.relative_to(out).as_posix().startswith('snippets/'))


# Engine configuration structs a reader sets in QUIVR_CONFIG, documented in reference/configuration.mdx.
CONFIGURATION = [('internal/app/run.go', 'Config'), ('internal/plugins/pin.go', 'PinConfig'),
                 ('internal/plugins/pin.go', 'RouteConfig'), ('internal/adapters/s3/blobs.go', 'Config'),
                 ('internal/corpus/corpus.go', 'Scope'), ('internal/monitoring/monitoring.go', 'Destination'),
                 ('internal/app/monitoring.go', 'DeliveryConfig'), ('internal/app/prune.go', 'ChangePruneConfig'),
                 ('internal/observability/recorder.go', 'Config')]
CONFIGURATION_PAGE = 'reference/configuration.mdx'
_JSON_TAG = re.compile(r'`json:"([a-z0-9_]+)[,"]')


def configuration_keys(root):
    """[(file, struct, key)] of the JSON keys of the configuration structs."""
    keys = []
    for file, struct in CONFIGURATION:
        path = pathlib.Path(root) / file
        if not path.is_file():
            continue
        match = re.search(r'^type ' + struct + r' struct \{\n(.*?)^\}', path.read_text(encoding='utf-8'), re.M | re.S)
        if match:
            keys += [(file, struct, key) for key in _JSON_TAG.findall(match.group(1))]
    return keys


def check(root):
    """[(path, rule, message, fix)] of the site in checkout `root`."""
    root = pathlib.Path(root)
    out = root / SITE
    if not out.is_dir():
        return []
    problems = []
    regenerate = f'run `make docs-site` and commit the result; never edit a generated file by hand'
    try:
        files = render(root)
    except ModuleNotFoundError as error:
        return [(SITE, 'stale-docs-site', f'cannot generate the site to compare it ({error})',
                 'install the pinned tools with `pip install -r contracts/http/v0/checks/requirements.txt`')]
    for name, content in sorted(files.items()):
        data = content.encode('utf-8') if isinstance(content, str) else content
        if not (out / name).is_file() or (out / name).read_bytes() != data:
            problems.append((f'{SITE}/{name}', 'stale-docs-site', 'this generated file is out of date', regenerate))
    try:
        config = json.loads((out / 'docs.json').read_text(encoding='utf-8'))
    except (OSError, ValueError) as error:
        return problems + [(f'{SITE}/docs.json', 'site-navigation', f'cannot read docs.json ({error})',
                            'restore a valid docs.json')]
    listed = set(_navigation(config.get('navigation', {})))
    pages = site_pages(root)
    for page in pages:
        if page not in listed:
            problems.append((f'{SITE}/{page}.mdx', 'site-navigation', 'this page is not in the navigation',
                             f'add "{page}" to a group of docs-site/docs.json, or delete the page'))
    for entry in sorted(listed):
        if entry.endswith(('.yaml', '.json')) or entry.startswith(('http://', 'https://')):
            continue
        if entry not in pages and f'{entry}.mdx' not in files:  # a missing generated page is already stale
            problems.append((f'{SITE}/docs.json', 'site-navigation', f'the navigation names "{entry}", which has no page',
                             f'write docs-site/{entry}.mdx, or remove the entry'))
    for page in pages:
        text = (out / f'{page}.mdx').read_text(encoding='utf-8')
        for number, line in docs._lines_outside_fences(text):
            for match in _LINK.finditer(line):
                target = match.group(1) or match.group(2)
                if not _resolves(out, target):
                    problems.append((f'{SITE}/{page}.mdx', 'site-link', f'line {number}: {target} is not a page or '
                                     'file of the site', 'link to an existing page, such as /quickstart'))
    documented = (out / CONFIGURATION_PAGE).read_text(encoding='utf-8') if (out / CONFIGURATION_PAGE).is_file() else None
    if documented is not None:
        for file, struct, key in configuration_keys(root):
            if f'`{key}`' not in documented:
                problems.append((f'{SITE}/{CONFIGURATION_PAGE}', 'site-configuration',
                                 f'`{key}` ({struct} in {file}) is not documented',
                                 f'describe `{key}` in the table of {struct} on that page'))
    return problems


def write(root):
    """Regenerate the generated files of docs-site/; return the site paths written."""
    root = pathlib.Path(root)
    written = []
    for name, content in sorted(render(root).items()):
        path = root / SITE / name
        data = content.encode('utf-8') if isinstance(content, str) else content
        if path.is_file() and path.read_bytes() == data:
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        written.append(name)
    return written


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--root', default=str(docs.ROOT))
    args = parser.parse_args(argv)
    for name in write(args.root):
        print(f'wrote {SITE}/{name}')
    problems = check(args.root)
    for path, rule, message, fix in problems:
        print(f'{path}: [{rule}] {message}. Fix: {fix}.')
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main())
