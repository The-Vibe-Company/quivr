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
from urllib.parse import urlsplit
from urllib.request import build_opener, ProxyHandler, HTTPRedirectHandler

# First-party plugins baked into the core image (core.Dockerfile), pinned when
# QUIVR_DEMO_PLUGINS=1, except newsml-g2, which is always pinned alongside
# the archive connector. The worker calls them (normalization, alert
# evaluation), so it runs them all on loopback. The API also calls a
# ``preview`` plugin, for Subscription previews, so it runs that one beside it.
PLUGIN_ROOT = pathlib.Path('/app/plugins')
PLUGIN_PYTHON = '/opt/quivr-plugins/bin/python'
PLUGINS = [
    {'id': 'newsml-g2', 'module': 'newsml_g2', 'port': 9905, 'always': True,
     'routes': [{'media_type': 'application/vnd.iptc.g2.newsitem+xml', 'mode': 'required'},
                {'media_type': 'application/vnd.iptc.g2.newsmessage+xml', 'mode': 'required'}]},
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
    {'id': 'object-storage-archive', 'port': 9990, 'signing_id': 'connector.object_storage_archive'},
    # Token windows and E5 embeddings through the deployment's TEI (plugins/core-ingest).
    {'id': 'core-ingest', 'port': 9950, 'api': True,
     'configuration': lambda env: {'tei_url': config_url(env, 'TEI_URL'), 'tokenizer': TOKENIZER}},
    # Today's search: keywords, vectors or both, from the candidates the engine serves (plugins/core-retrieve).
    {'id': 'core-retrieve', 'port': 9960, 'api': True, 'worker': False},
]
# The configure command emits a model-locked manifest without a provider call.
# /app is read-only to the runtime user; both files belong in private /tmp.
HOSTED_MANIFEST = '/tmp/hosted-embed/quivr-plugin.yaml'
HOSTED_EMBED = {'id': 'hosted-embed', 'signing_id': 'hosted.embed', 'port': 9980, 'api': True,
                'manifest': HOSTED_MANIFEST, 'secrets': ['AZURE_FOUNDRY_KEY']}
ENCODER_PYTHON = '/opt/query-encoder/venv/bin/python'
ENCODER_MODEL = '/opt/query-encoder/model'
ENCODER_URL = 'http://127.0.0.1:9995'
ENCODER_IDENTITY = {'model': 'google/embeddinggemma-2',
                    'model_revision': '914f7f89142e33e7',
                    'source_revision': '914f7f89142e33e77833254d9c9b90c3cef7303b',
                    'dimensions': 768}


def encoder_settings(env):
    if env.get('QUIVR_LOCAL_QUERY_ENCODER') != '1':
        return None
    if embedding_selection(env) != 'gemma':
        raise ValueError('QUIVR_LOCAL_QUERY_ENCODER requires QUIVR_DEMO_EMBEDDING=gemma')
    values = {}
    for name, default, limit in (('QUIVR_QUERY_ENCODER_THREADS', 4, 32),
                                  ('QUIVR_QUERY_ENCODER_STARTUP_SECONDS', 120, 600)):
        raw = env.get(name, str(default))
        if not raw.isascii() or not raw.isdigit() or not 1 <= int(raw) <= limit:
            raise ValueError(name + ' must be an integer from 1 to ' + str(limit))
        values[name] = int(raw)
    return values


def plugins_enabled(env):
    return env.get('QUIVR_DEMO_PLUGINS') == '1'


def described_enabled(env):
    return bool(env.get('TYPESAFE_API_KEY', '').strip())


def runtime_connectors(env):
    """Keep core.ingest reachable and optionally select hosted embeddings and Jev."""
    connectors = CONNECTORS
    selection = embedding_selection(env)
    # Opt-out of core.ingest (E5 through TEI) once hosted embeddings serve every
    # Corpus: new generations then carry only the hosted space.
    if selection and env.get('QUIVR_DEMO_CORE_INGEST', '').strip() == '0':
        connectors = [c for c in connectors if c['id'] != 'core-ingest']
    if selection:
        connectors = connectors + [{**HOSTED_EMBED, 'configuration': selected_hosted_configuration(env),
                                    'secrets': HOSTED_EMBED['secrets'] if selection == 'cohere' else ['EMBED_API_KEY'],
                                    'secret_names': {'AZURE_FOUNDRY_KEY': 'EMBED_API_KEY'} if selection == 'cohere' else {}}]
    if env.get('QUIVR_DEMO_JEV_RERANK') != '1':
        return connectors
    return connectors + [{
        'id': 'jev-rerank', 'module': 'jev_rerank', 'port': 9970,
        'api': True, 'worker': False, 'secrets': ['TYPESAFE_API_KEY'],
        'configuration': {'candidate_count': 30, 'trim_tokens': '256',
                          'tokenizer_path': TOKENIZER['model'], 'ranking': 'noul',
                          'cache_entries': 4096},
    }]


