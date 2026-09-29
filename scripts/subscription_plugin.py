"""Alert-rule plugins of the local harness.

Every stack pins two subscription plugins through the config's ``plugins``
list, next to the external normalizer (scripts/normalizer_plugin.py), which
stays in the single ``plugin`` pin:

* ``alerts``: the first-party keyword alerts plugin plugins/alerts, installed
  into the SDK virtualenv. It is on by default; ``QUIVR_ALERTS=off make dev``
  leaves it unpinned. ``make verify`` always pins it and finds its evaluator
  through QUIVR_TEST_KEYWORD_EVALUATOR.
* ``alert-rules``: the `quivr plugin init --kind subscription` template,
  scaffolded once per stack. The acceptance tests find its evaluator through
  QUIVR_TEST_ALERT_EVALUATOR.

Each runs as its own process with the repository's Python Plugin SDK. Every
oracle is a public HTTP read or a webhook.
"""
import os, pathlib, signal, subprocess, time, urllib.request

import normalizer_plugin

NAME = 'alert-rules'
MODULE = NAME.replace('-', '_')
VERSION = '0.1.0'
EVALUATOR = f'{NAME}@{VERSION}'
ALERTS = normalizer_plugin.ROOT / 'plugins' / 'alerts'
KEYWORD_EVALUATOR = 'alerts@0.1.0'
# Installer configuration of the alerts pin: the built-in field names only. A
# deployment maps its own names here, e.g. {"fields": {"author": "/extensions/<namespace>/data/author"}}.
ALERTS_CONFIGURATION = {}


def from_environment():
    """Whether `make dev` pins the keyword alerts plugin: QUIVR_ALERTS, default on."""
    value = (os.environ.get('QUIVR_ALERTS') or 'on').lower()
    if value not in ('on', 'off'):
        raise ValueError(f'QUIVR_ALERTS is on or off, not {value!r}')
    return value == 'on'


def select(stack, enabled):
    stack.state['alerts_plugin'] = bool(enabled)
    stack.save()


def directory(stack):
    return stack.directory / 'subscription-plugin'


def manifest(stack):
    return directory(stack) / 'quivr-plugin.yaml'


def running(stack):
    """The plugins this stack runs: (name, directory, module, port state key)."""
    items = []
    if manifest(stack).exists():
        items.append((NAME, directory(stack), MODULE, 'subscription_plugin_port'))
    if stack.state.get('alerts_plugin', True):
        items.append(('alerts', ALERTS, 'alerts', 'alerts_plugin_port'))
    return items


def pins(stack):
    """The `plugins` entries of QUIVR_CONFIG: the scaffolded template and, unless QUIVR_ALERTS=off, plugins/alerts."""
    out = []
    for name, path, _, port in running(stack):
        stack.state.setdefault(port, normalizer_plugin.stack_port())
        pin = {'manifest': str(path / 'quivr-plugin.yaml'), 'endpoint': f"http://127.0.0.1:{stack.state[port]}"}
        if name == 'alerts':
            pin['configuration'] = ALERTS_CONFIGURATION
        out.append(pin)
    return out


def describe(stack):
    """One line for `make dev`: which alert-rule plugins are pinned and how to change it."""
    names = ', '.join(f'{n} ({KEYWORD_EVALUATOR if n == "alerts" else EVALUATOR})' for n, *_ in running(stack))
    return f'Alert-rule plugins: {names or "none"} (QUIVR_ALERTS=on|off)'


def prepare(stack):
    """Scaffold the template once, install plugins/alerts into the SDK virtualenv, and assign ports."""
    stack.state.setdefault('subscription_plugin_port', normalizer_plugin.stack_port())
    stack.state.setdefault('alerts_plugin_port', normalizer_plugin.stack_port())
    stack.save()
    if not manifest(stack).exists():
        with (stack.directory / 'subscription-plugin-init.log').open('w') as log:
            subprocess.run([str(stack.directory / 'quivr'), 'plugin', 'init', NAME, '--kind', 'subscription', '--dir', str(directory(stack))],
                           cwd=normalizer_plugin.ROOT, check=True, stdout=log, stderr=log)
    if stack.state.get('alerts_plugin', True):
        python = normalizer_plugin.python()
        if subprocess.run([str(python), '-c', 'import alerts.rule'], cwd=normalizer_plugin.SDK, capture_output=True).returncode:
            subprocess.run([str(python), '-m', 'pip', 'install', '-q', '--disable-pip-version-check',
                            '-c', 'contracts/http/v0/checks/requirements.txt', '-e', str(ALERTS)], cwd=normalizer_plugin.ROOT, check=True)


def healthy(stack, port='subscription_plugin_port'):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{stack.state[port]}/v0/health", timeout=1) as r:
            return r.status == 200
    except (OSError, KeyError):
        return False


def start(stack):
    """Run each plugin like `quivr plugin dev` does, then wait for its health."""
    stop(stack)
    for name, path, module, port in running(stack):
        env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(stack.state[port]), 'QUIVR_PLUGIN_MANIFEST': str(path / 'quivr-plugin.yaml')}
        logfile = stack.directory / ('subscription-plugin.log' if name == NAME else 'alerts-plugin.log')
        with logfile.open('a') as log:
            p = subprocess.Popen([str(normalizer_plugin.python()), '-m', module], cwd=path, env=env, stdout=log, stderr=log, start_new_session=True)
        stack.state[f'{port}_pid'] = p.pid
        stack.save()
        deadline = time.monotonic() + 30
        while not healthy(stack, port):
            if p.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError(f'{name} alert-rule plugin not healthy; inspect {logfile}')
            time.sleep(.1)


def stop(stack):
    # subscription_plugin_pid is where stacks started before plugins/alerts recorded the template.
    for port, key in (('subscription_plugin_port', 'subscription_plugin_pid'), ('subscription_plugin_port', 'subscription_plugin_port_pid'), ('alerts_plugin_port', 'alerts_plugin_port_pid')):
        pid = stack.state.pop(key, None)
        stack.save()
        if pid is None:
            continue
        try:
            os.killpg(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            continue
        deadline = time.monotonic() + 10
        while healthy(stack, port) and time.monotonic() < deadline:
            time.sleep(.05)


def environment():
    return {'QUIVR_TEST_ALERT_EVALUATOR': EVALUATOR, 'QUIVR_TEST_KEYWORD_EVALUATOR': KEYWORD_EVALUATOR}


def verify(stack):
    """Match, non-match, invalid expression and metadata rules through the pinned template."""
    stack.tests('^TestAlertPlugin(Decides|Metadata)', environment())


def keywords(stack):
    """Keyword alerts through plugins/alerts: matched terms in the evidence, filters, invalid trees."""
    stack.tests('^TestKeywordAlerts', environment())


def outage(stack):
    """With the rule plugins stopped, evaluation waits and the platform stays healthy; it completes after the restart."""
    stop(stack)
    stack.tests('^TestAlertPluginOutageDelays$', environment())
    start(stack)
    stack.tests('^TestAlertPluginOutageRecovers$', environment())
