"""Select the newest completed alpha image release and validate its digest list."""
import argparse
import json
import re
import sys


def latest_release(releases):
    completed = []
    for release in releases:
        tag = release['tag_name']
        match = re.fullmatch(r'v2\.0\.0-alpha\.(0|[1-9][0-9]*)', tag)
        if match and not release.get('draft') and any(
                asset['name'] == 'images.txt' for asset in release.get('assets', [])):
            completed.append((int(match[1]), tag))
    return max(completed)[1] if completed else ''


def image_matrix(text, repository):
    prefix = f'ghcr.io/{repository.split("/")[0].lower()}/'
    images = []
    for line in text.splitlines():
        if not re.fullmatch(re.escape(prefix) + r'quivr(?:-plugin-[a-z0-9._-]+)?@sha256:[0-9a-f]{64}', line):
            raise ValueError(f'invalid release image digest: {line!r}')
        images.append({'image': line, 'name': line.split('@')[0].rsplit('/', 1)[1]})
    if not images or len({item['name'] for item in images}) != len(images):
        raise ValueError('release image list must be nonempty with unique image names')
    return {'include': images}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['release', 'matrix'])
    parser.add_argument('--repository', default='The-Vibe-Company/quivr')
    args = parser.parse_args()
    if args.mode == 'release':
        print(latest_release(json.load(sys.stdin)))
    else:
        print(json.dumps(image_matrix(sys.stdin.read(), args.repository)))


if __name__ == '__main__':
    main()
