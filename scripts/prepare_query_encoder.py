#!/usr/bin/env python3
"""Prepare and verify the pinned offline EmbeddingGemma text snapshot."""

import argparse
import hashlib
import json
import os
import pathlib
import re
import tempfile
import time
import urllib.parse
import urllib.request


ROOT = pathlib.Path(__file__).resolve().parents[1]
LOCK_PATH = ROOT / 'third_party' / 'query-encoder' / 'model-lock.json'
DOWNLOAD_TIMEOUT_SECONDS = 120  # Socket idle timeout.
DOWNLOAD_TOTAL_SECONDS = 600
CHUNK_SIZE = 1024 * 1024
_SUPPORTED_BACKENDS = ('onnx', 'pytorch')


def _valid_repository(value):
    if not isinstance(value, str) or not value or len(value) > 128 or '--' in value or '..' in value:
        return False
    parts = value.split('/')
    return (len(parts) in (1, 2)
            and all(re.fullmatch(r'[A-Za-z0-9_](?:[A-Za-z0-9_.-]*[A-Za-z0-9_])?', part)
                    for part in parts))


def _read_lock(lock_path):
    try:
        lock = json.loads(pathlib.Path(lock_path).read_text())
        files = lock['files']
        model = lock['model']
        model_revision = lock['model_revision']
        revision = lock['source_revision']
        dimensions = lock['dimensions']
    except (OSError, KeyError, TypeError, ValueError) as exc:
        raise RuntimeError('invalid query encoder asset lock') from exc
    if (not isinstance(model, str) or not model or not isinstance(model_revision, str)
            or not model_revision or not isinstance(revision, str) or not revision):
        raise RuntimeError('invalid query encoder asset lock metadata')
    if type(dimensions) is not int or dimensions != 768 or not isinstance(files, dict) or not files:
        raise RuntimeError('invalid query encoder asset lock metadata')
    backend = lock.get('backend')
    if backend is not None and backend not in _SUPPORTED_BACKENDS:
        raise RuntimeError('invalid query encoder asset lock backend')
    precision = lock.get('precision')
    if precision is not None and (not isinstance(precision, str) or not precision.strip()):
        raise RuntimeError('invalid query encoder asset lock precision')
    asset_repository = lock.get('asset_repository')
    if asset_repository is not None and not _valid_repository(asset_repository):
        raise RuntimeError('invalid query encoder asset repository')
    asset_revision = lock.get('asset_revision')
    if asset_revision is not None and (
            not isinstance(asset_revision, str) or not asset_revision
            or any(char.isspace() or '/' in char for char in asset_revision)
            or '..' in asset_revision):
        raise RuntimeError('invalid query encoder asset revision')
    normalized = {}
    for name, entry in files.items():
        if not isinstance(name, str) or not name or pathlib.PurePosixPath(name).is_absolute():
            raise RuntimeError('invalid query encoder asset path')
        relative = pathlib.PurePosixPath(name)
        if '..' in relative.parts or not isinstance(entry, dict):
            raise RuntimeError('invalid query encoder asset lock')
        digest = entry.get('sha256')
        size = entry.get('size')
        if (not isinstance(digest, str) or len(digest) != 64
                or any(char not in '0123456789abcdef' for char in digest)
                or type(size) is not int or size < 0):
            raise RuntimeError('invalid query encoder asset lock')
        normalized[name] = {'sha256': digest, 'size': size}
    model_file = lock.get('model_file')
    if model_file is not None and (
            not isinstance(model_file, str) or not model_file.endswith('.onnx')
            or model_file not in normalized):
        raise RuntimeError('invalid query encoder model_file in asset lock')
    lock['files'] = normalized
    return lock


def _asset_path(directory, name):
    root = pathlib.Path(directory).resolve()
    path = (root / pathlib.PurePosixPath(name)).resolve()
    try:
        path.relative_to(root)
    except ValueError as exc:
        raise RuntimeError('invalid query encoder asset path') from exc
    return path


def _mirror_base_url(value):
    """Validate an explicit mirror root before constructing revision-relative paths."""
    if value is None:
        return None
    if (not isinstance(value, str) or not value
            or any(character.isspace() or ord(character) < 32 for character in value)):
        raise ValueError('model mirror must be a nonempty HTTP(S) URL')
    parsed = urllib.parse.urlsplit(value)
    if (parsed.scheme not in ('http', 'https') or not parsed.netloc
            or parsed.username is not None or parsed.password is not None
            or parsed.query or parsed.fragment):
        raise ValueError('model mirror must be an HTTP(S) URL without credentials or query')
    if '..' in pathlib.PurePosixPath(parsed.path).parts:
        raise ValueError('model mirror path must not contain parent traversal')
    return value.rstrip('/')


def _asset_url(lock, name, mirror_base_url=None):
    mirror = _mirror_base_url(mirror_base_url)
    if mirror is None:
        mirror = 'https://huggingface.co'
    repository = lock.get('asset_repository') or lock['model']
    revision = lock.get('asset_revision') or lock['source_revision']
    return (mirror + '/' + urllib.parse.quote(repository, safe='/')
            + '/resolve/' + urllib.parse.quote(revision, safe='') + '/'
            + urllib.parse.quote(name, safe='/') + '?download=true')


