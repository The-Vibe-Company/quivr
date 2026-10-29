#!/usr/bin/env python3
"""Initialize one protected environment and render a standalone image-only stack."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent
COMPONENTS = ('core', 'web', 'cpu-embeddings', 'postgres', 'seaweed', 'tei')
SECRET_KEYS = ('QUIVR_DB_PASSWORD', 'QUIVR_API_KEY', 'QUIVR_OPERATOR_KEY',
               'QUIVR_CURSOR_KEY', 'QUIVR_CREDENTIAL_KEY', 'S3_ACCESS_KEY',
               'S3_SECRET_KEY', 'EMBED_API_KEY', 'DEMO_PASSWORD')
TEMPORAL = 'temporalio/temporal:1.8.3@sha256:cea463d98a8d6def4420f903ea5c3fcd0d85c8d10fbcc2770a50c12fff2eb26d'


def read_env(path):
    if path.is_symlink() or (path.exists() and stat.S_IMODE(path.stat().st_mode) & 0o077):
        raise ValueError('environment must be a regular private file (chmod 600)')
    values = {}
    if path.exists():
        for line in path.read_text().splitlines():
            if not line.strip() or line.startswith('#'):
                continue
            key, separator, value = line.partition('=')
            if not separator or not re.fullmatch('[A-Z][A-Z0-9_]*', key) or key in values:
                raise ValueError('invalid environment key or duplicate')
            if any(c in value for c in '\r\n\x00$\"\'`'):
                raise ValueError('environment values must be literal unquoted values')
            values[key] = value
    return values


def release_images(path):
    release = json.loads(path.read_text())
    if not re.fullmatch(r'2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-alpha\.(0|[1-9][0-9]*))?', release['version']) or not re.fullmatch('[0-9a-f]{40}', release['revision']):
        raise ValueError('invalid release identity')
    if any(not re.fullmatch('[0-9a-f]{40}', release['models'][name]) for name in ('gemma', 'e5')):
        raise ValueError('invalid model revision')
    images = {}
    for name in COMPONENTS:
        image = release['images'][name]
        reference = image['reference']
        if not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}', reference):
            raise ValueError('release images require immutable digests')
        if image['revision'] != release['revision'] or image['version'] != release['version']:
            raise ValueError('mixed release image identities')
        images[name] = reference
    return release, images


def infrastructure(profile, values=None):
    path = HERE / 'infrastructure.py'
    if not path.exists():
        path = HERE.parent / 'infrastructure.py'
    spec = importlib.util.spec_from_file_location('single_machine_infrastructure', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.resolve(profile, environ=values)


def stack(images, settings, values, initialized=False):
    """Compose is emitted as JSON (a YAML subset), with literal release digests."""
    model = values['QUIVR_EMBEDDING']
    if model not in ('gemma', 'e5'):
        raise ValueError('QUIVR_EMBEDDING must be gemma or e5')
    project = values['COMPOSE_PROJECT_NAME']
    if not re.fullmatch('[a-z0-9][a-z0-9_-]{0,62}', project):
        raise ValueError('invalid Compose project name')
    log = {'driver': 'json-file', 'options': {'max-size': '10m', 'max-file': '3'}}
    for name in ('postgres', 'weaviate'):
        memory = values.get('QUIVR_' + name.upper() + '_MEMORY_MB')
        if memory:
            if not memory.isdecimal() or int(memory) < 128:
                raise ValueError('service memory budget must be at least 128 MiB')
            settings[name]['deploy']['resources']['limits']['memory'] = str(int(memory) * 1048576)
            if name == 'weaviate':
                settings[name]['environment']['GOMEMLIMIT'] = str(int(int(memory) * .85)) + 'MiB'
    def health(command, retries=60):
        return {'test': command, 'interval': '2s', 'timeout': '5s', 'retries': retries, 'start_period': '10s'}
    healthy = {name: {'condition': 'service_healthy'} for name in ('postgres', 'temporal', 'seaweed', 'weaviate', 'embeddings')}
    core_env = {
        'QUIVR_SINGLE_MACHINE': '1', 'QUIVR_ORGANIZATION': '${QUIVR_ORGANIZATION:-quivr}',
        'DATABASE_URL': 'postgres://quivr:${QUIVR_DB_PASSWORD:?}@postgres:5432/quivr?sslmode=disable',
        'QUIVR_API_KEY': '${QUIVR_API_KEY:?}', 'QUIVR_OPERATOR_KEY': '${QUIVR_OPERATOR_KEY:?}',
        'QUIVR_CURSOR_KEY': '${QUIVR_CURSOR_KEY:?}', 'QUIVR_CREDENTIAL_KEY': '${QUIVR_CREDENTIAL_KEY:?}',
        'TEMPORAL_ADDRESS': 'temporal:7233', 'WEAVIATE_URL': 'http://weaviate:8080',
        'TEI_URL': 'http://embeddings:80' if model == 'e5' else 'http://embeddings:8080',
        'S3_ENDPOINT': 'http://seaweed:8333', 'S3_ACCESS_KEY': '${S3_ACCESS_KEY:?}',
        'S3_SECRET_KEY': '${S3_SECRET_KEY:?}', 'QUIVR_DEMO_PLUGINS': '1',
        'QUIVR_DEMO_CONNECTORS': '1', 'QUIVR_DEMO_ADMIN': '1',
        'QUIVR_WORKER_LIVE_SLOTS': '${QUIVR_WORKER_LIVE_SLOTS:-1}',
        'QUIVR_WORKER_BULK_SLOTS': '${QUIVR_WORKER_BULK_SLOTS:-1}'}
    if model == 'gemma':
        core_env.update(QUIVR_DEMO_EMBEDDING='gemma', QUIVR_DEMO_CORE_INGEST='0',
                        EMBED_URL='http://embeddings:8080', EMBED_API_KEY='${EMBED_API_KEY:?}')
    services = {
        'postgres': {'image': images['postgres'], 'environment': {
            **settings['postgres']['environment'], 'POSTGRES_USER': 'quivr', 'POSTGRES_DB': 'quivr',
            'POSTGRES_PASSWORD': '${QUIVR_DB_PASSWORD:?}'}, 'deploy': settings['postgres']['deploy'],
            'volumes': ['postgres-data:/var/lib/postgresql/data'],
            'healthcheck': health(['CMD', 'pg_isready', '-h', '127.0.0.1', '-U', 'quivr', '-d', 'quivr'])},
        'temporal': {'image': TEMPORAL,
            'command': ['server', 'start-dev', '--ip', '0.0.0.0', '--headless', '--db-filename', '/home/temporal/temporal.db'],
            'volumes': ['temporal-data:/home/temporal'],
            'healthcheck': health(['CMD', 'temporal', 'operator', 'cluster', 'health', '--address', '127.0.0.1:7233'])},
        'seaweed': {'image': images['seaweed'], 'environment': {
            'S3_ACCESS_KEY': '${S3_ACCESS_KEY:?}', 'S3_SECRET_KEY': '${S3_SECRET_KEY:?}'},
            'volumes': ['seaweed-data:/data'], 'healthcheck': health([
                'CMD', 'wget', '-q', '-O', '/dev/null', 'http://127.0.0.1:9333/cluster/status'])},
        'weaviate': {'image': settings['weaviate']['image'], 'hostname': 'quivr-index',
            'environment': settings['weaviate']['environment'], 'deploy': settings['weaviate']['deploy'],
            'volumes': ['weaviate-data:/var/lib/weaviate'], 'healthcheck': health([
                'CMD', 'wget', '-q', '-O', '/dev/null', 'http://127.0.0.1:8080/v1/.well-known/ready'], 180)},
        'embeddings': {'image': images['cpu-embeddings'] if model == 'gemma' else images['tei'],
            'environment': {'HF_HUB_OFFLINE': '1', 'TRANSFORMERS_OFFLINE': '1',
                'HF_HUB_DISABLE_TELEMETRY': '1', 'EMBED_API_KEY': '${EMBED_API_KEY:?}',
                'QUIVR_EMBEDDING_THREADS': '${QUIVR_EMBEDDING_THREADS:-4}'},
            'volumes': ['model-cache:/models:ro'], 'deploy': {'resources': {'limits': {
                'cpus': '${QUIVR_EMBEDDING_CPUS:-4}', 'memory': '${QUIVR_EMBEDDING_MEMORY:-4g}'}}}},
        'migrate': {'image': images['core'], 'environment': {**core_env, 'QUIVR_ROLE': 'migrate'},
            'depends_on': healthy, 'restart': 'no', 'tmpfs': ['/tmp:mode=1777,size=128m']},
        'api': {'image': images['core'], 'environment': {**core_env, 'QUIVR_ROLE': 'api'},
            'depends_on': {'migrate': {'condition': 'service_completed_successfully'}},
            'ports': ['127.0.0.1:${QUIVR_API_PORT:-8080}:8080'], 'tmpfs': ['/tmp:mode=1777,size=128m'],
            'healthcheck': health(['CMD', 'python', '-c', "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8081/readyz',timeout=3)"])},
        'worker': {'image': images['core'], 'environment': {**core_env, 'QUIVR_ROLE': 'worker'},
            'depends_on': {'migrate': {'condition': 'service_completed_successfully'}},
            'tmpfs': ['/tmp:mode=1777,size=128m'], 'stop_grace_period': '60s'},
        'demo': {'image': images['web'], 'profiles': ['demo'],
            'environment': {'QUIVR_API_URL': 'http://api:8080', 'QUIVR_API_KEY': '${QUIVR_API_KEY:?}',
                'DEMO_PASSWORD': '${DEMO_PASSWORD:?}', 'DEMO_SECURE_COOKIE': 'false', 'HOST': '0.0.0.0', 'PORT': '3000'},
            'ports': ['127.0.0.1:${QUIVR_DEMO_PORT:-3000}:3000'],
            'depends_on': {'api': {'condition': 'service_healthy'}}}}
    if model == 'e5':
        services['embeddings']['entrypoint'] = ['sh', '/app/tei-entrypoint.sh']
        services['embeddings']['command'] = []
        services['embeddings']['healthcheck'] = health(['CMD', 'curl', '-fsS', 'http://localhost/health'])
    for name, service in services.items():
        service.update(logging=log, networks=['stack'])
        if name != 'migrate':
            service.setdefault('restart', 'unless-stopped')
    # Docker does not publish host ports for an exclusively internal bridge.
    # Only API/demo join access; dependencies and inference stay on stack.
    for name in ('api', 'demo'):
        services[name]['networks'].append('access')
    return {'name': project, 'x-quivr-model': model, 'x-quivr-model-revision': values.get('QUIVR_MODEL_REVISION', ''),
            'x-quivr-initialized': initialized, 'services': services,
            'networks': {'stack': {'internal': values.get('QUIVR_OFFLINE', '0') == '1'}, 'access': {}},
            'volumes': {name: {'name': project + '-' + name} for name in
                ('postgres-data', 'temporal-data', 'seaweed-data', 'weaviate-data', 'model-cache')}}


def write_private(path, content):
    if path.is_symlink():
        raise ValueError('refuse symlink output')
    fd, name = tempfile.mkstemp(prefix='.' + path.name + '.', dir=path.parent)
    temporary = Path(name)
    try:
        with os.fdopen(fd, 'w') as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        temporary.chmod(0o600)
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def initialize(directory, release_path, project, embedding, profile, secret_files):
    release, images = release_images(release_path)
    directory.mkdir(parents=True, exist_ok=True)
    env_path = directory / '.env'
    values = read_env(env_path)
    values.setdefault('COMPOSE_PROJECT_NAME', project)
    values.setdefault('QUIVR_EMBEDDING', embedding)
    selected_revision = release['models'].get(values['QUIVR_EMBEDDING'])
    if selected_revision is None:
        raise ValueError('unsupported model')
    values.setdefault('QUIVR_MODEL_REVISION', selected_revision)
    if values['QUIVR_MODEL_REVISION'] != selected_revision:
        raise ValueError('installation model revision cannot change; use a fresh installation')
    values.setdefault('QUIVR_INFRASTRUCTURE_PROFILE', profile)
    values.setdefault('QUIVR_ORGANIZATION', 'quivr')
    values.setdefault('QUIVR_OFFLINE', '0')
    for key in SECRET_KEYS:
        if key not in values:
            source = secret_files.get(key)
            if source:
                if source.is_symlink() or not source.is_file() or source.stat().st_mode & 0o077:
                    raise ValueError('secret input must be a regular private file')
                value = source.read_text().strip()
                if not re.fullmatch('[a-f0-9]{64}', value):
                    raise ValueError('secret files must contain 64 hexadecimal characters')
                values[key] = value
            else:
                values[key] = secrets.token_hex(32)
        if not re.fullmatch('[a-f0-9]{64}', values[key]):
            raise ValueError('installation secrets must contain 64 hexadecimal characters')
    if values['QUIVR_API_KEY'] == values['QUIVR_OPERATOR_KEY']:
        raise ValueError('API and operator keys must differ')
    path = directory / 'compose.yaml'
    if path.is_symlink():
        raise ValueError('refuse symlink Compose output')
    if path.exists():
        previous = json.loads(path.read_text())
        if previous.get('x-quivr-initialized') and (previous['x-quivr-model'] != values['QUIVR_EMBEDDING'] or previous['name'] != values['COMPOSE_PROJECT_NAME']):
            raise ValueError('installation model/project cannot change; use a fresh installation')
    model = stack(images, infrastructure(values['QUIVR_INFRASTRUCTURE_PROFILE'], values), values, initialized=True)
    write_private(env_path, ''.join(f'{key}={value}\n' for key, value in values.items()))
    write_private(path, json.dumps(model, indent=2) + '\n')
    write_private(directory / 'release.json', json.dumps(release, indent=2) + '\n')
    return values, images


def docker(args):
    result = subprocess.run(['docker', *args], capture_output=True, text=True, timeout=900)
    if result.returncode:
        raise RuntimeError('Docker operation failed; inspect Docker privately (no configuration was printed)')
    return result.stdout


def prepare_cache(directory, mirror, source):
    values = read_env(directory / '.env')
    _, images = release_images(directory / 'release.json')
    volume = values['COMPOSE_PROJECT_NAME'] + '-model-cache'
    inspected = docker(['volume', 'create', '--label', 'quivr.model=' + values['QUIVR_EMBEDDING'], volume]).strip()
    if inspected != volume:
        raise RuntimeError('unexpected cache volume')
    labels = json.loads(docker(['volume', 'inspect', volume]))[0].get('Labels') or {}
    if labels.get('quivr.model') != values['QUIVR_EMBEDDING']:
        raise ValueError('cache model differs; use a fresh installation')
    command = ['run', '--rm', '--user', '0:0', '-v', volume + ':/models', '--entrypoint',
               '/opt/query-encoder/venv/bin/python']
    if source:
        command += ['--mount', 'type=bind,src=' + str(source.resolve()) + ',dst=/source,readonly']
    command += [images['cpu-embeddings'], '/app/deploy/single-machine/cache.py',
                '--output', '/models', '--model', values['QUIVR_EMBEDDING']]
    if mirror:
        command += ['--mirror', mirror]
    if source:
        command += ['--source', '/source']
    docker(command)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['init', 'prepare', 'upgrade'])
    parser.add_argument('--directory', type=Path, default=Path('.'))
    parser.add_argument('--release', type=Path, default=HERE / 'release.json')
    parser.add_argument('--project', default='quivr')
    parser.add_argument('--embedding', choices=['gemma', 'e5'], default='gemma')
    parser.add_argument('--profile', choices=['small', 'large'], default='small')
    parser.add_argument('--secret-file', action='append', default=[], metavar='KEY=PATH')
    parser.add_argument('--mirror')
    parser.add_argument('--source', type=Path)
    args = parser.parse_args()
    try:
        if args.command == 'prepare':
            prepare_cache(args.directory, args.mirror, args.source)
        else:
            files = {}
            for value in args.secret_file:
                key, _, path = value.partition('=')
                if key not in SECRET_KEYS or not path:
                    raise ValueError('unsupported secret file key')
                files[key] = Path(path)
            initialize(args.directory, args.release, args.project, args.embedding, args.profile, files)
            if args.command == 'upgrade':
                docker(['compose', '--project-directory', str(args.directory), 'stop', 'api', 'worker', 'demo'])
                docker(['compose', '--project-directory', str(args.directory), 'rm', '-f', 'migrate'])
                docker(['compose', '--project-directory', str(args.directory), 'up', '-d'])
        print('Ready: one Compose file and protected .env; model cache preparation is a separate install step.')
    except (ValueError, RuntimeError, KeyError, OSError) as error:
        # Do not echo exception values: paths and provider responses may contain secrets.
        print('Setup refused: ' + (str(error) if isinstance(error, (ValueError, RuntimeError)) else 'invalid installation input'), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
