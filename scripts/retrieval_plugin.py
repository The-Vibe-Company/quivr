"""Pin the SDK sample alongside two identities served by the fake plugin.

TestRetrievalPlugin exercises deployment aliases and full profile names through
the public API, retaining the sample's keyword/vector ranking proof. The stack's
configuration and processes are restored afterwards, even on failure.
"""
import json, os, pathlib, subprocess, time

import plugin_environment
import ports
from ingestion_plugin import healthy, stop_plugin

ROOT = pathlib.Path(__file__).resolve().parents[1]
SAMPLE = ROOT / 'sdks' / 'go' / 'examples' / 'fusion-retriever'
GO = os.environ.get('GO', 'go')
FAKE_MANIFEST = '''id: {plugin_id}
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.7.0 <0.8.0"
contributions:
  retrieval:
    profiles:
      default:
        max_latency_ms: 5000
        max_cost_cents: 0
    limits:
      max_rounds: 2
      max_requests: 2
      max_candidates: 30
'''


def verify(stack):
    """Pin three retrieval providers, exercise routing, then restore the stack's pins."""
    directory = stack.directory / 'retrieval-plugin'
    directory.mkdir(exist_ok=True)
    binary = directory / 'fusion-retriever'
    subprocess.run([GO, 'build', '-o', str(binary), './examples/fusion-retriever'], cwd=ROOT / 'sdks' / 'go', check=True)
    fake_binary = directory / 'quivr-fake-plugin'
    subprocess.run([GO, 'test', '-c', '-o', str(fake_binary), './tests/plugin-contract'], cwd=ROOT, check=True)
    port = ports.allocate()
    env = {**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(SAMPLE / 'quivr-plugin.yaml')}
    with (directory / 'plugin.log').open('a') as log:
        plugin = subprocess.Popen([str(binary)], cwd=SAMPLE, env=env, stdout=log, stderr=log, start_new_session=True)
    running = [(plugin, port, directory / 'plugin.log')]
    pins = [{'manifest': str(SAMPLE / 'quivr-plugin.yaml'), 'endpoint': f'http://127.0.0.1:{port}'}]
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    try:
        for name in ['first', 'second']:
            manifest = directory / f'{name}-quivr-plugin.yaml'
            manifest.write_text(FAKE_MANIFEST.format(plugin_id=f'quivr-test.{name}_retriever'))
            fake_port = ports.allocate()
            log_path = directory / f'{name}-plugin.log'
            with log_path.open('a') as log:
                fake = subprocess.Popen([str(fake_binary), '-test.run=^$'], cwd=directory, stdout=log, stderr=log,
                                        start_new_session=True, env={**env, 'QUIVR_FAKE_PLUGIN': '1',
                                            'QUIVR_PLUGIN_PORT': str(fake_port), 'QUIVR_PLUGIN_MANIFEST': str(manifest)})
            running.append((fake, fake_port, log_path))
            pins.append({'manifest': str(manifest), 'endpoint': f'http://127.0.0.1:{fake_port}'})
        deadline = time.monotonic() + 30
        for process, where, log in running:
            while not healthy(where):
                if process.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError('retrieval plugin not healthy; inspect ' + str(log))
                time.sleep(.1)
        for name, text in configs.items():
            cfg = json.loads(text)
            others = [p for p in cfg.get('plugins', []) if not p['manifest'].endswith('/core-retrieve/quivr-plugin.yaml')]
            cfg['plugins'] = others + pins
            cfg['retrieval'] = {'profiles': {'default': 'example.fusion_retriever/default', 'deep': 'example.fusion_retriever/deep',
                                            'first': 'quivr-test.first_retriever/default', 'second': 'quivr-test.second_retriever/default'}}
            path = stack.directory / name
            path.write_text(json.dumps(cfg))
            path.chmod(0o600)
        stack.stop_processes()
        stack.start_processes()
        stack.tests('^TestRetrievalPlugin$', {'QUIVR_TEST_RETRIEVAL_PLUGIN': '1'})
    finally:
        for name, text in configs.items():
            (stack.directory / name).write_text(text)
        for process, _, _ in running:
            stop_plugin(process)
        stack.stop_processes()
        stack.start_processes()
        stack.start_short_retention_api()