def embedding_selection(env):
    selection = env.get('QUIVR_DEMO_EMBEDDING', '').strip()
    if selection and selection not in ('gemma', 'cohere'):
        raise ValueError('QUIVR_DEMO_EMBEDDING must be gemma or cohere')
    return selection or ('cohere' if env.get('QUIVR_DEMO_HOSTED_EMBED') == '1' else '')


def selected_hosted_configuration(env):
    return gemma_configuration(env) if embedding_selection(env) == 'gemma' else hosted_configuration(env)


def gemma_configuration(env):
    endpoint = env.get('EMBED_URL', '').strip().rstrip('/')
    try:
        url = urlsplit(endpoint)
        port = url.port
    except ValueError:
        raise ValueError('EMBED_URL must be a valid HTTPS endpoint origin') from None
    private_cpu = env.get('QUIVR_SINGLE_MACHINE') == '1' and endpoint == 'http://embeddings:8080'
    if ((url.scheme != 'https' and not private_cpu) or not url.hostname
            or any(char.isspace() or ord(char) < 32 for char in endpoint)
            or port == 0 or url.netloc.endswith(':')
            or url.username is not None or url.password is not None
            or url.path or url.query or url.fragment):
        raise ValueError('EMBED_URL must be an HTTPS endpoint origin without credentials, path, query or fragment')
    key = env.get('EMBED_API_KEY', '')
    if not key or not key.isascii() or any(char.isspace() for char in key):
        raise ValueError('EMBED_API_KEY must be a nonempty ASCII bearer token without whitespace')
    configuration = {'format': 'openai', 'base_url': endpoint + '/v1', 'auth': 'bearer',
            'model': 'google/embeddinggemma-2', 'dimensions': 768,
            # The plugin revision field is bounded to 32 bytes; the image pins the full SHA.
            'model_revision': '914f7f89142e33e7',
            'query_template': 'task: search result | query: {query}',
            'document_template': 'title: {title} | text: {text}', 'title_source': 'title',
            'body_tokens': 512, 'max_chunks': 256,
            'rebalance_tail': True, 'tail_min_fraction': 0.25,
            'tokenizer': {'python': TOKENIZER['python'],
                          'model': '/app/.scratch/tokenizer/embeddinggemma-2.json',
                          'sha256': '4d777ef5bdc1aa36227abdfb77c3e49e7b9c892d16e1b6bda41c393504828be4'},
            # Exact model-tokenized body budget is separate from the full input window.
            'max_tokens_per_segment': 2048,
            'batch_size': 32, 'max_batch_tokens': 65536, 'max_concurrent_requests': 16,
            'request_timeout_ms': 10000, 'call_budget_ms': 90000,
            'usd_per_million_tokens': 0}
    if private_cpu:
        configuration.update(batch_size=1, max_batch_tokens=2048, max_concurrent_requests=1)
    return configuration


def hosted_configuration(env):
    for name in ('AZURE_FOUNDRY_ENDPOINT', 'AZURE_FOUNDRY_KEY'):
        if not env.get(name, '').strip():
            raise ValueError('Missing runtime variable: ' + name)
    return {'format': 'cohere',
            'base_url': config_url(env, 'AZURE_FOUNDRY_ENDPOINT').rstrip('/') + '/providers/cohere/v2',
            'auth': 'api-key', 'model': 'Cohere-Embed-V5-Pro', 'dimensions': 1024,
            # Conservative UTF-8 byte/token bound, not an exact provider token window.
            'max_tokens_per_segment': 6144,
            'batch_size': 32, 'max_batch_tokens': 196608, 'max_concurrent_requests': 16,
            'usd_per_million_tokens': 0.12}


def prepare_hosted_manifest(env):
    if not embedding_selection(env):
        return
    manifest = pathlib.Path(HOSTED_MANIFEST)
    manifest.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    configuration = manifest.parent / 'configuration.json'
    configuration.write_text(json.dumps(selected_hosted_configuration(env)))
    # Configure needs no credentials. Its errors name fields, never runtime values.
    with manifest.open('w') as output:
        subprocess.run(['/usr/local/bin/quivr-hosted-embed', 'configure', str(configuration)],
                       stdout=output, check=True,
                       env={'PATH': env.get('PATH', '/usr/local/bin:/usr/bin:/bin')})


