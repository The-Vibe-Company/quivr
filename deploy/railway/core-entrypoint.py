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
    if plugins_enabled(env):
        config['plugins'] = plugin_pins(env)
        config['destinations'] = {DESTINATION_ID: {'organization': 'quivr-demo', 'url': SINK_URL,
                                                   'secret': sink_secret(env['QUIVR_CURSOR_KEY'])}}
    # Optional: without it the core starts and refuses only credential deposits.
    if env.get('QUIVR_CREDENTIAL_KEY'):
        config['credential_key'] = env['QUIVR_CREDENTIAL_KEY']
    return config


def sidecar_commands(env):
    """(name, argv, cwd, env) of each plugin process. Its environment carries only the
    secrets that plugin declares (alerts: TYPESAFE_API_KEY), never the core's.

    The plugins are first-party code under the same user as the worker, not an isolation boundary.
    """
    commands = []
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
    # The worker is the only process that calls plugins, so it runs them beside itself.
    if mode == 'worker' and plugins_enabled(os.environ):
        sys.exit(supervise(sidecar_commands(os.environ) + [('quivr worker', ['quivr', 'worker'], None, None)]))
    os.execvp('quivr', ['quivr', mode])


if __name__ == '__main__':
    try:
        main()
    except KeyError as error:
        sys.exit('Missing runtime variable: ' + str(error))
