"""Run the offline push-source sample in local and guide-verification stacks."""
import os
import base64
import json
import secrets
import signal
import subprocess
import time
import urllib.request

import normalizer_plugin
import ports


def directory(stack):
    return stack.source / 'plugins' / 'push-source'


def port(stack):
    if 'push_source_port' not in stack.state:
        stack.state['push_source_port'] = ports.allocate()
        stack.save()
    return stack.state['push_source_port']


def pins(stack):
    return [{'manifest': str(directory(stack) / 'quivr-plugin.yaml'),
             'endpoint': f'http://127.0.0.1:{port(stack)}'}]


def healthy(stack):
    try:
        with urllib.request.urlopen(f'http://127.0.0.1:{port(stack)}/v0/health', timeout=1) as response:
            return response.status == 200
    except OSError:
        return False


def start(stack):
    stop(stack)
    # Keep the local signing secret across plugin/api/worker restarts. The
    # stack directory is private and gitignored, like its existing keys.
    key_file = stack.directory / 'push-source-signing.json'
    if not key_file.exists():
        ring = {'active': 'local', 'keys': [{'id': 'local',
                'secret': base64.urlsafe_b64encode(secrets.token_bytes(32)).decode().rstrip('=')}]}
        with key_file.open('x', opener=lambda path, flags: os.open(path, flags, 0o600)) as output:
            json.dump(ring, output)
    ring = json.loads(key_file.read_text())
    env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port(stack)),
           'QUIVR_PLUGIN_SIGNING_KEYS': json.dumps(ring)}
    env.pop('QUIVR_ENGINE_PLUGIN_KEYS', None)
    with (stack.directory / 'push-source.log').open('a') as log:
        process = subprocess.Popen([str(normalizer_plugin.python()), '-m', 'push_source'],
                                   cwd=directory(stack), env=env, stdout=log, stderr=log, start_new_session=True)
    stack.state['push_source_pid'] = process.pid
    stack.save()
    deadline = time.monotonic() + 30
    while not healthy(stack):
        if process.poll() is not None or time.monotonic() >= deadline:
            raise RuntimeError('push source not healthy; inspect ' + str(stack.directory / 'push-source.log'))
        time.sleep(.05)


def engine_environment(stack):
    """Supply only engine processes with the local plugin's signing ring."""
    key_file = stack.directory / 'push-source-signing.json'
    if not key_file.exists():
        return {}
    engine_keys = json.loads(os.environ.get('QUIVR_ENGINE_PLUGIN_KEYS', '{}'))
    engine_keys['push-source'] = json.loads(key_file.read_text())
    return {'QUIVR_ENGINE_PLUGIN_KEYS': json.dumps(engine_keys)}


def stop(stack):
    pid = stack.state.pop('push_source_pid', None)
    stack.save()
    if pid is not None:
        try:
            os.killpg(pid, signal.SIGTERM)
        except ProcessLookupError:
            return
        deadline = time.monotonic() + 10
        while healthy(stack) and time.monotonic() < deadline:
            time.sleep(.05)
        if healthy(stack):
            raise RuntimeError('push source did not stop; inspect its process before restarting')
