"""Connector plugin of the local verification harness.

`make verify` pins the Go SDK sample connector (sdks/go/examples/static-source,
kind ``static``) in the config's ``plugins`` list next to the other pins, and
runs it as its own process built with ``go build``. `make dev` does not pin it:
its kind is a test source. The acceptance phases in
tests/acceptance/collector_plugin_test.go talk to the public API only:

1. a ``static`` instance collects, its Records are searchable, and a second
   run resumes from the checkpoint (the source-read counter proves it);
2. with the plugin stopped, a new instance reports ``plugin_unavailable`` and
   collects nothing;
3. once the plugin is back, that instance collects everything.

Afterwards the process logs are scanned for the sample's test token.

First-party Go plugins (``FIRST_PARTY``) are different: every stack
(`make dev`, `make verify`, the browser demo) pins them by default, because
their kinds are real sources, and because the engine segments, embeds and
ranks nothing itself: core.ingest is its ingestion plugin (THE-777) and
core.retrieve its retrieval plugin (THE-779). Each row names a module under plugins/<id>,
built with ``go build`` into the stack directory whenever the stack starts
(the Go build cache makes it quick) and run on its own allocated port.
``QUIVR_<ID>=off make dev`` (the id in upper case, ``-`` as ``_``, for example
``QUIVR_RSS=off``) leaves one unpinned; verification and the demo always pin
them. The Railway image pins the same plugins (deploy/railway/core-entrypoint.py
``CONNECTORS``).
"""
import plugin_environment
import json, os, pathlib, signal, subprocess, time, urllib.request

import ports
from prepare_tokenizer import prepare as prepare_tokenizer

ROOT = pathlib.Path(__file__).resolve().parents[1]
SAMPLE = ROOT / 'sdks' / 'go' / 'examples' / 'static-source'
MANIFEST = SAMPLE / 'quivr-plugin.yaml'
# The sample's test token (tests/acceptance/collector_plugin_test.go): never a real secret.
TEST_TOKEN = b'fixture-test-secret-collector-plugin'
GO = os.environ.get('GO', 'go')


# One row per first-party Go connector plugin: ``id`` is its directory under
# plugins/, ``configuration(stack)`` the pin configuration of a local stack.
FIRST_PARTY = [
    # RSS and Atom feeds; the tests' fake feeds are on loopback.
    {'id': 'rss', 'configuration': lambda stack: {'allow_private_addresses': True}},
    # X lists; every stack points it at the local fake X API (tests/fakes/cmd/x), never at X.
    # allow_short_recheck (loopback endpoints only) lets tests recheck deletions every second.
    {'id': 'x-list', 'configuration': lambda stack: {'api_endpoint': f"http://127.0.0.1:{stack.state['fake_x_port']}",
                                                      'allow_short_recheck': True}},
    # Microsoft 365 mail, pinned to the local fake Graph (tests/fakes/cmd/graph), never to Microsoft.
    {'id': 'm365-mail', 'configuration': lambda stack: {'login_endpoint': f"http://127.0.0.1:{stack.state['graph_port']}",
                                                         'graph_endpoint': f"http://127.0.0.1:{stack.state['graph_port']}/v1.0"}},
    # Token windows and E5 embeddings through the stack's TEI and the pinned tokenizer. The api and
    # the worker refuse to start without an ingestion plugin; scripts/ingestion_plugin.py swaps it.
    {'id': 'core-ingest', 'configuration': lambda stack: {'tei_url': stack.state['tei_url'], 'tokenizer': prepare_tokenizer()}},
    # Today's search, answered from the candidates the engine serves. The api refuses to start without
    # a retrieval plugin; scripts/retrieval_plugin.py swaps it for the Go SDK sample.
    {'id': 'core-retrieve', 'configuration': lambda stack: {}},
]

# The first-party plugins without which the api and worker refuse to start: the engine segments,
# embeds and ranks nothing itself. Measurement stacks (make measure, make eval) pin only these.
CORE = ['core-ingest', 'core-retrieve']


def variable(row):
    """The environment variable that turns one first-party plugin on or off."""
    return 'QUIVR_' + row['id'].upper().replace('-', '_')


def from_environment():
    """The ids of the first-party plugins `make dev` pins: each QUIVR_<ID>, default on."""
    enabled = []
    for row in FIRST_PARTY:
        value = (os.environ.get(variable(row)) or 'on').lower()
        if value not in ('on', 'off'):
            raise ValueError(f'{variable(row)} is on or off, not {value!r}')
        if value == 'on':
            enabled.append(row['id'])
    return enabled


def select_first_party(stack, ids):
    stack.state['first_party'] = list(ids)
    stack.save()


def first_party(stack):
    """The rows this stack pins; every row when the stack never selected them."""
    ids = stack.state.get('first_party', [row['id'] for row in FIRST_PARTY])
    return [row for row in FIRST_PARTY if row['id'] in ids]


def first_party_port(stack, row):
    key = f"{row['id']}_plugin_port"
    if key not in stack.state:
        stack.state[key] = ports.allocate()
        stack.save()
    return stack.state[key]


def first_party_manifest(stack, row):
    return stack.source / 'plugins' / row['id'] / 'quivr-plugin.yaml'