def plugin_pins(env):
    pins = []
    for plugin in PLUGINS:
        if not plugin.get('always') and not plugins_enabled(env):
            continue
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
    return [{'manifest': c.get('manifest', str(PLUGIN_ROOT / c['id'] / 'quivr-plugin.yaml')), 'endpoint': f"http://127.0.0.1:{c['port']}",
             'configuration': configuration(c)} for c in runtime_connectors(env)]


# Resolve mounted secrets once so migration, core and sidecars use the same
# values even if a mounted secret rotates during startup.
REQUIRED_CORE_SECRETS = ('DATABASE_URL', 'QUIVR_CURSOR_KEY', 'S3_ACCESS_KEY', 'S3_SECRET_KEY')
OPTIONAL_CORE_SECRETS = ('QUIVR_CREDENTIAL_KEY', 'WEAVIATE_API_KEY', 'QUIVR_ENGINE_PLUGIN_KEYS',
                         'QUIVR_API_KEYS', 'QUIVR_TELEMETRY_HEADERS')
SCOPED_KEY_SECRETS = ('QUIVR_API_KEY', 'QUIVR_OPERATOR_KEY', 'QUIVR_QUEUE_KEY')
PLUGIN_SECRETS = ('TYPESAFE_API_KEY', 'AZURE_FOUNDRY_KEY', 'EMBED_API_KEY')


def runtime_environment(environ):
    env = dict(environ)
    for name in (*REQUIRED_CORE_SECRETS, *OPTIONAL_CORE_SECRETS, *SCOPED_KEY_SECRETS, *PLUGIN_SECRETS):
        path = env.pop(name + '_FILE', '')
        if path:
            if env.get(name):
                raise ValueError('Set either ' + name + ' or ' + name + '_FILE')
            try:
                value = pathlib.Path(path).read_bytes().decode('utf-8')
            except (OSError, ValueError):
                raise ValueError('Cannot read secret from ' + name + '_FILE') from None
            if value.endswith('\r\n'):
                value = value[:-2]
            elif value.endswith('\n'):
                value = value[:-1]
            if not value.strip() or '\0' in value:
                raise ValueError(name + '_FILE must contain a nonempty secret')
            env[name] = value
    for name in REQUIRED_CORE_SECRETS:
        if not env.get(name, '').strip():
            raise ValueError('Missing runtime secret: ' + name)
    return env


def config_url(env, name):
    value = env[name].strip()
    try:
        url = urlsplit(value)
        port = url.port
        if (url.scheme in ('http', 'https') and url.hostname and url.username is None
                and port != 0 and not url.netloc.endswith(':')
                and not any(char.isspace() or ord(char) < 32 for char in value)
                and not url.query and not url.fragment):
            return value
    except ValueError:
        pass
    raise ValueError(name + ' must be an HTTP(S) URL without credentials, query or fragment')


def engine_api_keys(env):
    key = env['QUIVR_API_KEY']
    organization = env.get('QUIVR_ORGANIZATION', 'quivr-demo').strip()
    if not organization:
        raise ValueError('QUIVR_ORGANIZATION must not be empty')
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
    keys = {key: {'organization': organization, 'actions': actions, 'corpora': ['*']}}
    # Optional operator key, never given to the web app: rebuilds a Corpus projection
    # (for example after a migration adds a projected field), reads the plugin
    # registry (plugins:admin), the admin views (observability:read) and archives (corpora:archive) or renames (corpora:rename)
    # Corpora from inside the deployment.
    operator = env.get('QUIVR_OPERATOR_KEY', '').strip()
    if operator:
        if operator == key:
            raise ValueError('API and operator keys must differ')
        keys[operator] = {'organization': organization, 'corpora': ['*'],
                                    'actions': ['corpora:read', 'projections:rebuild', 'operations:read', 'operations:write',
                                                'plugins:admin', 'observability:read', 'queues:read', 'corpora:archive', 'corpora:rename']}
    queue_key = env.get('QUIVR_QUEUE_KEY', '').strip()
    if queue_key:
        if queue_key in keys:
            raise ValueError('QUIVR_QUEUE_KEY must differ from API and operator keys')
        keys[queue_key] = {'organization': organization, 'corpora': ['*'],
                                     'actions': ['queues:read']}
    return keys


