"""Alert-rule plugins of the local harness.

Every stack pins two subscription plugins through the config's ``plugins``
list, next to the external normalizer (scripts/normalizer_plugin.py), which
stays in the single ``plugin`` pin:

* ``alerts``: the first-party alerts plugin plugins/alerts (keyword and
  described alerts), installed into the SDK virtualenv. It is on by default;
  ``QUIVR_ALERTS=off make dev`` leaves it unpinned. ``make verify`` always pins
  it and finds its evaluator through QUIVR_TEST_KEYWORD_EVALUATOR. Described
  alerts are offered (the pin's ``kinds``) only when the plugin has a
  classifier: in ``make verify`` the fake System One server
  (alerts.fake_system_one) with a test key, never TypeSafe; in ``make dev``
  TypeSafe itself when TYPESAFE_API_KEY is set in the environment.
* ``alert-rules``: the `quivr plugin init --kind subscription` template,
  scaffolded once per stack. The acceptance tests find its evaluator through
  QUIVR_TEST_ALERT_EVALUATOR.

Each runs as its own process with the repository's Python Plugin SDK. Every
oracle is a public HTTP read or a webhook.
"""
import json, os, pathlib, shutil, signal, subprocess, time, urllib.request

import normalizer_plugin

NAME = 'alert-rules'
MODULE = NAME.replace('-', '_')
VERSION = '0.1.0'
EVALUATOR = f'{NAME}@{VERSION}'
ALERTS = normalizer_plugin.ROOT / 'plugins' / 'alerts'
KEYWORD_EVALUATOR = 'alerts@0.3.0'
# Installer configuration of the alerts pin: the built-in field names only. A
# deployment maps its own names here, e.g. {"fields": {"author": "/extensions/<namespace>/data/author"}}.
ALERTS_CONFIGURATION = {}
# The bearer key the fake System One server accepts: a test value, never a TypeSafe key.
FAKE_KEY = 'test-key'


def from_environment():
    """Whether `make dev` pins the keyword alerts plugin: QUIVR_ALERTS, default on."""
    value = (os.environ.get('QUIVR_ALERTS') or 'on').lower()
    if value not in ('on', 'off'):
        raise ValueError(f'QUIVR_ALERTS is on or off, not {value!r}')
    return value == 'on'


def described_mode(verification):
    """Where described alerts are judged: the fake server in verification, TypeSafe
    when TYPESAFE_API_KEY is set, otherwise nowhere (described alerts are not offered)."""
    if verification:
        return 'fake'
    return 'typesafe' if os.environ.get('TYPESAFE_API_KEY', '').strip() else 'off'


def select(stack, enabled, described='off'):
    stack.state['alerts_plugin'] = bool(enabled)
    stack.state['described'] = described
    stack.save()


def kinds(stack, described=None):
    """The alert kinds the alerts pin offers: described only with a classifier."""
    mode = stack.state.get('described', 'off') if described is None else described
    local = ['keywords', 'meaning', 'keywords_or_meaning', 'keywords_and_meaning']
    return local + ['described'] if mode != 'off' else local


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


def pins(stack, described=None):
    """The `plugins` entries of QUIVR_CONFIG: the scaffolded template and, unless QUIVR_ALERTS=off, plugins/alerts.

    ``described='off'`` pins alerts as an installation without a TypeSafe key would."""
    out = []
    for name, path, _, port in running(stack):
        stack.state.setdefault(port, normalizer_plugin.stack_port())
        pin = {'manifest': str(path / 'quivr-plugin.yaml'), 'endpoint': f"http://127.0.0.1:{stack.state[port]}"}
        if name == 'alerts':
            pin['configuration'] = ALERTS_CONFIGURATION
            pin['kinds'] = kinds(stack, described)
        out.append(pin)
    return out


def describe(stack):
    """One line for `make dev`: which alert-rule plugins are pinned and how to change it."""
    names = ', '.join(f'{n} ({KEYWORD_EVALUATOR if n == "alerts" else EVALUATOR})' for n, *_ in running(stack))
    line = f'Alert-rule plugins: {names or "none"} (QUIVR_ALERTS=on|off)'
    if stack.state.get('alerts_plugin', True):
        mode = stack.state.get('described', 'off')
        line += {'fake': '; described alerts judged by the fake System One server',
                 'typesafe': '; described alerts judged by TypeSafe (article text is sent to it)',
                 'off': '; described alerts off (set TYPESAFE_API_KEY to offer them)'}[mode]
    return line


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


