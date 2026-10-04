#!/usr/bin/env python3
"""Replay the runnable blocks of the docs against a running Quivr.

A page of the documentation site (docs-site/*.mdx) or of docs/inventory.toml
may mark shell blocks as runnable and follow each one with the output it
expects; docs/runnable-guides.md is the authors' guide to the convention.
`make verify` replays every such page in its own Organization after the timed
scenarios (scripts/local.py), and a failure names the page, the block and what
differed.

    python3 scripts/guides.py --list      # the runnable blocks of every page
    QUIVR_API_URL=http://127.0.0.1:<port> QUIVR_API_KEY=<key> [QUIVR_DESTINATION=<id>] \
      python3 scripts/guides.py [page ...]   # replay against a running stack
"""
import argparse
import base64
import dataclasses
import hashlib
import json
import os
import pathlib
import re
import secrets
import subprocess
import sys
import tempfile
import time
import tomllib

ROOT = pathlib.Path(__file__).resolve().parent.parent
INVENTORY = 'docs/inventory.toml'
SITE = 'docs-site'
SHELLS = ('sh', 'bash', 'shell')
ANY = '...'
RETRY_SECONDS = 60
COMMAND_TIMEOUT = 30
# What a page's own Organization may do: everything an integrator's full key can.
ACTIONS = ['corpora:read', 'corpora:write', 'content:read', 'content:write', 'search:query', 'blobs:read', 'blobs:write',
           'changes:read', 'monitoring:read', 'monitoring:write', 'projections:rebuild', 'operations:read', 'operations:write',
           'connectors:read', 'connectors:write', 'connectors:admin', 'connector:push']

_FENCE = re.compile(r'^\s{0,3}(`{3,}|~{3,})(.*)$')
_BIND = re.compile(r'^\{\{([A-Z][A-Z0-9_]*)\}\}$')
_BOUND = re.compile(r'"\{\{([A-Z][A-Z0-9_]*)\}\}"')
_USED = re.compile(r'\$\{?([A-Z][A-Z0-9_]*)')
_EXPORTED = re.compile(r'\b([A-Z][A-Z0-9_]*)=')
# The only environment a block sees besides the values it keeps; a kept value may not replace it.
ENVIRONMENT = ('PATH', 'HOME', 'LANG', 'TMPDIR')
# What a page's blocks get from the stack: the API address, a key and the page's webhook destination.
STACK = ('QUIVR_API_URL', 'QUIVR_API_KEY', 'QUIVR_DESTINATION')
RESERVED = ENVIRONMENT + STACK
# In MDX, a comment on the line before a fence marks it, since fence words would show as its title.
_MARK = re.compile(r'^\s*\{/\*\s*(runnable(?: retry)?|output)\s*\*/\}\s*$')


class GuideError(ValueError):
    """A page whose runnable blocks cannot be read."""


@dataclasses.dataclass
class Block:
    guide: str
    number: int
    line: int
    command: str
    retry: bool = False
    expected: str | None = None
    json: bool = False
    # Set once another fence follows: a later output block no longer belongs to this command.
    closed: bool = False

    def where(self):
        return f'{self.guide}:{self.line}: block {self.number}'


def _closes(line, fence):
    stripped = line.strip()
    return stripped.startswith(fence) and set(stripped) == {fence[0]}


def blocks(guide, text):
    """The runnable blocks of one page, each with the output fence that follows it.

    A value a block keeps and a later block uses must also be exported by one of the
    reader's own shell blocks before that use, or a reader copying the page would miss it."""
    found, lines, index, kept, exported = [], text.splitlines(), 0, set(), set()
    while index < len(lines):
        opening = _FENCE.match(lines[index])
        index += 1
        if not opening:
            continue
        fence, words, line = opening.group(1), opening.group(2).split(), index
        body = []
        while index < len(lines) and not _closes(lines[index], fence):
            body.append(lines[index])
            index += 1
        index += 1
        content = '\n'.join(body) + '\n'
        if len(words) >= 2 and words[1] == 'output':
            if not found or found[-1].closed:
                raise GuideError(f'{guide}:{line}: an output block must directly follow a runnable block')
            found[-1].expected, found[-1].json, found[-1].closed = content, words[0] == 'json', True
            for name in _BOUND.findall(content):
                if name in RESERVED:
                    raise GuideError(f'{guide}:{line}: {{{{{name}}}}} would replace the ${name} every block runs with; rename it')
                kept.add(name)
        elif words and words[0] in SHELLS and 'runnable' in words[1:]:
            if found:
                found[-1].closed = True
            block = Block(guide, len(found) + 1, line, content, retry='retry' in words[1:])
            for name in _USED.findall(content):
                if name in kept and name not in exported:
                    raise GuideError(f'{block.where()}: uses ${name}, which readers are never told to set; '
                                     f'add a sh block with `export {name}=<…>` before it')
            found.append(block)
        else:
            if words and words[0] in SHELLS:
                exported.update(_EXPORTED.findall(content))
            if found:
                found[-1].closed = True
    return found


