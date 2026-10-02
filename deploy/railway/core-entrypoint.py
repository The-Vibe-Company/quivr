"""Translate Railway runtime variables into the existing core configuration."""
import base64
import hashlib
import hmac
import json
import os
import pathlib
import signal
import subprocess
import sys
import time

# First-party plugins baked into the core image (core.Dockerfile), pinned when
# QUIVR_DEMO_PLUGINS=1. The worker calls them (normalization, alert
# evaluation), so it runs them all on loopback. The API also calls a
# ``preview`` plugin, for Subscription previews, so it runs that one beside it.
PLUGIN_ROOT = pathlib.Path('/app/plugins')
PLUGIN_PYTHON = '/opt/quivr-plugins/bin/python'
PLUGINS = [
    {'id': 'pdf-text', 'module': 'pdf_text', 'port': 9900,
     'routes': [{'media_type': 'application/pdf', 'mode': 'required'}]},
    # TYPESAFE_API_KEY lets alerts decide described alerts (https://docs.quivr.thevibecompany.co/guides/described-alerts).
    # Its pin offers "described" only when the key is set.
    {'id': 'alerts', 'module': 'alerts', 'port': 9910, 'secrets': ['TYPESAFE_API_KEY'], 'preview': True},
]
# First-party Go connector plugins: core.Dockerfile builds every plugins/<id> with a
# go.mod into /usr/local/bin/quivr-<id> and keeps its manifest in /app/plugins/<id>.
# They are always pinned, and the worker always runs them, whatever QUIVR_DEMO_PLUGINS
# says: a Connector Instance of their kind keeps polling once created, and an unpinned
# kind would fail it with unsupported_connector_kind. ``configuration`` is the pin
# configuration (default {}); the private-address refusal stays on. ``push`` plugins
# also run beside the API, which relays the webhook deliveries of their kinds to them
# (the instance webhook addresses need QUIVR_PUBLIC_URL). The core.ingest
# ingestion plugin segments and embeds every Version for the worker and encodes
# queries for the API, so both run it (``api``); the core.retrieve retrieval
# plugin ranks searches, which only the API answers (``api``, not ``worker``).
# ``configuration`` may be a function of the runtime variables.
TOKENIZER = {'python': '/app/.scratch/tokenizer/venv/bin/python', 'model': '/app/.scratch/tokenizer/tokenizer.json'}
CONNECTORS = [
    {'id': 'rss', 'port': 9920},
    {'id': 'x-list', 'port': 9930, 'push': True},
    # Microsoft 365 mail on the public cloud endpoints (the plugin's defaults).
    {'id': 'm365-mail', 'port': 9940},
    # Token windows and E5 embeddings through the deployment's TEI (plugins/core-ingest).
    {'id': 'core-ingest', 'port': 9950, 'api': True,
     'configuration': lambda env: {'tei_url': env['TEI_URL'], 'tokenizer': TOKENIZER}},
    # Today's search: keywords, vectors or both, from the candidates the engine serves (plugins/core-retrieve).
    {'id': 'core-retrieve', 'port': 9960, 'api': True, 'worker': False},
]
# The demo Organization's webhook destination. The web facade reads Matches
# through the API, so nothing needs the webhook: the reserved .invalid name never
# resolves and every delivery attempt fails without leaving the container. The
# private-address refusal stays on.
DESTINATION_ID = 'demo-alerts-sink'
SINK_URL = 'http://alerts-sink.invalid/quivr-demo'


def plugins_enabled(env):
    return env.get('QUIVR_DEMO_PLUGINS') == '1'


def sink_secret(cursor_key):
    """A stable signing secret for the sink, derived from the cursor key: no extra variable."""
    key = hmac.new(cursor_key.encode(), b'quivr-demo-alerts-sink', hashlib.sha256).digest()
    return 'whsec_' + base64.b64encode(key).decode()


def described_enabled(env):
    return bool(env.get('TYPESAFE_API_KEY', '').strip())


def runtime_connectors(env):
    """Keep normal search and optionally add Jev for deep searches."""
    if env.get('QUIVR_DEMO_JEV_RERANK') != '1':
        return CONNECTORS
    return CONNECTORS + [{
        'id': 'jev-rerank', 'module': 'jev_rerank', 'port': 9970,
        'api': True, 'worker': False, 'secrets': ['TYPESAFE_API_KEY'],
        'configuration': {'candidate_count': 30, 'trim_tokens': '256',
                          'tokenizer_path': TOKENIZER['model'], 'ranking': 'noul',
                          'cache_entries': 4096},
    }]


