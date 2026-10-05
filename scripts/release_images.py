"""Discover the first-party images built for one alpha release.

The workflow uses this inventory instead of maintaining a second plugin list.
Version and changelog ownership stays with release-please.
"""
import argparse
import json
import pathlib
import re

import yaml

ROOT = pathlib.Path(__file__).resolve().parent.parent


def version(tag):
    if not re.fullmatch(r'v2\.0\.0-alpha\.(0|[1-9][0-9]*)', tag):
        raise ValueError(f'not a Quivr alpha release tag: {tag}')
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
            target = 'core-ingest' if folder.name == 'core-ingest' else 'go-plugin'
        elif (folder / 'pyproject.toml').exists():
            target = 'python-plugin'
        else:
            raise ValueError(f'no image runtime for {folder.name}')
        images.append({'plugin': folder.name, 'image': f'ghcr.io/{owner}/quivr-plugin-{plugin_id}',
                       'file': 'deploy/images/plugin.Dockerfile', 'target': target})
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
