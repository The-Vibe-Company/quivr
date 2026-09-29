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
"""
import json, os, pathlib, signal, subprocess, time, urllib.request

import ports

ROOT = pathlib.Path(__file__).resolve().parents[1]
SAMPLE = ROOT / 'sdks' / 'go' / 'examples' / 'static-source'
MANIFEST = SAMPLE / 'quivr-plugin.yaml'
# The sample's test token (tests/acceptance/collector_plugin_test.go): never a real secret.
TEST_TOKEN = b'fixture-test-secret-collector-plugin'
GO = os.environ.get('GO', 'go')


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
    env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(stack.state['connector_plugin_port']), 'QUIVR_PLUGIN_MANIFEST': str(MANIFEST)}
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