def plugin_pins(env):
    pins = []
    for plugin in PLUGINS:
        pin = {'manifest': str(PLUGIN_ROOT / plugin['id'] / 'quivr-plugin.yaml'),
               'endpoint': f"http://127.0.0.1:{plugin['port']}", 'configuration': {}}
        if 'routes' in plugin:
            pin['routes'] = plugin['routes']
        if plugin['id'] == 'alerts':
            # api and worker must agree: both read the same TYPESAFE_API_KEY.
            pin['kinds'] = ['keywords', 'described'] if described_enabled(env) else ['keywords']
        pins.append(pin)
    return pins


def connector_pins(env):
    def configuration(c):
        value = c.get('configuration', {})
        return value(env) if callable(value) else value
    return [{'manifest': str(PLUGIN_ROOT / c['id'] / 'quivr-plugin.yaml'), 'endpoint': f"http://127.0.0.1:{c['port']}",
             'configuration': configuration(c)} for c in runtime_connectors(env)]


def build_config(env):
    """Build the core configuration from Railway runtime variables."""
    key = env['QUIVR_API_KEY']
    # changes:read feeds the web app's live Veille page (read-only, fenced to the demo corpus).
    actions = ['corpora:read', 'corpora:write', 'content:read', 'content:write', 'search:query',
               'changes:read']
    # Opt-in: lets the web app list, create and watch Connector Instances.
    if env.get('QUIVR_DEMO_CONNECTORS') == '1':
        actions += ['connectors:read', 'connectors:write']
    # Opt-in: the Alertes tab creates Saved Queries and Subscriptions and reads their Matches.
    if plugins_enabled(env):
        actions += ['monitoring:read', 'monitoring:write']
    # Opt-in: the read-only Admin tab follows documents through their steps.
    if env.get('QUIVR_DEMO_ADMIN') == '1':
        actions += ['observability:read']
    config = {
        'database_url': env['DATABASE_URL'],
        'cursor_key': env['QUIVR_CURSOR_KEY'],
        'listen': '0.0.0.0:8080',
        'probe_listen': '0.0.0.0:' + env.get('PORT', '8081'),
        'temporal_address': env['TEMPORAL_ADDRESS'],
        'weaviate_url': env['WEAVIATE_URL'],
        # Encodes queries for generations built before core.ingest, until each Corpus is rebuilt.
        'tei_url': env['TEI_URL'],
        's3': {'endpoint': env['S3_ENDPOINT'], 'access_key': env['S3_ACCESS_KEY'],
               'secret_key': env['S3_SECRET_KEY'], 'bucket': 'quivr-content'},
        'keys': {key: {'organization': 'quivr-demo',
                       'actions': actions,
                       'corpora': ['*']}},
        # The demo lists its most frequent searches, so it records query text (7 days).
        # Its queries are demo traffic; a deployment with private queries leaves this off.
        'observability': {'record_query_text': True},
    }
    # Optional operator key, never given to the web app: rebuilds a Corpus projection
    # (for example after a migration adds a projected field), reads the plugin
    # registry (plugins:admin) and the admin views (observability:read) from inside the deployment.
    operator = env.get('QUIVR_OPERATOR_KEY', '').strip()
    if operator:
        config['keys'][operator] = {'organization': 'quivr-demo', 'corpora': ['*'],
                                    'actions': ['corpora:read', 'projections:rebuild', 'operations:read',
                                                'plugins:admin', 'observability:read']}
    if CONNECTORS:
        config['plugins'] = connector_pins(env)
    if env.get('QUIVR_DEMO_JEV_RERANK') == '1':
        config['retrieval'] = {'profiles': {'default': 'core.retrieve/default', 'deep': 'jev.rerank/deep'}}
    if plugins_enabled(env):
        config['plugins'] = config.get('plugins', []) + plugin_pins(env)
        config['destinations'] = {DESTINATION_ID: {'organization': 'quivr-demo', 'url': SINK_URL,
                                                   'secret': sink_secret(env['QUIVR_CURSOR_KEY'])}}
    # Optional: without it the core starts and refuses only credential deposits.
    if env.get('QUIVR_CREDENTIAL_KEY'):
        config['credential_key'] = env['QUIVR_CREDENTIAL_KEY']
    # Optional: the API's public address, from which push instances get their webhook
    # address (x_list webhook mode); without it they only poll.
    if env.get('QUIVR_PUBLIC_URL', '').strip():
        config['public_url'] = env['QUIVR_PUBLIC_URL'].strip()
    return config


