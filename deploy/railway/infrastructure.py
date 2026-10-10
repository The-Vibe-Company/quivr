#!/usr/bin/env python3
"""Preview/apply managed settings, or report drift without changing a deployment."""
import argparse
import json
import re
from pathlib import Path
import shlex
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from deploy import infrastructure as infra

PROJECT = '''query($id:String!){project(id:$id){
  environments(first:100){edges{node{id}} pageInfo{hasNextPage}}
  services(first:100){edges{node{id name}} pageInfo{hasNextPage}}}}'''
INSTANCE = '''query($serviceId:String!,$environmentId:String!){
  serviceInstance(serviceId:$serviceId,environmentId:$environmentId){
    serviceId environmentId dockerfilePath source{image} latestDeployment{id status}
    activeDeployments{id status deploymentStopped instances{id status}}}
  serviceInstanceLimitOverride(serviceId:$serviceId,environmentId:$environmentId)}'''
LIMITS = '''mutation($input:ServiceInstanceLimitsUpdateInput!){serviceInstanceLimitsUpdate(input:$input)}'''
UPDATE = '''mutation($serviceId:String!,$environmentId:String!,$input:ServiceInstanceUpdateInput!){
  serviceInstanceUpdate(serviceId:$serviceId,environmentId:$environmentId,input:$input)}'''


def execute(args, stdin=None):
    result = subprocess.run(['railway', *args], input=stdin, capture_output=True, text=True, timeout=45)
    if result.returncode:
        # API and CLI diagnostics may contain credentials; do not echo or persist them.
        raise RuntimeError('Railway command failed; check authentication and selected deployment privately')
    return result.stdout


def limits(value):
    if not isinstance(value, dict):
        return {}
    # Railway's manifest limitOverride contains containers.{memoryBytes,cpu}.
    # Keep unfamiliar responses unknown instead of assuming GB or host limits.
    value = value.get('containers', {})
    if not isinstance(value, dict):
        return {}
    result = {}
    for field, key in (('memoryBytes', 'memory'), ('cpu', 'cpus')):
        number = value.get(field)
        if isinstance(number, (float, int)) and not isinstance(number, bool) and number > 0:
            result[key] = str(int(number)) if key == 'memory' else str(number)
    return result


