"""Public isolation proof with one live and one bulk worker.

A corpus seeded before a new ingestion plugin is pinned keeps its original
serving generation. A rebuild adds the new plugin's spaces. Holding just that
plugin keeps real bulk document work active while the live corpus can ingest
through its original plugin. Tests observe only public API and probe metrics.
"""
import json
import os
import signal
import subprocess
import uuid

import ingestion_plugin
import ports


def verify(stack):
    run = {'QUIVR_TEST_QUEUES': '1', 'QUIVR_TEST_QUEUE_RUN': uuid.uuid4().hex}
    stack.tests('^TestQueueIsolationSeed$', run)
    directory = stack.directory / 'queue-workers'
    directory.mkdir(exist_ok=True)
    binary = directory / 'hash-embedder'
    subprocess.run([ingestion_plugin.GO, 'build', '-o', str(binary), './examples/hash-embedder'],
                   cwd=ingestion_plugin.ROOT / 'sdks' / 'go', check=True)
    port = ports.allocate()
    plugin = ingestion_plugin.start_plugin(directory, binary, port,
                                          ingestion_plugin.SAMPLE / 'quivr-plugin.yaml', 'plugin.log')
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    bulk_pid = None
    try:
        ingestion_plugin.await_healthy(plugin, port, directory / 'plugin.log')
        for name, text in configs.items():
            cfg = json.loads(text)
            cfg['plugins'] += [{'manifest': str(ingestion_plugin.SAMPLE / 'quivr-plugin.yaml'),
                                'endpoint': f'http://127.0.0.1:{port}', 'spaces': ingestion_plugin.SPACES}]
            cfg['ingestion'] = {'default': 'core.ingest', 'routes': {'application/pdf': 'example.hash_embedder'}}
            if name == 'worker.json':
                cfg['worker'] = {'queues': ['live'], 'slots': {'live': 1, 'bulk': 1}}
                bulk = {**cfg, 'worker': {'queues': ['bulk'], 'slots': {'live': 1, 'bulk': 1}},
                        'probe_listen': f'127.0.0.1:{ports.allocate()}'}
                bulk_port = int(bulk['probe_listen'].rsplit(':', 1)[1])
                path = stack.directory / 'queue-bulk.json'
                path.write_text(json.dumps(bulk)); path.chmod(0o600)
            path = stack.directory / name
            path.write_text(json.dumps(cfg)); path.chmod(0o600)
        stack.stop_processes()
        stack.start_processes()
        live_pid = stack.state['worker_pid']
        stack.spawn('worker', 'queue-bulk.json')
        bulk_pid = stack.state['worker_pid']
        stack.state['worker_pid'] = live_pid
        stack.state['queue_bulk_probe_port'] = bulk_port
        stack.save()
        stack.await_ready('queue_bulk_probe_port')
        run.update(QUIVR_TEST_QUEUE_LIVE_METRICS=f"http://127.0.0.1:{stack.state['worker_probe_port']}/metrics",
                   QUIVR_TEST_QUEUE_BULK_METRICS=f'http://127.0.0.1:{bulk_port}/metrics')
        os.killpg(plugin.pid, signal.SIGSTOP)
        try:
            stack.tests('^TestQueueIsolationDuring$', run)
        finally:
            os.killpg(plugin.pid, signal.SIGCONT)
        stack.tests('^TestQueueIsolationDrains$', run)
    finally:
        stack.stop_processes()
        ingestion_plugin.stop_plugin(plugin)
        for name, text in configs.items():
            (stack.directory / name).write_text(text)
        stack.state.pop('queue_bulk_probe_port', None)
        stack.save()
        stack.start_processes()
