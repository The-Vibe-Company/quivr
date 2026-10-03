"""Run the offline push-source sample in local and guide-verification stacks."""
import os
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
    env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port(stack))}
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