class Railway:
    def __init__(self, project, environment, run=execute):
        self.project, self.environment, self.run = project, environment, run
        self.targets = {}
        self.instances = {}

    def api(self, query, variables, object_result=False):
        response = json.loads(self.run(['api', query, '--variables', json.dumps(variables)]))
        if response.get('errors') or not isinstance(response.get('data'), dict):
            raise RuntimeError('Railway API rejected the infrastructure request')
        if query.lstrip().startswith('mutation') and not object_result and any(value is not True for value in response['data'].values()):
            raise RuntimeError('Railway API did not acknowledge the infrastructure update')
        return response['data']

    def select(self, selectors, include_autoscaler=False):
        project = self.api(PROJECT, {'id': self.project})['project']
        environments = project['environments']
        services = project['services']
        if environments.get('pageInfo', {}).get('hasNextPage') or services.get('pageInfo', {}).get('hasNextPage'):
            raise RuntimeError('selected project exceeds the bounded selector lookup')
        if self.environment not in {edge['node']['id'] for edge in environments['edges']}:
            raise RuntimeError('environment is not in the selected project')
        selectors = dict(selectors)
        if include_autoscaler and any(edge['node']['name'] == 'autoscaler' for edge in services['edges']):
            selectors['autoscaler'] = 'autoscaler'
        for logical, selector in selectors.items():
            if logical not in infra.KEYS:
                raise RuntimeError('unsupported managed service')
            matches = [edge['node']['id'] for edge in services['edges']
                       if selector in (edge['node']['id'], edge['node']['name'])]
            if len(matches) != 1:
                raise RuntimeError('managed service selector is missing or ambiguous')
            self.targets[logical] = matches[0]
        if len(set(self.targets.values())) != len(self.targets):
            raise RuntimeError('each managed role must select a different service')

    def flags(self, name):
        return ['--project', self.project, '--environment', self.environment, '--service', self.targets[name]]

    def configured(self, name):
        response = self.api(INSTANCE, {'serviceId': self.targets[name], 'environmentId': self.environment})
        instance = response['serviceInstance']
        if instance['serviceId'] != self.targets[name] or instance['environmentId'] != self.environment:
            raise RuntimeError('Railway returned an unexpected service instance')
        self.instances[name] = instance
        variables = json.loads(self.run(['variable', 'list', '--json', *self.flags(name)]))
        if not isinstance(variables, dict):
            raise RuntimeError('Railway returned an unsupported variable response')
        return {'environment': {key: infra.safe_environment(key, variables.get(key)) for key in infra.KEYS[name]},
                'image': (instance.get('source') or {}).get('image'),
                'dockerfile': instance.get('dockerfilePath'),
                'limits': limits(response.get('serviceInstanceLimitOverride'))}

    def runtime(self, name):
        instance = self.instances[name]
        active = [d for d in instance.get('activeDeployments', [])
                  if d['status'] == 'SUCCESS' and not d.get('deploymentStopped')]
        latest = instance.get('latestDeployment') or {}
        # Do not inspect an old or arbitrary instance while a deployment is pending.
        if len(active) != 1 or latest.get('id') != active[0]['id'] or latest.get('status') != 'SUCCESS':
            return []
        running = [i for i in active[0].get('instances', []) if i['status'] == 'RUNNING']
        return [self.probe(name, target['id']) for target in running]

    def probe(self, name, instance_id):
        command = ['ssh', *self.flags(name), '--deployment-instance', instance_id,
                   'sh -c ' + shlex.quote(infra.runtime_script(name))]
        try:
            output = self.run(command)
        except (RuntimeError, subprocess.TimeoutExpired):
            return {}
        try:
            if name == 'postgres':
                lines = output.splitlines()
                result = json.loads(lines[0])
                result['volume_mb'] = int(lines[1]) if len(lines) >= 2 and lines[1].isdigit() else None
                result['limits'] = infra.observed_resources(lines[2:])
                return result
            values = dict(line.split('=', 1) for line in output.splitlines() if '=' in line)
            return {'environment': {key: infra.safe_environment(key, values.get(key) or None)
                                    for key in infra.KEYS[name]},
                    'limits': infra.observed_resources(output.splitlines())}
        except (ValueError, IndexError, TypeError):
            return {}

    def configured_rows(self, name, spec, state):
        rows = []
        for key, value in spec.get('environment', {}).items():
            infra.observation(rows, name, 'configured', key, value,
                              infra.safe_environment(key, state['environment'].get(key)))
        if name == 'postgres':
            expected = 'deploy/railway/postgres.Dockerfile'
            actual = state.get('dockerfile')
            infra.observation(rows, name, 'configured', 'dockerfile', expected,
                              actual if actual in (None, expected) else '<other>')
            infra.observation(rows, name, 'configured', 'image_source_cleared', True, not state.get('image'))
        if name == 'weaviate' and spec.get('image'):
            actual = state.get('image')
            if actual and not isinstance(actual, str):
                actual = '<invalid>'
            if actual and not re.fullmatch(r'[a-zA-Z0-9./:_-]+(@sha256:[0-9a-f]{64})?', actual):
                actual = '<invalid>'
            infra.observation(rows, name, 'configured', 'image', spec['image'], actual)
        for key, value in spec.get('deploy', {}).get('resources', {}).get('limits', {}).items():
            infra.observation(rows, name, 'configured', key, value, state['limits'].get(key))
        return rows

    def preview(self, services):
        rows = []
        for name in self.targets:
            rows.extend(self.configured_rows(name, services[name], self.configured(name)))
        return infra.report(rows)

    def apply(self, services):
        for name in self.targets:
            spec, state = services[name], self.configured(name)
            for key, value in spec.get('environment', {}).items():
                if state['environment'].get(key) != value:
                    self.run(['variable', 'set', key, '--stdin', '--skip-deploys', *self.flags(name)], stdin=value)
            desired = spec.get('deploy', {}).get('resources', {}).get('limits', {})
            if desired and any(not infra.equivalent(key, value, state['limits'].get(key))
                               for key, value in desired.items()):
                self.api(LIMITS, {'input': {'serviceId': self.targets[name], 'environmentId': self.environment,
                                          'memoryGB': int(desired['memory']) / 1e9, 'vCPUs': float(desired['cpus'])}})
            if name == 'postgres' and (state.get('dockerfile') != 'deploy/railway/postgres.Dockerfile'
                                       or state.get('image')):
                self.api(UPDATE, {'serviceId': self.targets[name], 'environmentId': self.environment,
                                  'input': {'dockerfilePath': 'deploy/railway/postgres.Dockerfile',
                                            'source': {'image': None}}})
            if name == 'weaviate' and state.get('image') != spec.get('image'):
                self.api(UPDATE, {'serviceId': self.targets[name], 'environmentId': self.environment,
                                  'input': {'source': {'image': spec['image']}}})

    def initialize_monitoring(self):
        if 'postgres' not in self.targets:
            raise RuntimeError('monitoring initialization requires the postgres service')
        query = (infra.ROOT / 'deploy/postgres/init.sql').read_text()
        script = ('psql -XAt -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" '
                  '-d "${POSTGRES_DB:-postgres}" -c ' + shlex.quote(query))
        self.run(['ssh', *self.flags('postgres'), 'sh -c ' + shlex.quote(script)])

    def check(self, services):
        rows = []
        for name in self.targets:
            spec = services[name]
            rows.extend(self.configured_rows(name, spec, self.configured(name)))
            runtimes = self.runtime(name) or [{}]
            for ordinal, runtime in enumerate(runtimes, 1):
                first_row = len(rows)
                if name == 'postgres':
                    expected = infra.postgres_expected(spec, runtime.get('volume_mb'))
                    for key, value in expected.items():
                        infra.observation(rows, name, 'effective_sql', key, value, runtime.get('settings', {}).get(key))
                    for key in ('pg_stat_statements', 'statistics_preloaded'):
                        infra.observation(rows, name, 'effective_sql', key, True, runtime.get(key))
                else:
                    for key, value in spec.get('environment', {}).items():
                        infra.observation(rows, name, 'startup_environment', key, value,
                                          infra.safe_environment(key, runtime.get('environment', {}).get(key)))
                infra.storage_observation(rows, name, spec, runtime)
                # Resource overrides describe configured intent, not running cgroups.
                for key, value in spec.get('deploy', {}).get('resources', {}).get('limits', {}).items():
                    infra.observation(rows, name, 'runtime_resources', key, value, runtime.get('limits', {}).get(key))
                if len(runtimes) > 1:
                    for row in rows[first_row:]:
                        row['replica'] = ordinal
        return infra.report(rows)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['preview', 'apply', 'check', 'initialize-monitoring'])
    parser.add_argument('--project-id', required=True)
    parser.add_argument('--environment-id', required=True)
    parser.add_argument('--profile', default='small')
    parser.add_argument('--overrides', type=Path)
    parser.add_argument('--service', action='append', default=[], metavar='ROLE=NAME_OR_ID')
    args = parser.parse_args()
    try:
        services = infra.resolve(args.profile, args.overrides)
        selectors = dict(item.split('=', 1) for item in args.service) if args.service else {
            name: name for name, spec in services.items() if name != 'autoscaler'
            and (spec.get('image') or spec.get('deploy'))}
        adapter = Railway(args.project_id, args.environment_id)
        adapter.select(selectors, include_autoscaler=not args.service)
        if args.command == 'apply':
            adapter.apply(services)
            print(json.dumps({'status': 'configured', 'next': 'Deploy changed services, initialize-monitoring, then check.'}))
            return 0
        if args.command == 'initialize-monitoring':
            adapter.initialize_monitoring()
            print(json.dumps({'status': 'monitoring_initialized'}))
            return 0
        result = adapter.check(services) if args.command == 'check' else adapter.preview(services)
        print(json.dumps(result, indent=2))
        return {'match': 0, 'drift': 1, 'unknown': 2}[result['status']]
    except (RuntimeError, ValueError, KeyError, TypeError, OSError, subprocess.TimeoutExpired):
        print('Infrastructure request failed; verify the declaration, selectors, authentication and private service access.', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