def build_config(env):
    """Build the nonsecret core configuration from Railway runtime variables."""
    recording = env.get('QUIVR_RECORD_QUERY_TEXT', '0' if env.get('QUIVR_SINGLE_MACHINE') == '1' else '1')
    if recording not in ('0', '1'):
        raise ValueError('QUIVR_RECORD_QUERY_TEXT must be 0 or 1')
    pool_limit = env.get('QUIVR_POSTGRES_MAX_CONNECTIONS', '16').strip()
    if not pool_limit.isdecimal() or not 1 <= int(pool_limit) <= 2147483647:
        raise ValueError('QUIVR_POSTGRES_MAX_CONNECTIONS must be a positive int32')
    config = {
        'postgres': {'max_connections': int(pool_limit)},
        'listen': '0.0.0.0:8080',
        'probe_listen': '0.0.0.0:' + env.get('PORT', '8081'),
        # Workers may start before the API builds and applies new migrations.
        'migration_wait': '10m',
        'temporal_address': env['TEMPORAL_ADDRESS'],
        'weaviate_url': config_url(env, 'WEAVIATE_URL'),
        # Encodes queries for generations built before core.ingest, until each Corpus is rebuilt.
        'tei_url': config_url(env, 'TEI_URL'),
        's3': {'endpoint': config_url(env, 'S3_ENDPOINT'), 'bucket': 'quivr-content'},
        # The demo lists its most frequent searches, so it records query text (7 days).
        # Its queries are demo traffic; a deployment with private queries leaves this off.
        'observability': {'record_query_text': recording == '1'},
    }
    # Omitted queue variables keep one worker serving both queues.
    worker = {}
    if 'QUIVR_WORKER_QUEUES' in env:
        queues = [queue.strip() for queue in env['QUIVR_WORKER_QUEUES'].split(',')]
        if any(queue not in ('live', 'bulk') for queue in queues) or len(set(queues)) != len(queues):
            raise ValueError('QUIVR_WORKER_QUEUES must select live, bulk or live,bulk')
        worker['queues'] = queues
    slots = {}
    for queue in ('live', 'bulk'):
        name = 'QUIVR_WORKER_' + queue.upper() + '_SLOTS'
        if name in env:
            try:
                value = int(env[name])
            except ValueError:
                raise ValueError(name + ' must be an integer from 1 to 1024') from None
            if not 1 <= value <= 1024:
                raise ValueError(name + ' must be an integer from 1 to 1024')
            slots[queue] = value
    if slots:
        worker['slots'] = slots
    if worker:
        config['worker'] = worker
    if CONNECTORS:
        config['plugins'] = connector_pins(env)
    config['plugins'] = config.get('plugins', []) + plugin_pins(env)
    if embedding_selection(env):
        # The default covers every source format after normalization. Keeping
        # core.ingest pinned serves historical generations, not E5 evaluation.
        config['ingestion'] = {'default': 'hosted.embed'}
    if env.get('QUIVR_DEMO_JEV_RERANK') == '1':
        config['retrieval'] = {'profiles': {'default': 'core.retrieve/default', 'deep': 'jev.rerank/deep'}}
    # Optional: the API's public address, from which push instances get their webhook
    # address (x_list webhook mode); without it they only poll.
    if env.get('QUIVR_PUBLIC_URL', '').strip():
        config['public_url'] = config_url(env, 'QUIVR_PUBLIC_URL')
    if 'QUIVR_CONNECTOR_MIN_INTERVAL' in env:
        interval = env['QUIVR_CONNECTOR_MIN_INTERVAL'].strip()
        if not interval:
            raise ValueError('QUIVR_CONNECTOR_MIN_INTERVAL must be a positive duration')
        config['connector_min_interval'] = interval  # The engine validates Go durations.
    # Optional: Versions a rebuild step covers in parallel (1-256, engine default 8).
    concurrency = env.get('QUIVR_REBUILD_CONCURRENCY', '').strip()
    if concurrency:
        if not concurrency.isdigit() or not 1 <= int(concurrency) <= 256:
            raise ValueError('QUIVR_REBUILD_CONCURRENCY must be an integer from 1 to 256')
        config['rebuild'] = {'concurrency': int(concurrency)}
    return config


class DuplicateSigningMember(ValueError):
    pass


