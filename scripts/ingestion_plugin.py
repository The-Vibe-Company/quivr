"""Ingestion plugin step of the local verification harness (THE-776).

TestIngestionPluginBefore first ingests a Corpus through the stack's
core.ingest. The step then pins the Go SDK sample ingestion plugin
(sdks/go/examples/hash-embedder: paragraph segments embedded by hashing their
words in two vector spaces) beside core.ingest, selected as the default
ingestion plugin, with its small space served and its large space for
evaluation, restarts the API and the worker on that pin, and runs
TestIngestionPlugin through the public API: the Corpus keeps core.ingest's
space until it is rebuilt, a Corpus created after the swap starts on the
plugin's spaces, the rebuild moves the first one to the plugin's named spaces,
and search then encodes queries with the plugin.

Then, with no restart (THE-781), the same plugin built as 0.2.0 runs at a
second address: TestPluginActivation registers, checks and activates it
through the operator API while 0.1.0, frozen by the step, holds a Record in
flight; the step lets 0.1.0 run again, TestPluginActivationDrains sees that
Record finish on 0.1.0 and 0.1.0 drain (THE-786); the step stops 0.1.0, and
TestPluginActivationIngests ingests and searches through 0.2.0 alone. Finally (THE-782) 0.1.0 runs again
and 0.2.0 stops: TestPinnedWorkStarts pins a Record to 0.2.0's plan and
activates 0.1.0, the step restarts the worker, and TestPinnedWorkRetries checks
that the Record keeps retrying on 0.2.0. The step restores 0.2.0 and
TestPinnedWorkDrains sees the original import finish and release its pin.
Last (THE-783), 0.2.0 runs again as a bad release: TestRollbackStarts
activates it and ingests through it, the step stops it, and TestRollback rolls
back to 0.1.0 in one call, stopping the Record pinned to 0.2.0 and ingesting
the next one through 0.1.0 alone. Then (THE-784) TestBackfillFillsWindowAndPromotesSpaces builds a
Corpus that predates the large space and starts a backfill of a window into
it, pauses and resumes it, checks what it filled and promotes the large space
and back. Workflow interruption and retry are owned by Temporal workflow tests.
Finally (THE-785) TestQuarantineReprocessIngestion reprocesses the Record
TestRollback stopped. The stack's configuration and processes are restored
afterwards, even on failure.
"""
import plugin_environment
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


def start_plugin(directory, binary, port, manifest, log_name):
    """Run the sample plugin binary at a port, serving the given manifest's digest in discovery."""
    env = {**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(manifest)}
    with (directory / log_name).open('a') as log:
        return subprocess.Popen([str(binary)], cwd=SAMPLE, env=env, stdout=log, stderr=log, start_new_session=True)


def await_healthy(plugin, port, log):
    deadline = time.monotonic() + 30
    while not healthy(port):
        if plugin.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('ingestion plugin not healthy; inspect ' + str(log))
        time.sleep(.1)


def stop_plugin(plugin):
    try:
        os.killpg(plugin.pid, signal.SIGTERM)
    except (ProcessLookupError, PermissionError):
        pass
    try:
        plugin.wait(timeout=10)
    except subprocess.TimeoutExpired:
        pass


