#!/usr/bin/env python3
"""Prepare and verify the pinned offline EmbeddingGemma text snapshot."""

import argparse
import hashlib
import json
import os
import pathlib
import tempfile
import urllib.parse
import urllib.request


ROOT = pathlib.Path(__file__).resolve().parents[1]
LOCK_PATH = ROOT / 'third_party' / 'query-encoder' / 'model-lock.json'
DOWNLOAD_TIMEOUT_SECONDS = 120
CHUNK_SIZE = 1024 * 1024


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


def _download_asset(directory, name, expected, lock):
    path = _asset_path(directory, name)
    path.parent.mkdir(parents=True, exist_ok=True)
    # A sibling temporary file keeps a failed stream from becoming a plausible asset.
    fd, temporary_name = tempfile.mkstemp(prefix='.' + path.name + '.', suffix='.partial',
                                          dir=path.parent)
    temporary = pathlib.Path(temporary_name)
    digest = hashlib.sha256()
    size = 0
    url = ('https://huggingface.co/' + urllib.parse.quote(lock['model'], safe='/')
           + '/resolve/' + urllib.parse.quote(lock['source_revision'], safe='') + '/'
           + urllib.parse.quote(name, safe='/') + '?download=true')
    try:
        with os.fdopen(fd, 'wb') as output:
            with urllib.request.urlopen(url, timeout=DOWNLOAD_TIMEOUT_SECONDS) as response:
                while chunk := response.read(CHUNK_SIZE):
                    output.write(chunk)
                    digest.update(chunk)
                    size += len(chunk)
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


def prepare(output, *, verify_only=False, lock_path=LOCK_PATH):
    """Verify an existing snapshot or download missing/corrupt assets atomically."""
    output = pathlib.Path(output)
    lock = _read_lock(lock_path)
    if verify_only:
        verify_model(output, lock_path=lock_path)
        return lock
    output.mkdir(parents=True, exist_ok=True)
    for name, expected in lock['files'].items():
        path = _asset_path(output, name)
        try:
            _verify_asset(path, expected, name)
        except RuntimeError:
            _download_asset(output, name, expected, lock)
            _verify_asset(path, expected, name)
        else:
            _normalize_mode(path)
    return verify_model(output, lock_path=lock_path)


def _parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=pathlib.Path)
    parser.add_argument('--verify-only', action='store_true')
    return parser


def main(argv=None):
    args = _parser().parse_args(argv)
    lock = prepare(args.output, verify_only=args.verify_only)
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
