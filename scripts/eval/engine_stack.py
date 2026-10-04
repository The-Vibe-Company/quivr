#!/usr/bin/env python3
"""Trusted full-stack smoke and Sandbox entrypoint. No held-out data or providers."""
import hashlib
import json
import argparse
import os
import pathlib
import selectors
import select
import signal
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[2]
DOCUMENTS = {
    'orchard': {'title': 'Apple harvest', 'text': 'Ripe apples are harvested from trees in the orchard.'},
    'stars': {'title': 'Astronomy', 'text': 'A telescope observes distant stars and planets in the night sky.'},
    'bicycle': {'title': 'Bicycle repair', 'text': 'Replace the bicycle chain and adjust the brakes before riding.'}}
QUERY = 'apple harvest orchard trees'
MODES = ['lexical', 'semantic', 'hybrid']


def fingerprint():
    return hashlib.sha256(json.dumps(DOCUMENTS, sort_keys=True).encode()).hexdigest()


def lineage():
    import yaml
    compose = yaml.safe_load((ROOT / 'deploy/compose/compose.yaml').read_text())
    model = json.loads((ROOT / 'third_party/e5/model-lock.json').read_text())
    return {'services': {name: value['image'] for name, value in compose['services'].items()},
            'model_revision': model['model_revision'],
            'tokenizer_sha256': model['files']['tokenizer.json'],
            'go_version': next(line.split()[1] for line in (ROOT / 'go.mod').read_text().splitlines()
                               if line.startswith('go ')), 'python_version': '3.12',
            'plugins': ['core.ingest', 'core.retrieve']}


def failure(error):
    # Dependency exceptions can reflect API bodies, document text or local keys.
    # Keep class information without exporting their arbitrary messages.
    classes = {'OSError', 'RuntimeError', 'TimeoutError', 'InterruptedError',
               'ValueError', 'CalledProcessError', 'LeaseLost', 'Unavailable'}
    kind = type(error).__name__
    return {'error_class': kind if kind in classes else 'Exception',
            'error_message': 'smoke dependency or lifecycle failed; inspect the private remote logs'}


def search_smoke(client, corpus, keys):
    import run
    for mode in MODES:
        ranked, _, error = run.search(client, corpus, QUERY, mode, 'default', keys, limit=10)
        if error or 'orchard' not in ranked:
            raise RuntimeError('smoke search did not return the expected record')
    return {'documents': len(DOCUMENTS), 'searches': len(MODES), 'modes': list(MODES)}


def progress(path, phase, count=0):
    if path:
        staged = pathlib.Path(str(path) + '.tmp')
        staged.write_text(json.dumps({'phase': phase, 'count': count}))
        staged.replace(path)


def smoke(cfg, progress_path=None):
    sys.path.insert(0, str(ROOT / 'scripts'))
    import run
    from local import alive
    stacks, phases = [], {}
    started, cleanup = time.monotonic(), False
    outcome = {'status': 'failed'}
    previous = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
    def cancel(*_):
        raise InterruptedError('smoke cancelled')
    for sig in previous:
        signal.signal(sig, cancel)
    try:
        progress(progress_path, 'starting')
        client = run.start_stack(phases, stacks)
        progress(progress_path, 'ingesting', len(DOCUMENTS))
        _, created = client.call('POST', '/v0/corpora',
            {'name': 'Engine smoke', 'idempotency_key': uuid.uuid4().hex}, expected=(201,), attempts=1)
        keys, _ = run.ingest(client, created['corpus_id'], 'engine-smoke', DOCUMENTS,
                             timeout=min(300, cfg['max_seconds']), stall=60)
        if len(keys) != len(DOCUMENTS):
            raise RuntimeError('smoke ingestion did not make every record searchable')
        progress(progress_path, 'searching', len(keys))
        outcome = {'status': 'complete', **search_smoke(client, created['corpus_id'], keys)}
    except Exception as error:
        outcome = {'status': 'failed', **failure(error)}
    finally:
        # A second cancel must not interrupt cleanup. The supervisor and local
        # dispatcher still enforce independent hard cleanup time bounds.
        for sig in previous:
            signal.signal(sig, signal.SIG_IGN)
        progress(progress_path, 'cleanup')
        try:
            for stack in reversed(stacks):
                pids = list(stack.state['pids']) + [value for key, value in stack.state.items()
                                                  if key.endswith('_plugin_pid')]
                stack.down(reset=True)
                if stack.compose('ps', '--all', '-q', capture_output=True, text=True).stdout.strip():
                    raise RuntimeError('smoke containers remain after cleanup')
                deadline = time.monotonic() + cfg['cleanup_seconds']
                while any(alive(pid) for pid in pids):
                    if time.monotonic() >= deadline:
                        raise TimeoutError('smoke processes remain after cleanup')
                    select.select([], [], [], .05)
            cleanup = True
        except Exception as error:
            outcome = {'status': 'failed', **failure(error)}
        for sig, handler in previous.items():
            signal.signal(sig, handler)
    return {**outcome, 'remote_cleanup_verified': cleanup,
            'duration_seconds': round(time.monotonic() - started, 3)}


