"""Select the newest completed image release and validate its digest list."""
import argparse
import json
import re
import sys


def release_key(tag):
    """Sort key for a Quivr 2 release tag, or None: a stable release ranks above its alphas."""
    match = re.fullmatch(r'v2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-alpha\.(0|[1-9][0-9]*))?', tag)
    if not match:
        return None
    minor, patch, alpha = match.groups()
    return (int(minor), int(patch), alpha is None, int(alpha or 0))


def completed(releases):
    return [release['tag_name'] for release in releases
            if release_key(release['tag_name']) and not release.get('draft')
            and any(asset['name'] == 'images.txt' for asset in release.get('assets', []))]


def latest_release(releases):
    tags = completed(releases)
    return max(tags, key=release_key) if tags else ''


def newest_in_channel(releases, candidate):
    """The newest completed release in the candidate's channel (stable or alpha), counting the candidate."""
    stable = release_key(candidate)[2]
    tags = [tag for tag in completed(releases) if release_key(tag)[2] == stable] + [candidate]
    return max(tags, key=release_key)


def image_matrix(text, repository):
    prefix = f'ghcr.io/{repository.split("/")[0].lower()}/'
    images = []
    for line in text.splitlines():
        if not re.fullmatch(re.escape(prefix) + r'quivr(?:-plugin-[a-z0-9._-]+|-(?:core|web|cpu-embeddings|postgres|seaweed|tei))?@sha256:[0-9a-f]{64}', line):
            raise ValueError(f'invalid release image digest: {line!r}')
        images.append({'image': line, 'name': line.split('@')[0].rsplit('/', 1)[1]})
    if not images or len({item['name'] for item in images}) != len(images):
        raise ValueError('release image list must be nonempty with unique image names')
    return {'include': images}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['release', 'matrix', 'newest'])
    parser.add_argument('--repository', default='The-Vibe-Company/quivr')
    parser.add_argument('--candidate', help='newest mode: the release being published')
    args = parser.parse_args()
    if args.mode == 'release':
        print(latest_release(json.load(sys.stdin)))
    elif args.mode == 'newest':
        if not args.candidate or not release_key(args.candidate):
            parser.error('newest needs --candidate with a Quivr 2 release tag')
        print(newest_in_channel(json.load(sys.stdin), args.candidate))
    else:
        print(json.dumps(image_matrix(sys.stdin.read(), args.repository)))


if __name__ == '__main__':
    main()
