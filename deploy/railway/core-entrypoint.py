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
# QUIVR_DEMO_PLUGINS=1. Only the worker calls them (normalization, alert
# evaluation), so only the worker runs them, on loopback; the API loads the same
# pins from the manifests and never contacts them.
PLUGIN_ROOT = pathlib.Path('/app/plugins')
PLUGIN_PYTHON = '/opt/quivr-plugins/bin/python'
PLUGINS = [
    {'id': 'pdf-text', 'module': 'pdf_text', 'port': 9900,
     'routes': [{'media_type': 'application/pdf', 'mode': 'required'}]},
    # TYPESAFE_API_KEY lets alerts decide described alerts (docs/described-alerts.md).
    # Only this plugin receives it, and its pin offers "described" only when it is set.
    {'id': 'alerts', 'module': 'alerts', 'port': 9910, 'secrets': ['TYPESAFE_API_KEY']},
]
# First-party Go connector plugins: core.Dockerfile builds every plugins/<id> with a
# go.mod into /usr/local/bin/quivr-<id> and keeps its manifest in /app/plugins/<id>.
# They are always pinned, and the worker always runs them, whatever QUIVR_DEMO_PLUGINS
# says: a Connector Instance of their kind keeps polling once created, and an unpinned
# kind would fail it with unsupported_connector_kind. ``configuration`` is the pin
# configuration (default {}); the private-address refusal stays on. ``push`` plugins
# also run beside the API, which relays the webhook deliveries of their kinds to them
# (the instance webhook addresses need QUIVR_PUBLIC_URL).
CONNECTORS = [
    {'id': 'rss', 'port': 9920},
    {'id': 'x-list', 'port': 9930, 'push': True},
    # Microsoft 365 mail on the public cloud endpoints (the plugin's defaults).
    {'id': 'm365-mail', 'port': 9940},
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


def connector_pins():
    return [{'manifest': str(PLUGIN_ROOT / c['id'] / 'quivr-plugin.yaml'), 'endpoint': f"http://127.0.0.1:{c['port']}",
             'configuration': c.get('configuration', {})} for c in CONNECTORS]


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
    config = {
        'database_url': env['DATABASE_URL'],
        'cursor_key': env['QUIVR_CURSOR_KEY'],
        'listen': '0.0.0.0:8080',
        'probe_listen': '0.0.0.0:' + env.get('PORT', '8081'),
        'temporal_address': env['TEMPORAL_ADDRESS'],
        'weaviate_url': env['WEAVIATE_URL'],
        'tei_url': env['TEI_URL'],
        'tokenizer': {'python': '/app/.scratch/tokenizer/venv/bin/python',
                      'script': '/app/scripts/token_offsets.py',
                      'model': '/app/.scratch/tokenizer/tokenizer.json'},
        's3': {'endpoint': env['S3_ENDPOINT'], 'access_key': env['S3_ACCESS_KEY'],
               'secret_key': env['S3_SECRET_KEY'], 'bucket': 'quivr-content'},
        'keys': {key: {'organization': 'quivr-demo',
                       'actions': actions,
                       'corpora': ['*']}},
    }
    if CONNECTORS:
        config['plugins'] = connector_pins()
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
    plugin, the API only the connector plugins it relays push deliveries to. Its
    environment carries only the secrets that plugin declares (alerts:
    TYPESAFE_API_KEY), never the core's.

    The plugins are first-party code under the same user as the worker, not an isolation boundary.
    """
    commands = []
    for connector in CONNECTORS:
        if role == 'api' and not connector.get('push'):
            continue
        directory = PLUGIN_ROOT / connector['id']
        child = {'PATH': env.get('PATH', '/usr/local/bin:/usr/bin:/bin'),
                 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(connector['port']),
                 'QUIVR_PLUGIN_MANIFEST': str(directory / 'quivr-plugin.yaml')}
        commands.append((connector['id'], ['/usr/local/bin/quivr-' + connector['id']], str(directory), child))
    if role == 'api' or not plugins_enabled(env):
        return commands
    for plugin in PLUGINS:
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
    # Only the API applies startup migrations; failures abort before serving.
    if mode == 'api':
        subprocess.run(['quivr', 'migrate'], check=True)
    # The worker calls every plugin, and the API the push connector plugins it relays
    # webhook deliveries to, so each runs those beside itself.
    sidecars = sidecar_commands(os.environ, mode) if mode in ('api', 'worker') else []
    if sidecars:
        sys.exit(supervise(sidecars + [('quivr ' + mode, ['quivr', mode], None, None)]))
    os.execvp('quivr', ['quivr', mode])


if __name__ == '__main__':
    try:
        main()
    except KeyError as error:
        sys.exit('Missing runtime variable: ' + str(error))
