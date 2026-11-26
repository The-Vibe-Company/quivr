"""Harness-only scripted connector and alert rule, pinned like any plugin."""
import go_bin_cache
import plugin_environment
import os, signal, subprocess, time, urllib.request

import normalizer_plugin

ROOT = normalizer_plugin.ROOT
GO = os.environ.get('GO', 'go')
MANIFEST = ROOT / 'internal/plugins/devhost/fakeplugin/scriptedsource/fixture.yaml'


def binary(stack):
    return go_bin_cache.ensure('fixture-plugin', stack.source, './tests/fakeplugin', go=GO)


def pins(stack):
    stack.state.setdefault('fixture_plugin_port', normalizer_plugin.stack_port())
    stack.save()
    return [{'manifest': str(MANIFEST), 'endpoint': f"http://127.0.0.1:{stack.state['fixture_plugin_port']}"}]


def healthy(stack):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{stack.state['fixture_plugin_port']}/v0/health", timeout=1) as response:
            return response.status == 200
    except (OSError, KeyError):
        return False


def prepare(stack):
    pins(stack)
    binary(stack)


def start(stack):
    stop(stack)
    executable = binary(stack)
    env = {**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(stack.state['fixture_plugin_port']),
           'QUIVR_PLUGIN_MANIFEST': str(MANIFEST), 'QUIVR_FAKE_PLUGIN_MODE': 'fixture'}
    with (stack.directory / 'fixture-plugin.log').open('a') as log:
        process = subprocess.Popen([str(executable)], cwd=ROOT, env=env, stdout=log, stderr=log, start_new_session=True)
    stack.state['fixture_plugin_pid'] = process.pid
    stack.save()
    deadline = time.monotonic() + 30
    while not healthy(stack):
        if process.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('fixture plugin not healthy; inspect ' + str(stack.directory / 'fixture-plugin.log'))
        time.sleep(.05)


def stop(stack):
    pid = stack.state.pop('fixture_plugin_pid', None)
    stack.save()
    if pid is not None:
        try:
            os.killpg(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            return
        deadline = time.monotonic() + 10
        while healthy(stack) and time.monotonic() < deadline:
            time.sleep(.05)