def unique_json_members(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateSigningMember()
        result[key] = value
    return result


def engine_signing_environment(env):
    try:
        keys = json.loads(env.get('QUIVR_ENGINE_PLUGIN_KEYS') or '{}', object_pairs_hook=unique_json_members)
        if not isinstance(keys, dict):
            raise ValueError()
    except DuplicateSigningMember:
        raise ValueError('Invalid engine plugin signing configuration: duplicate JSON member in QUIVR_ENGINE_PLUGIN_KEYS') from None
    except (ValueError, TypeError):
        raise ValueError('Invalid engine plugin signing configuration') from None
    for connector in runtime_connectors(env):
        plugin_id = connector.get('signing_id')
        if plugin_id and plugin_id not in keys:
            secret = hmac.new(env['QUIVR_CURSOR_KEY'].encode(),
                              ('quivr-packaged-plugin:' + plugin_id).encode(), hashlib.sha256).digest()
            keys[plugin_id] = {'active': 'packaged', 'keys': [{'id': 'packaged',
                'secret': base64.urlsafe_b64encode(secret).decode().rstrip('=')}]}
    return {'QUIVR_ENGINE_PLUGIN_KEYS': json.dumps(keys)}


def sidecar_commands(env, role='worker', *, signing=None):
    """(name, argv, cwd, env) of each plugin process of a role: the worker runs every
    plugin but the retrieval plugin, the API only the connector plugins it relays push
    deliveries to, the ingestion plugin it encodes queries with, the retrieval plugin
    that ranks its searches, and the subscription plugins it calls for previews. Its
    environment carries only the secrets that plugin declares (alerts and Jev:
    TYPESAFE_API_KEY, hosted.embed: EMBED_API_KEY), never the core's.

    The plugins are first-party code under the same user as the worker, not an isolation boundary.
    """
    commands = []
    if role == 'migrate':
        return commands
    settings = encoder_settings(env) if role == 'api' else None
    if settings and role == 'api':
        commands.append(('text-encoder', [ENCODER_PYTHON, '-m', 'deploy.cpu.text_encoder',
                         '--model-dir', ENCODER_MODEL, '--port', '9995',
                         '--threads', str(settings['QUIVR_QUERY_ENCODER_THREADS'])], '/app',
                         {'PATH': env.get('PATH', '/usr/local/bin:/usr/bin:/bin'),
                          'PYTHONUNBUFFERED': '1', 'HF_HUB_OFFLINE': '1',
                          'TRANSFORMERS_OFFLINE': '1', 'HF_HUB_DISABLE_TELEMETRY': '1',
                          'TOKENIZERS_PARALLELISM': 'false'}))
    rings = json.loads((signing or engine_signing_environment(env))['QUIVR_ENGINE_PLUGIN_KEYS'])
    for connector in runtime_connectors(env):
        if role == 'api' and not (connector.get('push') or connector.get('api')):
            continue
        if role == 'worker' and connector.get('worker') is False:
            continue
        directory = PLUGIN_ROOT / connector['id']
        child = {'PATH': env.get('PATH', '/usr/local/bin:/usr/bin:/bin'),
                 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(connector['port']),
                 'QUIVR_PLUGIN_MANIFEST': connector.get('manifest', str(directory / 'quivr-plugin.yaml'))}
        if connector.get('signing_id'):
            child['QUIVR_PLUGIN_SIGNING_KEYS'] = json.dumps(rings[connector['signing_id']])
        child.update({connector.get('secret_names', {}).get(name, name): env[name]
                      for name in connector.get('secrets', []) if env.get(name, '').strip()})
        if settings and role == 'api' and connector['id'] == 'hosted-embed':
            child['QUIVR_HOSTED_QUERY_URL'] = ENCODER_URL + '/v1'
        if 'module' in connector:
            child['PYTHONUNBUFFERED'] = '1'
            argv = [PLUGIN_PYTHON, '-m', connector['module']]
        else:
            argv = ['/usr/local/bin/quivr-' + connector['id']]
        commands.append((connector['id'], argv, str(directory), child))
    for plugin in PLUGINS:
        if not plugin.get('always') and not plugins_enabled(env):
            continue
        if role == 'api' and not plugin.get('preview'):
            continue
        directory = PLUGIN_ROOT / plugin['id']
        child = {'PATH': env.get('PATH', '/usr/local/bin:/usr/bin:/bin'), 'PYTHONUNBUFFERED': '1',
                 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(plugin['port']),
                 'QUIVR_PLUGIN_MANIFEST': str(directory / 'quivr-plugin.yaml')}
        child.update({name: env[name] for name in plugin.get('secrets', []) if env.get(name, '').strip()})
        commands.append((plugin['id'], [PLUGIN_PYTHON, '-m', plugin['module']], str(directory), child))
    return commands


class RefuseRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def read_encoder_health(url, timeout):
    # Loopback readiness must never inherit proxy credentials or follow redirects.
    return build_opener(ProxyHandler({}), RefuseRedirect()).open(url, timeout=timeout)


def wait_for_encoder(child, readiness, stopping, poll):
    deadline = time.monotonic() + readiness['timeout']
    while not stopping:
        if child.poll() is not None:
            raise ValueError('local text encoder exited before readiness')
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise ValueError('local text encoder readiness timed out')
        try:
            with read_encoder_health(readiness['url'], min(1.0, remaining)) as response:
                body = response.read(4097)
            metadata = json.loads(body) if len(body) <= 4096 else {}
        except (OSError, ValueError):
            time.sleep(min(poll, max(0, deadline - time.monotonic())))
            continue
        if not isinstance(metadata, dict) or metadata.get('status') != 'ok' or any(
                metadata.get(key) != readiness[key] for key in ENCODER_IDENTITY):
            raise ValueError('local text encoder identity differs from the pinned model')
        return


def supervise(commands, grace=10.0, poll=0.2, readiness=None):
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
            if readiness and name == readiness['name']:
                try:
                    wait_for_encoder(children[-1][1], readiness, stopping, poll)
                except ValueError as error:
                    print(str(error), file=sys.stderr, flush=True)
                    break
            if stopping:
                break
        else:
            # All children started. A failed readiness barrier skips this loop.
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
    env = runtime_environment(os.environ)
    if env.get('QUIVR_API_KEYS') and any(env.get(name) for name in SCOPED_KEY_SECRETS):
        raise ValueError('QUIVR_API_KEYS cannot be combined with the individual Railway API key variables')
    api_keys = env.get('QUIVR_API_KEYS') or json.dumps(engine_api_keys(env))
    signing = engine_signing_environment(env)
    config = build_config(env)
    settings = encoder_settings(env) if mode == 'api' else None
    readiness = None
    if mode == 'api' and settings:
        if not pathlib.Path(ENCODER_PYTHON).is_file() or not pathlib.Path(ENCODER_MODEL + '/model.safetensors').is_file():
            raise ValueError('QUIVR_LOCAL_QUERY_ENCODER requires an image built with QUIVR_BUILD_LOCAL_QUERY_ENCODER=1')
        readiness = {'name': 'text-encoder', 'url': ENCODER_URL + '/health',
                     'timeout': settings['QUIVR_QUERY_ENCODER_STARTUP_SECONDS'], **ENCODER_IDENTITY}
    os.umask(0o077)
    prepare_hosted_manifest(env)
    path = pathlib.Path('/tmp/quivr-runtime.json')
    path.write_text(json.dumps(config))
    # The engine receives the scoped map. Provider keys and verification rings
    # go only to the sidecars that use them.
    core_env = {name: value for name, value in env.items()
                if name not in PLUGIN_SECRETS and name not in SCOPED_KEY_SECRETS
                and name not in ('QUIVR_PLUGIN_SIGNING_KEYS', 'QUIVR_PLUGIN_SIGNING_KEYS_FILE')}
    core_env.update(signing, QUIVR_API_KEYS=api_keys, QUIVR_CONFIG=str(path))
    # Only the API applies startup migrations; failures abort before serving.
    if mode == 'api' and env.get('QUIVR_SINGLE_MACHINE') != '1':
        subprocess.run(['quivr', 'migrate'], check=True, env=core_env)
    # The worker calls every plugin but the retrieval plugin, and the API the push
    # connector plugins it relays webhook deliveries to, the ingestion plugin it encodes
    # queries with and the retrieval plugin that ranks its searches, so each runs those
    # beside itself.
    sidecars = sidecar_commands(env, mode, signing=signing) if mode in ('api', 'worker') else []
    if sidecars:
        commands = sidecars + [('quivr ' + mode, ['quivr', mode], None, core_env)]
        sys.exit(supervise(commands, readiness=readiness) if readiness else supervise(commands))
    os.execvpe('quivr', ['quivr', mode], core_env)


if __name__ == '__main__':
    try:
        main()
    except KeyError as error:
        sys.exit('Missing runtime variable: ' + str(error))
    except ValueError as error:
        sys.exit(str(error))
