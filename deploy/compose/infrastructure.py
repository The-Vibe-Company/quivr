#!/usr/bin/env python3
"""Apply a declared Compose profile or report live drift without changing settings."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from deploy import infrastructure as infra
from deploy.reset_support import vector_endpoint


def execute(args, environ=None):
    result = subprocess.run(['docker', *args], capture_output=True, text=True, timeout=180, env=environ)
    if result.returncode:
        raise RuntimeError('Docker command failed; check the selected project privately')
    return result.stdout


class Compose:
    def __init__(self, project, run=execute):
        if not re.fullmatch(r'[a-z0-9][a-z0-9_-]*', project):
            raise ValueError('invalid Compose project name')
        self.project, self.run = project, run
        self.containers = {}

    def configured(self, name):
        identifiers = self.run(['ps', '-q', '--filter', 'label=com.docker.compose.project=' + self.project,
                                '--filter', 'label=com.docker.compose.service=' + name]).split()
        if len(identifiers) != 1:
            return {}
        state = json.loads(self.run(['inspect', identifiers[0]]))[0]
        labels = state['Config'].get('Labels', {})
        if labels.get('com.docker.compose.project') != self.project or labels.get('com.docker.compose.service') != name:
            raise RuntimeError('Docker returned an unexpected project or service')
        if not state.get('State', {}).get('Running'):
            return {}
        self.containers[name] = identifiers[0]
        variables = dict(item.split('=', 1) for item in state['Config'].get('Env', []) if '=' in item)
        host = state['HostConfig']
        limits = {}
        if host.get('Memory', 0) > 0:
            limits['memory'] = str(host['Memory'])
        if host.get('NanoCpus', 0) > 0:
            limits['cpus'] = str(host['NanoCpus'] / 1e9)
        image = state['Config'].get('Image')
        if image and not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}', image):
            image = '<unverified>'
        return {'environment': {key: infra.safe_environment(key, variables.get(key) or None)
                                for key in infra.KEYS.get(name, ())}, 'image': image, 'limits': limits}

    def runtime(self, name):
        if name not in self.containers:
            return {}
        try:
            output = self.run(['exec', self.containers[name], 'sh', '-c', infra.runtime_script(name)])
            if name == 'postgres':
                lines = output.splitlines()
                result = json.loads(lines[0])
                result['volume_mb'] = int(lines[1]) if len(lines) > 1 and lines[1].isdigit() else None
                result['limits'] = infra.observed_resources(lines[2:])
                return result
            values = dict(line.split('=', 1) for line in output.splitlines() if '=' in line)
            return {'environment': {key: infra.safe_environment(key, values.get(key) or None)
                                    for key in infra.KEYS[name]},
                    'limits': infra.observed_resources(output.splitlines())}
        except (RuntimeError, subprocess.TimeoutExpired, ValueError, IndexError):
            return {}

    def index_is_local(self):
        path = infra.ROOT / '.scratch' / self.project / 'config.json'
        if not path.exists():
            return None
        configured = json.loads(path.read_text()).get('weaviate_url')
        if not self.configured('weaviate'):
            return False
        details = json.loads(self.run(['inspect', self.containers['weaviate']]))[0]
        bindings = details.get('NetworkSettings', {}).get('Ports', {}).get('8080/tcp') or []
        local = {vector_endpoint('http://' + item['HostIp'] + ':' + item['HostPort']) for item in bindings
                 if item.get('HostIp') == '127.0.0.1' and item.get('HostPort', '').isdigit()}
        return vector_endpoint(configured) in local - {None}

    def check(self, services, runtime=True, external_host=None):
        rows = []
        for name in ('postgres', 'temporal', 'weaviate'):
            spec = services[name]
            if name == 'weaviate' and external_host is not None:
                rows.extend(infra.external_vector_report(spec, external_host)['observations'])
                continue
            state = self.configured(name)
            infra.observation(rows, name, 'configured', 'image', spec['image'], state.get('image'))
            for key, value in spec['environment'].items():
                # Empty/defaulted PostgreSQL settings are reported through SQL below.
                actual = state.get('environment', {}).get(key)
                if name != 'postgres' or actual is not None or not runtime:
                    infra.observation(rows, name, 'configured', key, value, actual)
            for key, value in spec['deploy']['resources']['limits'].items():
                infra.observation(rows, name, 'configured', key, value, state.get('limits', {}).get(key))
            if not runtime:
                continue
            observed = self.runtime(name)
            if name == 'postgres':
                for key, value in infra.postgres_expected(spec, observed.get('volume_mb')).items():
                    infra.observation(rows, name, 'effective_sql', key, value, observed.get('settings', {}).get(key))
                for key in ('pg_stat_statements', 'statistics_preloaded'):
                    infra.observation(rows, name, 'effective_sql', key, True, observed.get(key))
            else:
                for key, value in spec['environment'].items():
                    infra.observation(rows, name, 'startup_environment', key, value,
                                      observed.get('environment', {}).get(key))
            infra.storage_observation(rows, name, spec, observed)
            for key, value in spec['deploy']['resources']['limits'].items():
                infra.observation(rows, name, 'runtime_resources', key, value, observed.get('limits', {}).get(key))
        return infra.report(rows)

    def apply(self, overlay, external=False):
        # Reuse the selected local stack's credentials; never create or rotate them.
        directory = infra.ROOT / '.scratch' / self.project
        environment = dict(os.environ)
        if not environment.get('QUIVR_DB_PASSWORD'):
            statefile = directory / 'state.json'
            if not statefile.exists():
                raise RuntimeError('selected local project state is missing')
            password = json.loads(statefile.read_text()).get('password')
            if not isinstance(password, str) or not password:
                raise RuntimeError('selected local project state has no database password')
            environment['QUIVR_DB_PASSWORD'] = password
        environment.setdefault('QUIVR_LOCAL_ROOT', str(directory))
        environment.setdefault('QUIVR_MODEL_ROOT', str(infra.ROOT / '.scratch/e5-model'))
        # Only infrastructure services; never scale workers or alter unrelated services.
        self.run(['compose', '--project-name', self.project, '-f', str(infra.ROOT / 'deploy/compose/compose.yaml'),
                  '-f', str(overlay), 'up', '-d', 'postgres', 'temporal', *([] if external else ['weaviate'])], environ=environment)

    def initialize_monitoring(self):
        self.configured('postgres')
        if 'postgres' not in self.containers:
            raise RuntimeError('monitoring initialization requires one running PostgreSQL container')
        import shlex
        query = (infra.ROOT / 'deploy/postgres/init.sql').read_text()
        self.run(['exec', self.containers['postgres'], 'sh', '-c',
                  'psql -XAt -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" '
                  '-d "${POSTGRES_DB:-postgres}" -c ' + shlex.quote(query)])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['preview', 'apply', 'check', 'initialize-monitoring'])
    parser.add_argument('--project', required=True)
    parser.add_argument('--profile', default='small')
    parser.add_argument('--overrides', type=Path)
    parser.add_argument('--vector-host', nargs='?', const='', metavar='SSH_TARGET',
                        help='external index: observe through SSH, or unknown if no target')
    args = parser.parse_args()
    try:
        services = infra.resolve(args.profile, args.overrides, os.environ)
        adapter = Compose(args.project)
        location = adapter.index_is_local() if args.vector_host is None else False
        if args.command == 'apply':
            overlay = infra.ROOT / '.scratch' / args.project / 'infrastructure.json'
            infra.write_overlay(overlay, args.profile, args.overrides, os.environ)
            adapter.apply(overlay, external=args.vector_host is not None or location is False)
            print(json.dumps({'status': 'applied', 'next': 'initialize-monitoring, then check'}))
            return 0
        if args.command == 'initialize-monitoring':
            adapter.initialize_monitoring()
            print(json.dumps({'status': 'monitoring_initialized'}))
            return 0
        external = args.vector_host if args.vector_host is not None else (None if location is True else '')
        result = adapter.check(services, runtime=args.command == 'check', external_host=external)
        print(json.dumps(result, indent=2))
        return {'match': 0, 'drift': 1, 'unknown': 2}[result['status']]
    except (RuntimeError, ValueError, OSError, KeyError, TypeError, IndexError, subprocess.TimeoutExpired):
        print('Compose infrastructure request failed; check the declaration and selected project privately.', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
