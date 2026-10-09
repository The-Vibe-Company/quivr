#!/usr/bin/env python3
"""Deploy explicit services in the already configured dedicated demo project."""
import argparse
import json
import os
from pathlib import Path
from provision import cli, ROOT, SERVICES, load_services


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--project-id', required=True)
    parser.add_argument('--environment-id', required=True)
    parser.add_argument('--profile', default='small')
    parser.add_argument('--overrides', type=Path)
    parser.add_argument('services', nargs='+', choices=list(SERVICES))
    args = parser.parse_args()
    project = json.loads(cli('status', '--json'))
    if project['id'] != args.project_id or project['name'] != 'quivr-v2-demo':
        raise SystemExit('Link the dedicated demo project first.')
    if args.environment_id not in {edge['node']['id'] for edge in project.get('environments', {}).get('edges', [])}:
        raise SystemExit('Select an environment in the linked project.')
    cli('environment', args.environment_id)
    services = load_services(args.profile, args.overrides)
    directory = ROOT / '.scratch' / 'railway' / args.project_id
    ids = json.loads((directory / 'services.json').read_text())
    os.umask(0o077)
    for name in args.services:
        if 'image' in services[name]:
            query = ('mutation($serviceId:String!,$environmentId:String!,$input:ServiceInstanceUpdateInput!)'
                     '{serviceInstanceUpdate(serviceId:$serviceId,environmentId:$environmentId,input:$input)}')
            cli('api', query, '--variables', json.dumps({
                'serviceId': ids[name], 'environmentId': args.environment_id,
                'input': {'source': {'image': services[name]['image']}}}))
            output = cli('redeploy', '--service', ids[name], '--project', args.project_id,
                         '--environment', args.environment_id, '--from-source', '--yes', '--json')
        else:
            output = cli('up', '--service', ids[name], '--detach', '--json', '--message', 'THE-664 ' + name)
        (directory / ('deploy-' + name + '.json')).write_text(output)
        print('Deployment requested:', name, flush=True)


if __name__ == '__main__':
    main()