def markdown(text):
    """An MDX page with its `{/* runnable */}` and `{/* output */}` marks moved onto the fences they
    precede, as a Markdown page writes them; line numbers are kept."""
    lines = text.splitlines()
    for index, line in enumerate(lines):
        mark = _MARK.match(line)
        if not mark or index + 1 >= len(lines) or not (fence := _FENCE.match(lines[index + 1])):
            continue
        words = fence.group(2).split()
        lines[index] = ''
        lines[index + 1] = lines[index + 1][:fence.start(2)] + ' '.join(words[:1] + [mark.group(1)])
    return '\n'.join(lines) + '\n'


def pages(root=ROOT):
    """Every page with runnable blocks: the declared Markdown pages in inventory order, then the site's pages."""
    root = pathlib.Path(root)
    try:
        declared = list(tomllib.loads((root / INVENTORY).read_text()).get('pages', {}))
    except (OSError, tomllib.TOMLDecodeError) as error:
        raise GuideError(f'{INVENTORY}: {error}') from error
    site = sorted(path.relative_to(root).as_posix() for path in (root / SITE).rglob('*.mdx')) if (root / SITE).is_dir() else []
    runnable = {}
    for path in declared + site:
        file = root / path
        if not file.is_file():
            continue
        text = file.read_text()
        if (found := blocks(path, markdown(text) if path.endswith('.mdx') else text)):
            runnable[path] = found
    return runnable


def match(expected, actual, bindings, at='$'):
    """Why `actual` does not match `expected`, or None. Binds new {{NAME}} placeholders."""
    if expected == ANY:
        return None
    if isinstance(expected, str) and (bind := _BIND.match(expected)):
        name = bind.group(1)
        if name in bindings:
            return None if actual == bindings[name] else f'{at}: expected {name} = {json.dumps(bindings[name])}, got {json.dumps(actual)}'
        if not isinstance(actual, (str, int, float)) or isinstance(actual, bool):
            return f'{at}: {name} must be a string or a number, got {json.dumps(actual)}'
        bindings[name] = actual
        return None
    if isinstance(expected, dict):
        if not isinstance(actual, dict):
            return f'{at}: expected an object, got {json.dumps(actual)}'
        for key, value in expected.items():
            if key not in actual:
                return f'{at}.{key}: missing'
            if (why := match(value, actual[key], bindings, f'{at}.{key}')):
                return why
        return None
    if isinstance(expected, list):
        if not isinstance(actual, list):
            return f'{at}: expected an array, got {json.dumps(actual)}'
        more = bool(expected) and expected[-1] == ANY
        items = expected[:-1] if more else expected
        if len(actual) < len(items) or (not more and len(actual) != len(items)):
            return f'{at}: expected {len(items)}{" or more" if more else ""} items, got {len(actual)}'
        for position, value in enumerate(items):
            if (why := match(value, actual[position], bindings, f'{at}[{position}]')):
                return why
        return None
    same = actual == expected and isinstance(actual, bool) == isinstance(expected, bool)
    return None if same else f'{at}: expected {json.dumps(expected)}, got {json.dumps(actual)}'


def attempt(block, env, cwd, bindings):
    """Run one block once: (why it failed or None, the bindings after it)."""
    added = dict(bindings)
    try:
        done = subprocess.run(['bash', '-eo', 'pipefail', '-c', block.command], cwd=cwd, capture_output=True, text=True,
                              timeout=COMMAND_TIMEOUT, env={**env, **{k: str(v) for k, v in bindings.items()}})
    except subprocess.TimeoutExpired:
        return f'no answer within {COMMAND_TIMEOUT}s', added
    if done.returncode:
        return f'exit status {done.returncode}: {_tail(done.stderr or done.stdout)}', added
    if block.expected is None:
        return None, added
    if not block.json:
        same = done.stdout.strip() == block.expected.strip()
        return (None if same else f'expected output {_tail(block.expected)}, got {_tail(done.stdout)}'), added
    try:
        expected = json.loads(block.expected)
    except ValueError as error:
        return f'the expected output is not JSON ({error})', added
    try:
        actual = json.loads(done.stdout)
    except ValueError:
        return f'output is not JSON: {_tail(done.stdout)}', added
    why = match(expected, actual, added)
    return (f'{why}; output {_tail(done.stdout)}' if why else None), added