def stop(process, seconds):
    """Stop an owned process group; never signal a caller's group."""
    if process is None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=seconds)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=5)


def supervise(cfg):
    """Sandbox PID 1 owns dockerd and the runner, including before create returns.

    Keepalives arrive on stdin, independent of smoke stdout/exec activity.
    Expiry exits this original entrypoint; Modal then finishes the Sandbox.
    """
    daemon, worker = None, None
    previous = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
    selector = selectors.DefaultSelector()
    selector.register(sys.stdin, selectors.EVENT_READ)
    heartbeat = time.monotonic()
    end = heartbeat + cfg['max_seconds']
    result = {'status': 'failed', **failure(TimeoutError()), 'remote_cleanup_verified': False}
    def cancel(*_):
        raise InterruptedError('supervisor cancelled')
    signal.signal(signal.SIGINT, cancel)
    signal.signal(signal.SIGTERM, cancel)
    with tempfile.TemporaryDirectory(prefix='quivr-engine-') as temp:
        directory = pathlib.Path(temp)
        result_path, event_path = directory / 'result.json', directory / 'progress.json'
        try:
            with (directory / 'docker.log').open('w') as log:
                daemon = subprocess.Popen(['dockerd'], stdout=log, stderr=log, start_new_session=True)
            phase, ready, last_event = 'docker', False, None
            while True:
                now = time.monotonic()
                if now - heartbeat >= cfg['reaper_seconds'] or now >= end:
                    raise TimeoutError('supervisor keepalive or execution deadline expired')
                for key, _ in selector.select(timeout=.25):
                    ping = os.read(key.fd, 4096)
                    if ping:
                        heartbeat = time.monotonic()
                    else:
                        selector.unregister(key.fileobj)
                        raise InterruptedError('dispatcher disconnected')
                if daemon.poll() is not None:
                    raise RuntimeError('Docker daemon exited')
                if not ready:
                    probe = subprocess.run(['docker', 'info'], stdout=subprocess.DEVNULL,
                                           stderr=subprocess.DEVNULL, timeout=5)
                    ready = probe.returncode == 0
                    if ready:
                        with (directory / 'runner.log').open('w') as log:
                            worker = subprocess.Popen([sys.executable, __file__, '--local', '--settings',
                                json.dumps(cfg), '--output', str(result_path), '--progress', str(event_path)],
                                stdout=log, stderr=log, start_new_session=True)
                if event_path.exists():
                    event = json.loads(event_path.read_text())
                    if event['phase'] in ('starting', 'ingesting', 'searching', 'cleanup') and type(event['count']) is int:
                        phase = event['phase']
                        if event != last_event:
                            print(json.dumps({'event': 'progress', **event}), flush=True)
                            last_event = event
                elif last_event is None:
                    print(json.dumps({'event': 'progress', 'phase': phase, 'count': 0}), flush=True)
                    last_event = {'phase': phase, 'count': 0}
                if worker is not None and worker.poll() is not None:
                    if worker.returncode != 0 or not result_path.exists():
                        raise RuntimeError('smoke runner exited without evidence')
                    result = json.loads(result_path.read_text())
                    break
        except Exception as error:
            result = {'status': 'failed', **failure(error), 'remote_cleanup_verified': False}
        finally:
            signal.signal(signal.SIGINT, signal.SIG_IGN)
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
            for child in (worker, daemon):
                try:
                    stop(child, cfg['cleanup_seconds'] / 2)
                except Exception as error:
                    result = {'status': 'failed', **failure(error), 'remote_cleanup_verified': False}
            selector.close()
            for sig, handler in previous.items():
                signal.signal(sig, handler)
        print(json.dumps({'event': 'result', **result}), flush=True)
    return 0 if result['status'] == 'complete' else 2


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--local', action='store_true', help='unpaid local Docker smoke')
    parser.add_argument('--supervise', action='store_true', help='internal Modal entrypoint')
    parser.add_argument('--settings', default='{}')
    parser.add_argument('--output', type=pathlib.Path)
    parser.add_argument('--progress', type=pathlib.Path)
    args = parser.parse_args(argv)
    from modal_engine import policy
    cfg = policy(json.loads(args.settings))
    if args.supervise:
        return supervise(cfg)
    if not args.local or not args.output:
        parser.error('local smoke requires --local --output')
    # Child logs contain internal identifiers; keep them separate from exported
    # aggregate JSON. The supervisor redirects the whole child to private logs.
    row = smoke(cfg, args.progress)
    args.output.write_text(json.dumps(row))
    return 0 if row['status'] == 'complete' else 2


if __name__ == '__main__':
    raise SystemExit(main())