def _digest(path):
    digest = hashlib.sha256()
    size = 0
    with pathlib.Path(path).open('rb') as stream:
        while chunk := stream.read(CHUNK_SIZE):
            digest.update(chunk)
            size += len(chunk)
    return digest.hexdigest(), size


def _verify_asset(path, expected, name):
    if not path.is_file():
        raise RuntimeError('missing pinned model asset: ' + name)
    actual, size = _digest(path)
    if size != expected['size']:
        raise RuntimeError('size mismatch for pinned model asset: ' + name)
    if actual != expected['sha256']:
        raise RuntimeError('checksum mismatch for pinned model asset: ' + name)


def _normalize_mode(path):
    path.chmod(0o644)


def verify_model(directory, *, lock_path=LOCK_PATH):
    """Verify every pinned asset before an offline model loader can use it."""
    lock = _read_lock(lock_path)
    for name, expected in lock['files'].items():
        _verify_asset(_asset_path(directory, name), expected, name)
    return lock


def _download_asset(directory, name, expected, lock, *, mirror_base_url=None):
    path = _asset_path(directory, name)
    path.parent.mkdir(parents=True, exist_ok=True)
    # A sibling temporary file keeps a failed stream from becoming a plausible asset.
    fd, temporary_name = tempfile.mkstemp(prefix='.' + path.name + '.', suffix='.partial',
                                          dir=path.parent)
    temporary = pathlib.Path(temporary_name)
    digest = hashlib.sha256()
    size = 0
    url = _asset_url(lock, name, mirror_base_url)
    try:
        deadline = time.monotonic() + DOWNLOAD_TOTAL_SECONDS
        with os.fdopen(fd, 'wb') as output:
            with urllib.request.urlopen(url, timeout=DOWNLOAD_TIMEOUT_SECONDS) as response:
                while True:
                    if time.monotonic() >= deadline:
                        raise TimeoutError('model asset transfer deadline exceeded')
                    chunk = response.read1(min(CHUNK_SIZE, expected['size'] + 1 - size))
                    if not chunk:
                        break
                    size += len(chunk)
                    if size > expected['size']:
                        raise RuntimeError('size mismatch for pinned model asset: ' + name)
                    output.write(chunk)
                    digest.update(chunk)
            output.flush()
            os.fsync(output.fileno())
            if size != expected['size']:
                raise RuntimeError('size mismatch for pinned model asset: ' + name)
            if digest.hexdigest() != expected['sha256']:
                raise RuntimeError('checksum mismatch for pinned model asset: ' + name)
            # The final image runs as a non-root user. Apply the public mode
            # only after the complete stream has passed both checks.
            os.fchmod(output.fileno(), 0o644)
            os.fsync(output.fileno())
        temporary.replace(path)
    except Exception:
        temporary.unlink(missing_ok=True)
        raise


def prepare(output, *, verify_only=False, lock_path=LOCK_PATH, mirror_base_url=None, mirror=None):
    """Verify an existing snapshot or download missing/corrupt assets atomically."""
    output = pathlib.Path(output)
    lock = _read_lock(lock_path)
    if mirror is not None and mirror_base_url is not None and mirror != mirror_base_url:
        raise ValueError('mirror and mirror_base_url disagree')
    if mirror is not None:
        mirror_base_url = mirror
    mirror_base_url = _mirror_base_url(mirror_base_url)
    if verify_only:
        verify_model(output, lock_path=lock_path)
        return lock
    output.mkdir(parents=True, exist_ok=True)
    for name, expected in lock['files'].items():
        path = _asset_path(output, name)
        try:
            _verify_asset(path, expected, name)
        except RuntimeError:
            _download_asset(output, name, expected, lock, mirror_base_url=mirror_base_url)
            _verify_asset(path, expected, name)
        else:
            _normalize_mode(path)
    return verify_model(output, lock_path=lock_path)


def _parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=pathlib.Path)
    parser.add_argument('--verify-only', action='store_true')
    parser.add_argument('--mirror-base-url', '--mirror', dest='mirror_base_url')
    parser.add_argument('--lock-path', type=pathlib.Path, default=None)
    return parser


def main(argv=None):
    args = _parser().parse_args(argv)
    kwargs = {'verify_only': args.verify_only, 'mirror_base_url': args.mirror_base_url}
    if args.lock_path is not None:
        kwargs['lock_path'] = args.lock_path
    lock = prepare(args.output, **kwargs)
    print(json.dumps({
        'model': lock['model'],
        'model_revision': lock['model_revision'],
        'source_revision': lock['source_revision'],
        'dimensions': lock['dimensions'],
        'output': str(args.output),
        'verified_files': len(lock['files']),
        'verify_only': args.verify_only,
    }, indent=2, sort_keys=True))
    return 0


if __name__ == '__main__':
    main()
