#!/usr/bin/env python3
"""Configure the dedicated Railway demo. Does not expose domains or deploy code."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess

ROOT = Path(__file__).resolve().parents[2]
SERVICES = json.loads((Path(__file__).parent / 'services.json').read_text())


def cli(*args, stdin=None):
    result = subprocess.run(['railway', *args], cwd=ROOT, input=stdin, text=True, capture_output=True)
    if result.returncode:
        # CLI diagnostics can contain values; retain them locally, never echo credentials.
        path = ROOT / '.scratch' / 'railway-command-error.log'
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(result.stderr + '\n' + result.stdout)
        path.chmod(0o600)
        raise RuntimeError('Railway command failed: ' + ' '.join(args[:2]))
    return result.stdout


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--project-id', required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    project = json.loads(cli('status', '--json'))
    if project['id'] != args.project_id or project['name'] != 'quivr-v2-demo':
        raise SystemExit('Link the dedicated quivr-v2-demo project first.')
    existing = {s['name']: s for s in json.loads(cli('service', 'list', '--json'))}
    print('Project:', project['id'])
    for name, spec in SERVICES.items():
        print(name, 'persistent ' + spec['volume'] if 'volume' in spec else 'stateless', spec.get('dockerfile', 'pinned image'))
    if not args.apply:
        return
    os.umask(0o077)
    directory = ROOT / '.scratch' / 'railway' / args.project_id
    directory.mkdir(parents=True, exist_ok=True)
    secretfile = directory / 'secrets.json'
    if secretfile.exists():
        values = json.loads(secretfile.read_text())
        # credential_key became required with Connector Instances (THE-668); add it once
        # to existing deployments without rotating any other secret.
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
    for name, spec in SERVICES.items():
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
        elif name in ('api', 'worker'):
            variables.update(common)
        elif name == 'web':
            variables.update(QUIVR_API_KEY=values['api_key'], DEMO_PASSWORD=values['demo_password'])
        for key, value in variables.items():
            cli('variable', 'set', key, '--stdin', '--skip-deploys', '--service', sid, stdin=value)
        config = {'restartPolicyType': 'ON_FAILURE', 'restartPolicyMaxRetries': 10}
        if 'dockerfile' in spec:
            config.update(dockerfilePath=f"deploy/railway/{spec['dockerfile']}.Dockerfile")
        if 'healthcheck' in spec:
            config.update(healthcheckPath=spec['healthcheck'], healthcheckTimeout=180)
        query = 'mutation($id:String!,$input:ServiceInstanceUpdateInput!){serviceInstanceUpdate(serviceId:$id,input:$input)}'
        cli('api', query, '--variables', json.dumps({'id': sid, 'input': config}))
        print('Configured', name, sid, flush=True)
    (directory / 'services.json').write_text(json.dumps(state, indent=2))
    print('Credentials retained only in', secretfile, '(0600). Deploy dependencies, then API, worker and web.')


if __name__ == '__main__':
    main()