def fake_url(stack):
    return f"http://127.0.0.1:{stack.state['fake_system_one_port']}"


def fake_healthy(stack):
    try:
        with urllib.request.urlopen(fake_url(stack) + '/requests', timeout=1) as r:
            return r.status == 200
    except (OSError, KeyError):
        return False


def start_fake(stack):
    """The fake System One server described alerts talk to in verification (alerts.fake_system_one)."""
    stack.state.setdefault('fake_system_one_port', normalizer_plugin.stack_port())
    with (stack.directory / 'fake-system-one.log').open('a') as log:
        p = subprocess.Popen([str(normalizer_plugin.python()), '-m', 'alerts.fake_system_one', '--port', str(stack.state['fake_system_one_port']), '--key', FAKE_KEY],
                             cwd=ALERTS, stdout=log, stderr=log, start_new_session=True)
    stack.state['fake_system_one_pid'] = p.pid
    stack.save()
    deadline = time.monotonic() + 30
    while not fake_healthy(stack):
        if p.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('fake System One server not healthy; inspect ' + str(stack.directory / 'fake-system-one.log'))
        time.sleep(.05)


def classifier_environment(stack):
    """TYPESAFE_* of the alerts plugin: the fake's, TypeSafe's from the environment, or none."""
    mode = stack.state.get('described', 'off')
    if mode == 'fake':
        return {'TYPESAFE_API_KEY': FAKE_KEY, 'TYPESAFE_API_URL': fake_url(stack) + '/v1/systemone'}
    if mode == 'typesafe':
        return {}
    return {'TYPESAFE_API_KEY': ''}


def start(stack):
    """Run each plugin like `quivr plugin dev` does, then wait for its health."""
    stop(stack)
    if stack.state.get('alerts_plugin', True) and stack.state.get('described') == 'fake':
        start_fake(stack)
    for name, path, module, port in running(stack):
        env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(stack.state[port]), 'QUIVR_PLUGIN_MANIFEST': str(path / 'quivr-plugin.yaml')}
        if name == 'alerts':
            env.update(classifier_environment(stack))
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
    for port, key in (('subscription_plugin_port', 'subscription_plugin_pid'), ('subscription_plugin_port', 'subscription_plugin_port_pid'), ('alerts_plugin_port', 'alerts_plugin_port_pid'), (None, 'fake_system_one_pid')):
        pid = stack.state.pop(key, None)
        stack.save()
        if pid is None:
            continue
        try:
            os.killpg(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            continue
        deadline = time.monotonic() + 10
        while (fake_healthy(stack) if port is None else healthy(stack, port)) and time.monotonic() < deadline:
            time.sleep(.05)


def environment(stack=None):
    env = {'QUIVR_TEST_ALERT_EVALUATOR': EVALUATOR, 'QUIVR_TEST_KEYWORD_EVALUATOR': KEYWORD_EVALUATOR}
    if stack is not None and stack.state.get('described') == 'fake':
        env['QUIVR_TEST_FAKE_SYSTEM_ONE_URL'] = fake_url(stack)
    return env


def verify(stack):
    """Match, non-match, invalid expression and metadata rules through the pinned template."""
    stack.tests('^TestAlertPlugin(Decides|Metadata)', environment())


def keywords(stack):
    """Keyword alerts through plugins/alerts: matched terms in the evidence, filters, invalid trees."""
    stack.tests('^TestKeywordAlerts', environment())


def vectors(stack):
    base = f"http://127.0.0.1:{stack.state['api_port']}"
    def call(method, path, body=None):
        request = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None,
                                         headers={'Authorization': 'Bearer ' + stack.state['admin'], 'Content-Type': 'application/json'}, method=method)
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response)
    key = stack.name + '-vectors'
    corpus = call('POST', '/v0/corpora', {'name': 'Local vector alerts', 'idempotency_key': key})['corpus_id']
    cursor = call('GET', '/v0/changes?corpus_id=' + corpus)['next_cursor']
    query = call('POST', '/v0/saved-queries', {'idempotency_key': key, 'name': 'Port strikes', 'definition': {
        'corpus_ids': [corpus], 'expression': {'kind': 'meaning', 'meaning_check': 'vectors', 'description': 'Labour strikes at ports and harbours'},
        'retrieval_profile': 'default', 'temporal_policy': 'from_activation'}})
    version = query['current_version']['version_id']
    subscription = call('POST', '/v0/subscriptions', {'idempotency_key': key, 'name': 'Local port strikes',
        'saved_query_id': query['saved_query_id'], 'saved_query_version_id': version,
        'evaluator': {'plugin_id': 'alerts', 'version': '0.3.0', 'configuration': {'wait_for_enrichment': False}},
        'destination_id': 'local-receiver-org-a'})
    env = {**environment(stack), 'QUIVR_TEST_VECTOR_CORPUS': corpus, 'QUIVR_TEST_VECTOR_QUERY': query['saved_query_id'],
           'QUIVR_TEST_VECTOR_VERSION': version, 'QUIVR_TEST_VECTOR_SUBSCRIPTION': subscription['subscription_id'],
           'QUIVR_TEST_VECTOR_CURSOR': cursor}
    stack.compose('stop', 'tei')
    try:
        stack.tests('^TestVectorAlertsPending$', env)
    finally:
        stack.compose('up', '-d', '--wait', '--wait-timeout', '180', 'tei')
    stack.tests('^TestVectorAlertsEnrichmentMatchesOnce$', env)


