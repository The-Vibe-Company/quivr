#!/usr/bin/env python3
"""Deploy explicit services in the already configured dedicated demo project."""
import argparse
import json
import os
from pathlib import Path
from provision import cli, ROOT, SERVICES


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--project-id', required=True)
    parser.add_argument('services', nargs='+', choices=list(SERVICES))
    args = parser.parse_args()
    project = json.loads(cli('status', '--json'))
    if project['id'] != args.project_id or project['name'] != 'quivr-v2-demo':
        raise SystemExit('Link the dedicated demo project first.')
    directory = ROOT / '.scratch' / 'railway' / args.project_id
    ids = json.loads((directory / 'services.json').read_text())
    os.umask(0o077)
    for name in args.services:
        if 'image' in SERVICES[name]:
            output = cli('service', 'source', 'connect', '--service', ids[name], '--image', SERVICES[name]['image'], '--json')
        else:
            output = cli('up', '--service', ids[name], '--detach', '--json', '--message', 'THE-664 ' + name)
        (directory / ('deploy-' + name + '.json')).write_text(output)
        print('Deployment requested:', name, flush=True)


if __name__ == '__main__':
    main()