def first_party_pins(stack):
    """The `plugins` entries of QUIVR_CONFIG for the first-party Go plugins."""
    return [{'manifest': str(first_party_manifest(stack, row)), 'endpoint': f"http://127.0.0.1:{first_party_port(stack, row)}",
             'configuration': row['configuration'](stack)} for row in first_party(stack)]


def describe(stack):
    """One line for `make dev`: which first-party connector plugins are pinned and how to change it."""
    names = ', '.join(row['id'] for row in first_party(stack)) or 'none'
    switches = ', '.join(f'{variable(row)}=on|off' for row in FIRST_PARTY)
    return f'Connector plugins: {names}' + (f' ({switches})' if switches else '')


def healthy_port(port):
    if port is None:
        return False
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/v0/health", timeout=1) as r:
            return r.status == 200
    except OSError:
        return False


def start_first_party(stack, only=None):
    """Build each pinned first-party plugin (or only the named ones), run it and wait for its health."""
    stop_first_party(stack, only)
    for row in first_party(stack):
        if only is not None and row['id'] not in only:
            continue
        directory, binary = stack.source / 'plugins' / row['id'], stack.directory / f"quivr-{row['id']}"
        log = stack.directory / f"{row['id']}-plugin.log"
        subprocess.run([GO, 'build', '-o', str(binary), '.'], cwd=directory, check=True)
        port = first_party_port(stack, row)
        env = {**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(first_party_manifest(stack, row))}
        with log.open('a') as out:
            p = subprocess.Popen([str(binary)], cwd=directory, env=env, stdout=out, stderr=out, start_new_session=True)
        stack.state[f"{row['id']}_plugin_pid"] = p.pid
        stack.save()
        deadline = time.monotonic() + 30
        while not healthy_port(port):
            if p.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError(f"{row['id']} connector plugin not healthy; inspect {log}")
            time.sleep(.05)


def stop_first_party(stack, only=None):
    for row in FIRST_PARTY:
        if only is not None and row['id'] not in only:
            continue
        pid = stack.state.pop(f"{row['id']}_plugin_pid", None)
        stack.save()
        if pid is None:
            continue
        try:
            os.killpg(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            continue
        deadline = time.monotonic() + 10
        while healthy_port(stack.state.get(f"{row['id']}_plugin_port")) and time.monotonic() < deadline:
            time.sleep(.05)


def select(stack, enabled):
    stack.state['connector_plugin'] = bool(enabled)
    stack.save()


def pins(stack):
    """The `plugins` entry of QUIVR_CONFIG, or none outside verification."""
    if not stack.state.get('connector_plugin'):
        return []
    stack.state.setdefault('connector_plugin_port', ports.allocate())
    stack.save()
    return [{'manifest': str(MANIFEST), 'endpoint': f"http://127.0.0.1:{stack.state['connector_plugin_port']}"}]


def binary(stack):
    return stack.directory / 'static-source'


def healthy(stack):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{stack.state['connector_plugin_port']}/v0/health", timeout=1) as r:
            return r.status == 200
    except (OSError, KeyError):
        return False


def start(stack):
    """Build the sample once per stack, run it and wait for its health."""
    stop(stack)
    if not binary(stack).exists():
        subprocess.run([GO, 'build', '-o', str(binary(stack)), './examples/static-source'], cwd=ROOT / 'sdks' / 'go', check=True)
    env = {**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(stack.state['connector_plugin_port']), 'QUIVR_PLUGIN_MANIFEST': str(MANIFEST)}
    with (stack.directory / 'connector-plugin.log').open('a') as log:
        p = subprocess.Popen([str(binary(stack))], cwd=SAMPLE, env=env, stdout=log, stderr=log, start_new_session=True)
    stack.state['connector_plugin_pid'] = p.pid
    stack.save()
    deadline = time.monotonic() + 30
    while not healthy(stack):
        if p.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('connector plugin not healthy; inspect ' + str(stack.directory / 'connector-plugin.log'))
        time.sleep(.05)


def stop(stack):
    pid = stack.state.pop('connector_plugin_pid', None)
    stack.save()
    if pid is None:
        return
    try:
        os.killpg(pid, signal.SIGTERM)
    except (ProcessLookupError, PermissionError):
        return
    deadline = time.monotonic() + 10
    while healthy(stack) and time.monotonic() < deadline:
        time.sleep(.05)


def verify(stack):
    """Collect and resume, then an outage and its recovery; times the phases."""
    started = time.monotonic()
    start(stack)
    stack.tests('^TestCollectorPluginCollectsAndResumes$')
    stop(stack)
    stack.tests('^TestCollectorPluginOutage$')
    start(stack)
    stack.tests('^TestCollectorPluginRecovers$')
    leaked = [log.name for log in [*stack.directory.glob('api*.log*'), *stack.directory.glob('worker*.log*'), stack.directory / 'connector-plugin.log']
              if log.exists() and TEST_TOKEN in log.read_bytes()]
    assert not leaked, f'the test token appears in {leaked}'
    (stack.directory / 'collector-plugin.json').write_text(json.dumps({'status': 'passed', 'seconds': round(time.monotonic() - started, 1)}))