def replay(found, env, retry_seconds=RETRY_SECONDS, pause=1.0):
    """Replay one page's blocks in order; stop at the first failure, which later blocks depend on."""
    results, bindings = [], {}
    with tempfile.TemporaryDirectory(prefix='quivr-guide-') as cwd:
        for block in found:
            start, attempts = time.monotonic(), 0
            while True:
                attempts += 1
                why, added = attempt(block, env, cwd, bindings)
                if why is None or not block.retry or time.monotonic() - start >= retry_seconds:
                    break
                time.sleep(pause)
            result = {'guide': block.guide, 'block': block.number, 'line': block.line, 'attempts': attempts,
                      'seconds': round(time.monotonic() - start, 3), 'status': 'failed' if why else 'passed'}
            results.append(result)
            if why:
                result['error'] = f'{block.where()}: {why}' + (f' (after {attempts} attempts)' if block.retry else '')
                break
            bindings = added
    return results


def failures(results):
    return [r['error'] for r in results if r['status'] == 'failed']


def organization(guide):
    return 'org_guide_' + hashlib.sha256(guide.encode()).hexdigest()[:10]


def destination(guide):
    """The webhook destination of a page's Organization, which its blocks read as $QUIVR_DESTINATION."""
    return organization(guide).replace('_', '-') + '-receiver'


def destinations(stack, root=ROOT):
    """Harness webhook destinations: one per page with runnable blocks, which delivers nowhere."""
    try:
        runnable = pages(root)
    except GuideError:
        return {}
    secret = 'whsec_' + base64.b64encode(b'local-test-signing-secret-guides').decode()
    return {destination(guide): dict(organization=organization(guide), url='http://127.0.0.1:9/' + destination(guide),
                                     secret=secret) for guide in runnable}


def keys(stack, root=ROOT):
    """Harness keys: one per page with runnable blocks, bound to that page's own Organization."""
    granted = {}
    try:
        runnable = pages(root)
    except GuideError:
        return granted  # the runnable_guides step reports it; the stack itself must still start
    for guide in runnable:
        token = stack.state.setdefault('guide_key ' + guide, secrets.token_hex(24))
        granted[token] = dict(organization=organization(guide), actions=ACTIONS, corpora=['*'])
    stack.save()
    return granted


def verify(stack, root=ROOT):
    """The make verify step: replay every runnable page against the stack, each in its own Organization."""
    url, results = f"http://127.0.0.1:{stack.state['api_port']}", []
    for guide, found in pages(root).items():
        results += replay(found, {**environment(), 'QUIVR_API_URL': url, 'QUIVR_API_KEY': stack.state['guide_key ' + guide],
                                  'QUIVR_DESTINATION': destination(guide)})
    (stack.directory / 'guides.json').write_text(json.dumps(results, indent=2))
    if not results:
        raise AssertionError(f'no runnable block found in the pages of {INVENTORY}')
    if failures(results):
        raise AssertionError('runnable guide blocks failed:\n' + '\n'.join(failures(results)))
    return results


def environment():
    """The harness environment a block may rely on, never the secrets of the process that replays it."""
    return {name: os.environ[name] for name in ENVIRONMENT if name in os.environ}


def _tail(text, limit=300):
    text = ' '.join(text.split())
    return json.dumps(text if len(text) <= limit else '…' + text[-limit:], ensure_ascii=False)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('guides', nargs='*', help='pages to replay (default: every page with runnable blocks)')
    parser.add_argument('--list', action='store_true', help='list the runnable blocks instead of replaying them')
    parser.add_argument('--root', default=str(ROOT), help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    try:
        found = pages(args.root)
    except GuideError as error:
        print(error, file=sys.stderr)
        return 2
    unknown = [g for g in args.guides if g not in found]
    if unknown:
        print(f'no runnable block in: {", ".join(unknown)}', file=sys.stderr)
        return 2
    selected = [bs for g, bs in found.items() if not args.guides or g in args.guides]
    if args.list:
        for block in (b for bs in selected for b in bs):
            print(block.where() + (' (retry)' if block.retry else ''))
        return 0
    if not os.environ.get('QUIVR_API_URL') or not os.environ.get('QUIVR_API_KEY'):
        print('set QUIVR_API_URL and QUIVR_API_KEY to the API address and a key of the stack to replay against '
              '(`eval "$(make env)"` for the make dev stack)', file=sys.stderr)
        return 2
    stack = {name: os.environ[name] for name in STACK if os.environ.get(name)}
    results = [r for bs in selected for r in replay(bs, {**environment(), **stack})]
    for line in failures(results):
        print(line, file=sys.stderr)
    print(f'{sum(r["status"] == "passed" for r in results)} runnable blocks passed, {len(failures(results))} failed')
    return 1 if failures(results) else 0


if __name__ == '__main__':
    sys.exit(main())
