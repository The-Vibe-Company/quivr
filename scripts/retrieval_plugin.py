"""Retrieval plugin step of the local verification harness (THE-778).

Pins the Go SDK sample retrieval plugin (sdks/go/examples/fusion-retriever:
keyword and vector candidates fused by reciprocal rank) beside the stack's
other pins, restarts the API and the worker on that pin, and runs
TestRetrievalPlugin through the public API. The stack's configuration and
processes are restored afterwards, even on failure.
"""
import json, os, pathlib, signal, subprocess, time

import ports
from ingestion_plugin import healthy

ROOT = pathlib.Path(__file__).resolve().parents[1]
SAMPLE = ROOT / 'sdks' / 'go' / 'examples' / 'fusion-retriever'
GO = os.environ.get('GO', 'go')


def verify(stack):
    """Pin the sample retrieval plugin, run its acceptance test, then restore the stack's pins."""
    directory = stack.directory / 'retrieval-plugin'
    directory.mkdir(exist_ok=True)
    binary = directory / 'fusion-retriever'
    subprocess.run([GO, 'build', '-o', str(binary), './examples/fusion-retriever'], cwd=ROOT / 'sdks' / 'go', check=True)
    port = ports.allocate()
    env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(SAMPLE / 'quivr-plugin.yaml')}
    with (directory / 'plugin.log').open('a') as log:
        plugin = subprocess.Popen([str(binary)], cwd=SAMPLE, env=env, stdout=log, stderr=log, start_new_session=True)
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    try:
        deadline = time.monotonic() + 30
        while not healthy(port):
            if plugin.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError('retrieval plugin not healthy; inspect ' + str(directory / 'plugin.log'))
            time.sleep(.1)
        for name, text in configs.items():
            cfg = json.loads(text)
            cfg['plugins'] = cfg.get('plugins', []) + [{'manifest': str(SAMPLE / 'quivr-plugin.yaml'), 'endpoint': f'http://127.0.0.1:{port}'}]
            path = stack.directory / name
            path.write_text(json.dumps(cfg))
            path.chmod(0o600)
        stack.stop_processes()
        stack.start_processes()
        stack.tests('^TestRetrievalPlugin$', {'QUIVR_TEST_RETRIEVAL_PLUGIN': '1'})
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