def described(stack):
    """A described alert exposes classifier evidence through the public Match.
    Preview judges recent articles through the same plugin and saves nothing."""
    stack.tests('^(TestDescribedAlertEvidenceReachesTheAPI|TestSubscriptionPreview)', environment(stack))


def upgrade(stack):
    """The template's next build, 0.2.0, runs beside the pinned 0.1.0: TestAlertPluginUpgrade registers and
    activates it, migrates Subscriptions to it, rolls back and migrates them back (THE-805). It stops after."""
    copy = stack.directory / 'subscription-plugin-next'
    shutil.rmtree(copy, ignore_errors=True)
    shutil.copytree(directory(stack), copy, ignore=shutil.ignore_patterns('__pycache__'))
    manifest_next = copy / 'quivr-plugin.yaml'
    manifest_next.write_text(manifest_next.read_text().replace(f'version: {VERSION}', 'version: 0.2.0', 1))
    stack.state.setdefault('subscription_plugin_next_port', normalizer_plugin.stack_port())
    stack.save()
    port = stack.state['subscription_plugin_next_port']
    env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(manifest_next)}
    with (stack.directory / 'subscription-plugin-next.log').open('a') as log:
        p = subprocess.Popen([str(normalizer_plugin.python()), '-m', MODULE], cwd=copy, env=env, stdout=log, stderr=log, start_new_session=True)
    try:
        deadline = time.monotonic() + 30
        while not healthy(stack, 'subscription_plugin_next_port'):
            if p.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError(f'{NAME} 0.2.0 not healthy; inspect {stack.directory / "subscription-plugin-next.log"}')
            time.sleep(.1)
        stack.tests('^TestAlertPluginUpgrade$', {**environment(), 'QUIVR_TEST_ALERT_UPGRADE_ENDPOINT': f'http://127.0.0.1:{port}', 'QUIVR_TEST_ALERT_UPGRADE_MANIFEST': str(manifest_next),
                                                  'QUIVR_TEST_ALERT_PINNED_ENDPOINT': f"http://127.0.0.1:{stack.state['subscription_plugin_port']}"})
    finally:
        try:
            os.killpg(p.pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
        p.wait(timeout=10)


def outage(stack):
    """With the rule plugins stopped, evaluation waits and the platform stays healthy; it completes after the restart."""
    stop(stack)
    stack.tests('^TestAlertPluginOutageDelays$', environment())
    start(stack)
    stack.tests('^TestAlertPluginOutageRecovers$', environment())
