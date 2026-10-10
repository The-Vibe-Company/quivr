#!/usr/bin/env python3
"""Configure the dedicated Railway demo. Does not expose domains or deploy code."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from deploy import infrastructure

ROOT = Path(__file__).resolve().parents[2]


def load_services(profile='small', overrides=None):
    services = json.loads((Path(__file__).parent / 'services.json').read_text())
    for name, shared in infrastructure.resolve(profile, overrides).items():
        if name not in services:
            continue
        services[name].setdefault('variables', {}).update(shared['environment'])
        limits = shared.get('deploy', {}).get('resources', {}).get('limits')
        if limits:
            services[name]['limits'] = limits
        if name == 'weaviate':
            services[name]['image'] = shared['image']
    return services


SERVICES = load_services()


def deployment_config(spec, new):
    config = {'restartPolicyType': 'ON_FAILURE', 'restartPolicyMaxRetries': 10}
    # Existing replicas belong to the autoscaler/operator, including regional counts.
    if new and 'replicas' in spec:
        config['numReplicas'] = spec['replicas']
    if 'dockerfile' in spec:
        config['dockerfilePath'] = f"deploy/railway/{spec['dockerfile']}.Dockerfile"
        if spec['dockerfile'] in ('postgres', 'index-warmup'):
            config['source'] = {'image': None}
    if 'healthcheck' in spec:
        config.update(healthcheckPath=spec['healthcheck'], healthcheckTimeout=180)
    return config


def cli(*args, stdin=None):
    result = subprocess.run(['railway', *args], cwd=ROOT, input=stdin, text=True, capture_output=True)
    if result.returncode:
        # CLI diagnostics can contain credentials; never log or echo them.
        raise RuntimeError('Railway command failed: ' + ' '.join(args[:2]))
    if args[0] == 'api':
        response = json.loads(result.stdout)
        if response.get('errors') or not isinstance(response.get('data'), dict):
            raise RuntimeError('Railway API rejected the provisioning request')
        if args[1].startswith('mutation') and any(value is not True for value in response['data'].values()):
            raise RuntimeError('Railway API did not acknowledge the provisioning update')
    return result.stdout


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--project-id', required=True)
    parser.add_argument('--environment-id', required=True)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--profile', default='small')
    parser.add_argument('--overrides', type=Path)
    args = parser.parse_args()
    services = load_services(args.profile, args.overrides)
    project = json.loads(cli('status', '--json'))
    if project['id'] != args.project_id or project['name'] != 'quivr-v2-demo':
        raise SystemExit('Link the dedicated quivr-v2-demo project first.')
    environments = project.get('environments', {}).get('edges', [])
    if args.environment_id not in {edge['node']['id'] for edge in environments}:
        raise SystemExit('Select an environment in the linked project.')
    existing = {s['name']: s for s in json.loads(cli('service', 'list', '--json'))}
    print('Project:', project['id'])
    for name, spec in services.items():
        print(name, 'persistent ' + spec['volume'] if 'volume' in spec else 'stateless', spec.get('dockerfile', 'pinned image'))
    if not args.apply:
        return
    cli('environment', args.environment_id)
    existing = {s['name']: s for s in json.loads(cli('service', 'list', '--json'))}
    os.umask(0o077)
    directory = ROOT / '.scratch' / 'railway' / args.project_id
    directory.mkdir(parents=True, exist_ok=True)
    secretfile = directory / 'secrets.json'
    if secretfile.exists():
        values = json.loads(secretfile.read_text())
        # credential_key is optional (THE-691) but enables Deposited Credentials; add it
        # once to existing deployments without rotating any other secret.
        if 'credential_key' not in values:
            values['credential_key'] = secrets.token_hex(32)
            secretfile.write_text(json.dumps(values))
    else:
        if any(s.get('source') for s in existing.values()):
            raise SystemExit('Existing deployment without local secrets: restore credentials instead of rotating them.')
        values = {k: secrets.token_hex(32) for k in ['database_password', 'api_key', 'cursor_key', 'credential_key', 's3_access', 's3_secret', 'demo_password']}
        secretfile.write_text(json.dumps(values))
    common = {
        'QUIVR_API_KEY': values['api_key'], 'QUIVR_CURSOR_KEY': values['cursor_key'],
        'QUIVR_CREDENTIAL_KEY': values['credential_key'],
        'DATABASE_URL': f"postgres://quivr:{values['database_password']}@postgres.railway.internal:5432/quivr?sslmode=disable",
        'S3_ACCESS_KEY': values['s3_access'], 'S3_SECRET_KEY': values['s3_secret'],
        'S3_ENDPOINT': 'http://seaweed.railway.internal:8333',
        'TEMPORAL_ADDRESS': 'temporal.railway.internal:7233',
        'WEAVIATE_URL': 'http://weaviate.railway.internal:8080', 'TEI_URL': 'http://tei.railway.internal:80',
    }
    volumes = json.loads(cli('volume', 'list', '--json')).get('volumes', [])
    state = {}
    for name, spec in services.items():
        service = existing.get(name) or json.loads(cli('add', '--service', name, '--json'))
        sid = service['id']
        state[name] = sid
        cli('service', 'link', sid)
        if 'volume' in spec and not any(v.get('serviceName') == name for v in volumes):
            cli('volume', 'add', '--mount-path', spec['volume'], '--json')
        variables = dict(spec.get('variables', {}))
        if name == 'postgres':
            variables['POSTGRES_PASSWORD'] = values['database_password']
        elif name == 'seaweed':
            variables.update({k: common[k] for k in ['S3_ACCESS_KEY', 'S3_SECRET_KEY']})
        elif name in ('api', 'worker', 'worker-bulk'):
            variables.update(common)
        elif name == 'web':
            variables.update(QUIVR_API_KEY=values['api_key'], DEMO_PASSWORD=values['demo_password'])
        for key, value in variables.items():
            cli('variable', 'set', key, '--stdin', '--skip-deploys', '--service', sid, stdin=value)
        config = deployment_config(spec, new=name not in existing)
        query = 'mutation($id:String!,$environment:String!,$input:ServiceInstanceUpdateInput!){serviceInstanceUpdate(serviceId:$id,environmentId:$environment,input:$input)}'
        cli('api', query, '--variables', json.dumps({'id': sid, 'environment': args.environment_id, 'input': config}))
        if 'limits' in spec:
            limits = spec['limits']
            query = 'mutation($input:ServiceInstanceLimitsUpdateInput!){serviceInstanceLimitsUpdate(input:$input)}'
            cli('api', query, '--variables', json.dumps({'input': {
                'serviceId': sid, 'environmentId': args.environment_id,
                'memoryGB': int(limits['memory']) / 1e9, 'vCPUs': float(limits['cpus'])}}))
        print('Configured', name, sid, flush=True)
    (directory / 'services.json').write_text(json.dumps(state, indent=2))
    print('Credentials retained only in', secretfile, '(0600). Deploy dependencies, then API, workers and web.')


if __name__ == '__main__':
    main()
