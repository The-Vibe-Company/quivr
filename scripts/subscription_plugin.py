"""Alert-rule plugin of the local harness.

Every stack pins the `quivr plugin init --kind subscription` template,
scaffolded once per stack as ``alert-rules``, next to the external normalizer
(scripts/normalizer_plugin.py): the normalizer stays in the config's single
``plugin`` pin and the alert rule goes to ``plugins``, so both run together.
It runs as its own process with the repository's Python Plugin SDK. The
acceptance tests find its evaluator through QUIVR_TEST_ALERT_EVALUATOR. Every
oracle is a public HTTP read or a webhook.
"""
import os, pathlib, signal, subprocess, time, urllib.request

import normalizer_plugin

NAME = 'alert-rules'
MODULE = NAME.replace('-', '_')
VERSION = '0.1.0'
EVALUATOR = f'{NAME}@{VERSION}'


def directory(stack):
    return stack.directory / 'subscription-plugin'


def manifest(stack):
    return directory(stack) / 'quivr-plugin.yaml'


def pins(stack):
    """The `plugins` entries of QUIVR_CONFIG: none until the template is scaffolded."""
    if not manifest(stack).exists():
        return []
    stack.state.setdefault('subscription_plugin_port', normalizer_plugin.stack_port())
    return [{'manifest': str(manifest(stack)), 'endpoint': f"http://127.0.0.1:{stack.state['subscription_plugin_port']}"}]


def prepare(stack):
    """Scaffold the template once with the stack's quivr binary and assign its port."""
    stack.state.setdefault('subscription_plugin_port', normalizer_plugin.stack_port())
    stack.save()
    if not manifest(stack).exists():
        with (stack.directory / 'subscription-plugin-init.log').open('w') as log:
            subprocess.run([str(stack.directory / 'quivr'), 'plugin', 'init', NAME, '--kind', 'subscription', '--dir', str(directory(stack))],
                           cwd=normalizer_plugin.ROOT, check=True, stdout=log, stderr=log)


def healthy(stack):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{stack.state['subscription_plugin_port']}/v0/health", timeout=1) as r:
            return r.status == 200
    except OSError:
        return False


def start(stack):
    """Run the template like `quivr plugin dev` does, then wait for its health."""
    stop(stack)
    env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(stack.state['subscription_plugin_port']), 'QUIVR_PLUGIN_MANIFEST': str(manifest(stack))}
    with (stack.directory / 'subscription-plugin.log').open('a') as log:
        p = subprocess.Popen([str(normalizer_plugin.python()), '-m', MODULE], cwd=directory(stack), env=env, stdout=log, stderr=log, start_new_session=True)
    stack.state['subscription_plugin_pid'] = p.pid
    stack.save()
    deadline = time.monotonic() + 30
    while not healthy(stack):
        if p.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('alert-rule plugin not healthy; inspect ' + str(stack.directory / 'subscription-plugin.log'))
        time.sleep(.1)


def stop(stack):
    pid = stack.state.pop('subscription_plugin_pid', None)
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


def environment():
    return {'QUIVR_TEST_ALERT_EVALUATOR': EVALUATOR}


def verify(stack):
    """Match, non-match, invalid expression and metadata rules through the pinned template."""
    stack.tests('^TestAlertPlugin(Decides|Metadata)', environment())


def outage(stack):
    """With the rule plugin stopped, evaluation waits and the platform stays healthy; it completes after the restart."""
    stop(stack)
    stack.tests('^TestAlertPluginOutageDelays$', environment())
    start(stack)
    stack.tests('^TestAlertPluginOutageRecovers$', environment())