def verify(stack):
    """Pin the sample ingestion plugin, run its acceptance test, then restore the stack's pins."""
    directory = stack.directory / 'ingestion-plugin'
    run = {'QUIVR_TEST_INGESTION_PLUGIN': '1', 'QUIVR_TEST_INGESTION_RUN': uuid.uuid4().hex}
    stack.tests('^TestIngestionPluginBefore$', run)
    directory.mkdir(exist_ok=True)
    binary = directory / 'hash-embedder'
    subprocess.run([GO, 'build', '-o', str(binary), './examples/hash-embedder'], cwd=ROOT / 'sdks' / 'go', check=True)
    port = ports.allocate()
    plugin = start_plugin(directory, binary, port, SAMPLE / 'quivr-plugin.yaml', 'plugin.log')
    # The next build of the same plugin: its manifest at version 0.2.0, same spaces.
    next_manifest = directory / 'quivr-plugin-0.2.0.yaml'
    next_manifest.write_text((SAMPLE / 'quivr-plugin.yaml').read_text().replace('version: 0.1.0', 'version: 0.2.0', 1))
    next_port = ports.allocate()
    next_plugin = None
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    try:
        await_healthy(plugin, port, directory / 'plugin.log')
        for name, text in configs.items():
            cfg = json.loads(text)
            others = cfg.get('plugins', [])
            cfg['ingestion'] = {'default': 'example.hash_embedder'}
            cfg['plugins'] = others + [{'manifest': str(SAMPLE / 'quivr-plugin.yaml'), 'endpoint': f'http://127.0.0.1:{port}', 'spaces': SPACES}]
            path = stack.directory / name
            path.write_text(json.dumps(cfg))
            path.chmod(0o600)
        # The short-retention API keeps the stack's own pins: it stays stopped
        # while the plugin's spaces are registered.
        stack.stop_processes()
        stack.start_processes()
        stack.tests('^TestIngestionPlugin$', run)
        next_plugin = start_plugin(directory, binary, next_port, next_manifest, 'plugin-0.2.0.log')
        await_healthy(next_plugin, next_port, directory / 'plugin-0.2.0.log')
        activation = {**run, 'QUIVR_TEST_ACTIVATION_ENDPOINT': f'http://127.0.0.1:{next_port}', 'QUIVR_TEST_ROLLBACK_ENDPOINT': f'http://127.0.0.1:{port}',
                      'QUIVR_TEST_ACTIVATION_PINNED_MANIFEST': str(SAMPLE / 'quivr-plugin.yaml'), 'QUIVR_TEST_ACTIVATION_MANIFEST': str(next_manifest)}
        # Work in flight finishes on the version it started on (THE-786):
        # 0.1.0 is frozen, so its calls hang instead of failing, while
        # TestPluginActivation starts a Record on it and activates 0.2.0;
        # running again, it finishes that Record and drains.
        os.killpg(plugin.pid, signal.SIGSTOP)
        try:
            stack.tests('^TestPluginActivation$', activation)
        finally:
            os.killpg(plugin.pid, signal.SIGCONT)
        stack.tests('^TestPluginActivationDrains$', activation)
        # Only 0.2.0 is left to segment, embed and encode queries.
        stop_plugin(plugin)
        stack.tests('^TestPluginActivationIngests$', activation)
        # Work finishes on the plan it started on (THE-782): 0.1.0 runs again
        # at its address and 0.2.0 stops. TestPinnedWorkStarts pins a Record
        # to 0.2.0's plan and activates 0.1.0; the worker restarts while 0.2.0
        # drains. The import keeps retrying on its original pin until that
        # exact build returns; it never moves to 0.1.0.
        plugin = start_plugin(directory, binary, port, SAMPLE / 'quivr-plugin.yaml', 'plugin.log')
        await_healthy(plugin, port, directory / 'plugin.log')
        stop_plugin(next_plugin)
        stack.tests('^TestPinnedWorkStarts$', activation)
        stack.stop_worker()
        stack.start_worker()
        stack.tests('^TestPinnedWorkRetries$', activation)
        next_plugin = start_plugin(directory, binary, next_port, next_manifest, 'plugin-0.2.0.log')
        await_healthy(next_plugin, next_port, directory / 'plugin-0.2.0.log')
        stack.tests('^TestPinnedWorkDrains$', activation)
        # One-call rollback (THE-783): 0.2.0 is activated as a bad release,
        # then stops; TestRollback rolls back to 0.1.0.
        stack.tests('^TestRollbackStarts$', activation)
        stop_plugin(next_plugin)
        stack.tests('^TestRollback$', activation)
        # Backfill (THE-784): a Corpus built while 0.1.0 enabled its small
        # space alone gets the large one from a paced backfill, which is
        # paused, resumes and is promoted. Workflow tests own interruption/replay.
        stack.tests('^TestBackfillFillsWindowAndPromotesSpaces$', activation)
        # Quarantine reprocess (THE-785): the Record TestRollback stopped is
        # reprocessed through the plan now active and becomes searchable.
        stack.tests('^TestQuarantineReprocessIngestion$', activation)
    finally:
        for name, text in configs.items():
            (stack.directory / name).write_text(text)
        for p in [plugin, next_plugin]:
            if p is not None:
                stop_plugin(p)
        stack.stop_processes()
        stack.start_processes()
        stack.start_short_retention_api()


def verify_routes(stack):
    """Use the manifest-driven fakeplugin beside core.ingest and pdf-text."""
    directory = stack.directory / 'ingestion-routes'
    directory.mkdir(exist_ok=True)
    binary = directory / 'fixture-ingestion'
    subprocess.run([GO, 'test', '-c', '-o', str(binary), './tests/plugin-contract'], cwd=ROOT, check=True)
    manifest = directory / 'quivr-plugin.yaml'
    manifest.write_text((SAMPLE / 'quivr-plugin.yaml').read_text().replace('example.hash_embedder', 'example.fixture_ingest'))
    port = ports.allocate()
    env = {**plugin_environment.inherited(), 'QUIVR_FAKE_PLUGIN': '1', 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(manifest)}
    with (directory / 'plugin.log').open('a') as log:
        plugin = subprocess.Popen([str(binary)], cwd=directory, env=env, stdout=log, stderr=log, start_new_session=True)
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    try:
        await_healthy(plugin, port, directory / 'plugin.log')
        for name, text in configs.items():
            cfg = json.loads(text)
            cfg['plugins'].append({'manifest': str(manifest), 'endpoint': f'http://127.0.0.1:{port}', 'spaces': {k.replace('example.hash_embedder', 'example.fixture_ingest'): v for k, v in SPACES.items()}})
            cfg['ingestion'] = {'default': 'core.ingest', 'routes': {'text/plain': 'example.fixture_ingest', 'application/pdf': 'core.ingest'}}
            (stack.directory / name).write_text(json.dumps(cfg))
        stack.stop_processes()
        stack.start_processes()
        stack.tests('^TestIngestionSourceRoutes$', {'QUIVR_TEST_INGESTION_ROUTES': uuid.uuid4().hex})
    finally:
        stack.stop_processes()
        for name, text in configs.items():
            (stack.directory / name).write_text(text)
        stop_plugin(plugin)
        stack.start_processes()
        stack.start_short_retention_api()
