"""Ingestion plugin step of the local verification harness (THE-776).

TestIngestionPluginBefore first ingests a Corpus through the stack's
core.ingest. The step then pins the Go SDK sample ingestion plugin
(sdks/go/examples/hash-embedder: paragraph segments embedded by hashing their
words in two vector spaces) in place of core.ingest, a deployment pins one
ingestion plugin, with its small space served and its large space for
evaluation, restarts the API and the worker on that pin, and runs
TestIngestionPlugin through the public API: the Corpus keeps core.ingest's
space until it is rebuilt, a Corpus created after the swap starts on the
plugin's spaces, the rebuild moves the first one to the plugin's named spaces,
and search then encodes queries with the plugin. The stack's configuration
and processes are restored afterwards, even on failure.
"""
import json, os, pathlib, signal, subprocess, time, urllib.error, urllib.request, uuid

import ports

ROOT = pathlib.Path(__file__).resolve().parents[1]
SAMPLE = ROOT / 'sdks' / 'go' / 'examples' / 'hash-embedder'
GO = os.environ.get('GO', 'go')
SPACES = {'example.hash_embedder.small': 'served', 'example.hash_embedder.large': 'evaluation'}


def healthy(port):
    try:
        with urllib.request.urlopen(f'http://127.0.0.1:{port}/v0/health', timeout=2) as r:
            return r.status == 200
    except (urllib.error.URLError, OSError):
        return False


def verify(stack):
    """Pin the sample ingestion plugin, run its acceptance test, then restore the stack's pins."""
    directory = stack.directory / 'ingestion-plugin'
    run = {'QUIVR_TEST_INGESTION_PLUGIN': '1', 'QUIVR_TEST_INGESTION_RUN': uuid.uuid4().hex}
    stack.tests('^TestIngestionPluginBefore$', run)
    directory.mkdir(exist_ok=True)
    binary = directory / 'hash-embedder'
    subprocess.run([GO, 'build', '-o', str(binary), './examples/hash-embedder'], cwd=ROOT / 'sdks' / 'go', check=True)
    port = ports.allocate()
    env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(SAMPLE / 'quivr-plugin.yaml')}
    with (directory / 'plugin.log').open('a') as log:
        plugin = subprocess.Popen([str(binary)], cwd=SAMPLE, env=env, stdout=log, stderr=log, start_new_session=True)
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    try:
        deadline = time.monotonic() + 30
        while not healthy(port):
            if plugin.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError('ingestion plugin not healthy; inspect ' + str(directory / 'plugin.log'))
            time.sleep(.1)
        for name, text in configs.items():
            cfg = json.loads(text)
            others = [p for p in cfg.get('plugins', []) if not p['manifest'].endswith('/core-ingest/quivr-plugin.yaml')]
            cfg['plugins'] = others + [{'manifest': str(SAMPLE / 'quivr-plugin.yaml'), 'endpoint': f'http://127.0.0.1:{port}', 'spaces': SPACES}]
            path = stack.directory / name
            path.write_text(json.dumps(cfg))
            path.chmod(0o600)
        # The short-retention API keeps the stack's own pins: it stays stopped
        # while the plugin's spaces are registered.
        stack.stop_processes()
        stack.start_processes()
        stack.tests('^TestIngestionPlugin$', run)
    finally:
        for name, text in configs.items():
            (stack.directory / name).write_text(text)
        try:
            os.killpg(plugin.pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
        stack.stop_processes()
        stack.start_processes()
        stack.start_short_retention_api()
