"""Discover the first-party images built for one Quivr 2 release.

The workflow uses this inventory instead of maintaining a second plugin list.
Version and changelog ownership stays with release-please.
"""
import argparse
import json
import pathlib
import re

import yaml

ROOT = pathlib.Path(__file__).resolve().parent.parent


# Stable 2.x releases, and the 2.0.0 alphas that preceded them.
VERSION = r'2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-alpha\.(0|[1-9][0-9]*))?'


def version(tag):
    if not re.fullmatch('v' + VERSION, tag):
        raise ValueError(f'not a Quivr 2 release tag: {tag}')
    return tag[1:]


def inventory(root, repository):
    owner = repository.split('/')[0].lower()
    images = [{'plugin': '', 'image': f'ghcr.io/{owner}/quivr',
               'file': 'deploy/images/quivr.Dockerfile', 'target': ''}]
    seen = set()
    for manifest in sorted((root / 'plugins').glob('*/quivr-plugin.yaml')):
        folder = manifest.parent
        plugin_id = yaml.safe_load(manifest.read_text())['id']
        if not re.fullmatch(r'[a-z0-9]+(?:[._-][a-z0-9]+)*', plugin_id) or plugin_id in seen:
            raise ValueError(f'invalid or duplicate image id: {plugin_id}')
        seen.add(plugin_id)
        if (folder / 'go.mod').exists():
            target = folder.name if folder.name in {'core-ingest', 'hosted-embed'} else 'go-plugin'
        elif (folder / 'pyproject.toml').exists():
            target = 'python-plugin'
        else:
            raise ValueError(f'no image runtime for {folder.name}')
        images.append({'plugin': folder.name, 'plugin_id': plugin_id,
                       'image': f'ghcr.io/{owner}/quivr-plugin-{plugin_id}',
                       'file': 'deploy/images/plugin.Dockerfile', 'target': target})
    for component, dockerfile in (
            ('core', 'deploy/railway/core.Dockerfile'), ('web', 'deploy/railway/web.Dockerfile'),
            ('cpu-embeddings', 'deploy/cpu/embeddings.Dockerfile'),
            ('postgres', 'deploy/railway/postgres.Dockerfile'),
            ('seaweed', 'deploy/railway/seaweed.Dockerfile'), ('tei', 'deploy/railway/tei.Dockerfile')):
        images.append({'plugin': '', 'component': component, 'image': f'ghcr.io/{owner}/quivr-{component}',
                       'file': dockerfile, 'target': ''})
    return {'include': images}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--repository', required=True)
    args = parser.parse_args()
    release = version(args.tag)
    manifest_version = json.loads((ROOT / '.release-please-manifest.json').read_text())['.']
    if release != manifest_version or release != (ROOT / 'version.txt').read_text().strip():
        raise ValueError('release tag, release-please manifest and version.txt must agree')
    print(json.dumps({'version': release, 'matrix': inventory(ROOT, args.repository)}))


if __name__ == '__main__':
    main()