def sidecar_commands(env, role='worker'):
    """(name, argv, cwd, env) of each plugin process of a role: the worker runs every
    plugin but the retrieval plugin, the API only the connector plugins it relays push
    deliveries to, the ingestion plugin it encodes queries with, the retrieval plugin
    that ranks its searches, and the subscription plugins it calls for previews. Its
    environment carries only the secrets that plugin declares (alerts and Jev:
    TYPESAFE_API_KEY), never the core's.

    The plugins are first-party code under the same user as the worker, not an isolation boundary.
    """
    commands = []
    for connector in runtime_connectors(env):
        if role == 'api' and not (connector.get('push') or connector.get('api')):
            continue
        if role == 'worker' and connector.get('worker') is False:
            continue
        directory = PLUGIN_ROOT / connector['id']
        child = {'PATH': env.get('PATH', '/usr/local/bin:/usr/bin:/bin'),
                 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(connector['port']),
                 'QUIVR_PLUGIN_MANIFEST': str(directory / 'quivr-plugin.yaml')}
        child.update({name: env[name] for name in connector.get('secrets', []) if env.get(name, '').strip()})
        if 'module' in connector:
            child['PYTHONUNBUFFERED'] = '1'
            argv = [PLUGIN_PYTHON, '-m', connector['module']]
        else:
            argv = ['/usr/local/bin/quivr-' + connector['id']]
        commands.append((connector['id'], argv, str(directory), child))
    if not plugins_enabled(env):
        return commands
    for plugin in PLUGINS:
        if role == 'api' and not plugin.get('preview'):
            continue
        directory = PLUGIN_ROOT / plugin['id']
        child = {'PATH': env.get('PATH', '/usr/local/bin:/usr/bin:/bin'), 'PYTHONUNBUFFERED': '1',
                 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(plugin['port']),
                 'QUIVR_PLUGIN_MANIFEST': str(directory / 'quivr-plugin.yaml')}
        child.update({name: env[name] for name in plugin.get('secrets', []) if env.get(name, '').strip()})
        commands.append((plugin['id'], [PLUGIN_PYTHON, '-m', plugin['module']], str(directory), child))
    return commands


def supervise(commands, grace=10.0, poll=0.2):
    """Run the processes together; when one exits or SIGTERM arrives, stop the others.

    Returns 0 after a requested stop. Otherwise it returns the status of the
    first process to exit (1 if that was 0), so Railway restarts the container.
    """
    stopping, children, code = [], [], 1

    def stop(signum, _frame):
        stopping.append(signum)
        for _, child in children:
            if child.poll() is None:
                child.send_signal(signal.SIGTERM)

    previous = {s: signal.signal(s, stop) for s in (signal.SIGTERM, signal.SIGINT)}
    try:
        for name, argv, cwd, env in commands:
            children.append((name, subprocess.Popen(argv, cwd=cwd, env=env)))
            print(f'started {name}', file=sys.stderr, flush=True)
        while not stopping:
            exited = [(name, child.returncode) for name, child in children if child.poll() is not None]
            if exited:
                name, code = exited[0]
                print(f'{name} exited with status {code}; stopping the container', file=sys.stderr, flush=True)
                break
            time.sleep(poll)
        for _, child in children:
            if child.poll() is None:
                child.send_signal(signal.SIGTERM)
        deadline = time.monotonic() + grace
        for _, child in children:
            try:
                child.wait(timeout=max(0.0, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
        return 0 if stopping else (code or 1)
    finally:
        for s, handler in previous.items():
            signal.signal(s, handler)


def main():
    mode = os.environ.get('QUIVR_ROLE', 'api')
    if mode not in ('api', 'worker', 'migrate'):
        raise SystemExit('QUIVR_ROLE must be api, worker or migrate')
    config = build_config(os.environ)
    os.umask(0o077)
    path = pathlib.Path('/tmp/quivr-runtime.json')
    path.write_text(json.dumps(config))
    os.environ['QUIVR_CONFIG'] = str(path)
    # Plugin-only credentials belong to declared sidecar environments, not the engine.
    plugin_secrets = {name for plugin in PLUGINS + runtime_connectors(os.environ)
                      for name in plugin.get('secrets', [])}
    core_env = {name: value for name, value in os.environ.items() if name not in plugin_secrets}
    # Only the API applies startup migrations; failures abort before serving.
    if mode == 'api':
        subprocess.run(['quivr', 'migrate'], check=True, env=core_env)
    # The worker calls every plugin but the retrieval plugin, and the API the push
    # connector plugins it relays webhook deliveries to, the ingestion plugin it encodes
    # queries with and the retrieval plugin that ranks its searches, so each runs those
    # beside itself.
    sidecars = sidecar_commands(os.environ, mode) if mode in ('api', 'worker') else []
    if sidecars:
        sys.exit(supervise(sidecars + [('quivr ' + mode, ['quivr', mode], None, core_env)]))
    os.execvpe('quivr', ['quivr', mode], core_env)


if __name__ == '__main__':
    try:
        main()
    except KeyError as error:
        sys.exit('Missing runtime variable: ' + str(error))
