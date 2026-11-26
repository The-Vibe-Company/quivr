#!/usr/bin/env python3
"""Content-addressed cache for local Go engine and plugin binaries in .scratch/bin.

Rebuilds a Go binary only when its module sources, local ``go.mod`` replace
dependencies, Go toolchain binary, or effective ``go env`` build environment
change. Serializes check/build/publish per binary with an advisory file lock
and pairs the binary inode/size with its fingerprint in a single atomic stamp.
"""
from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
import contextlib
import fcntl
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
BIN_DIR = ROOT / '.scratch' / 'bin'
FORMAT_VERSION = 'go-bin-cache-v2'
ENV_PREFIXES = ('GO', 'CGO_', 'PKG_CONFIG')
ENV_EXACT_KEYS = frozenset({'CC', 'CXX', 'AR', 'PATH'})
# go env keys that describe ephemeral temp paths, cache/telemetry locations, or
# invocation working directory rather than build outputs (GOGCCFLAGS embeds a
# random per-invocation go-build<n> directory in -ffile-prefix-map).
GO_ENV_IGNORED_KEYS = frozenset({
    'GOAUTH',
    'GOBIN',
    'GOCACHE',
    'GOCACHEPROG',
    'GOENV',
    'GOGCCFLAGS',
    'GOMOD',
    'GOMODCACHE',
    'GOPATH',
    'GOROOT',
    'GOTELEMETRY',
    'GOTELEMETRYDIR',
    'GOTMPDIR',
    'GOTOOLDIR',
})
IGNORED_DIRS = frozenset({
    '.git',
    '.scratch',
    '.pr-lens',
    '.venv',
    'venv',
    '__pycache__',
    'node_modules',
    '.pytest_cache',
    '.mypy_cache',
    '.ruff_cache',
})
IGNORED_NAMES = frozenset({'.DS_Store'})
IGNORED_SUFFIXES = ('.pyc', '.pyo', '.swp', '.swo')

_SAFE_NAME = re.compile(r'^[A-Za-z0-9._-]+$')
_GO_ENV_CACHE: dict[tuple[str, str, tuple[tuple[str, str], ...]], str] = {}


def _find_module_root(directory: pathlib.Path) -> pathlib.Path:
    current = directory.resolve()
    for candidate in (current, *current.parents):
        if (candidate / 'go.mod').is_file():
            return candidate
    return current


def _local_replaces(module_root: pathlib.Path) -> list[pathlib.Path]:
    """Return resolved local filesystem directories referenced by ``replace`` in go.mod."""
    gomod = module_root / 'go.mod'
    if not gomod.is_file():
        return []
    targets: list[pathlib.Path] = []
    in_block = False
    for raw_line in gomod.read_text(errors='replace').splitlines():
        line = raw_line.split('//', 1)[0].strip()
        if not line:
            continue
        if in_block:
            if line.startswith(')'):
                in_block = False
                continue
            spec = line
        elif line.startswith('replace ('):
            in_block = True
            continue
        elif line == 'replace':
            continue
        elif line.startswith('replace '):
            spec = line[len('replace '):].strip()
        else:
            continue
        if '=>' not in spec:
            continue
        rhs = spec.split('=>', 1)[1].strip()
        tokens = rhs.split()
        # Module version replacements carry two tokens (path + version); local
        # filesystem replacements carry a single directory path token.
        if len(tokens) != 1:
            continue
        raw_target = tokens[0].strip('"\'')
        candidate = pathlib.Path(raw_target)
        resolved = candidate.resolve() if candidate.is_absolute() else (module_root / candidate).resolve()
        if resolved.is_dir():
            targets.append(resolved)
    return targets


def _collect_module_roots(start_module: pathlib.Path) -> list[pathlib.Path]:
    """Return ``start_module`` and every transitive local ``replace`` module directory."""
    seen: set[pathlib.Path] = set()
    ordered: list[pathlib.Path] = []
    stack = [start_module.resolve()]
    while stack:
        current = stack.pop()
        if current in seen:
            continue
        seen.add(current)
        ordered.append(current)
        for dep in reversed(_local_replaces(current)):
            if dep not in seen:
                stack.append(dep)
    return sorted(ordered)


def _walk_module_files(module_root: pathlib.Path, *, include_tests: bool) -> list[pathlib.Path]:
    """Return sorted source and embeddable files belonging to one Go module.

    Follows Go's module and ``//go:embed`` boundaries: skips nested ``go.mod``
    submodules, skips ignored VCS/cache directories, and hashes every file
    (including ``testdata`` and binary assets) under directories that have a
    relevant ``.go`` file in themselves or an ancestor within the module (plus
    ``go.mod`` and ``go.sum`` at the module root).
    """
    files: list[pathlib.Path] = []
    for root_name in ('go.mod', 'go.sum'):
        candidate = module_root / root_name
        if candidate.is_file():
            files.append(candidate)

    # Track directories whose ancestor (or self) contains a relevant .go file.
    go_active_dirs: set[pathlib.Path] = set()
    for dirpath, dirnames, filenames in os.walk(module_root, topdown=True):
        current = pathlib.Path(dirpath)
        dirnames.sort()
        pruned: list[str] = []
        for d in dirnames:
            if d in IGNORED_DIRS:
                continue
            child = current / d
            if (child / 'go.mod').is_file():
                continue
            pruned.append(d)
        dirnames[:] = pruned

        has_go = any(
            name.endswith('.go') and (include_tests or not name.endswith('_test.go'))
            for name in filenames
        )
        parent_active = current != module_root and current.parent in go_active_dirs
        if has_go or parent_active:
            go_active_dirs.add(current)
        else:
            continue

        for name in sorted(filenames):
            if name in IGNORED_NAMES or name.endswith(IGNORED_SUFFIXES):
                continue
            if current == module_root and name in ('go.mod', 'go.sum'):
                continue
            if not include_tests and name.endswith('_test.go'):
                continue
            path = current / name
            if not path.is_file():
                continue
            files.append(path)
    return files


def _toolchain_identity(go: str, env: dict[str, str]) -> str:
    if os.sep in go or (os.altsep and os.altsep in go):
        candidate = pathlib.Path(go)
    else:
        found = shutil.which(go, path=env.get('PATH'))
        candidate = pathlib.Path(found) if found else None
    if candidate is not None:
        try:
            resolved = candidate.resolve()
            st = resolved.stat()
            return f'{resolved}:{st.st_size}:{st.st_mtime_ns}'
        except OSError:
            return str(candidate)
    return go


def _relevant_env_items(env: dict[str, str]) -> tuple[tuple[str, str], ...]:
    return tuple(sorted(
        (k, v) for k, v in env.items()
        if k in ENV_EXACT_KEYS or k.startswith(ENV_PREFIXES)
    ))


def _effective_go_env(go: str, cwd: pathlib.Path, env: dict[str, str]) -> str:
    """Return a canonical serialization of the effective ``go env`` build settings."""
    key = (go, str(cwd), _relevant_env_items(env))
    cached = _GO_ENV_CACHE.get(key)
    if cached is not None:
        return cached
    try:
        result = subprocess.run(
            [go, 'env', '-json'],
            cwd=cwd,
            env=env,
            capture_output=True,
            text=True,
            check=True,
        )
        parsed = json.loads(result.stdout)
        filtered = {k: v for k, v in sorted(parsed.items()) if k not in GO_ENV_IGNORED_KEYS}
        serialized = json.dumps(filtered, sort_keys=True, separators=(',', ':'))
    except (OSError, subprocess.CalledProcessError, ValueError):
        serialized = json.dumps(dict(key[2]), sort_keys=True, separators=(',', ':'))
    _GO_ENV_CACHE[key] = serialized
    return serialized


def fingerprint(
    cwd: pathlib.Path | str,
    package: str = '.',
    *,
    go: str | None = None,
    mode: str = 'build',
    extra_args: tuple[str, ...] = (),
    env: dict[str, str] | None = None,
) -> str:
    """Compute a deterministic SHA-256 fingerprint for a Go build invocation."""
    effective_env = dict(os.environ if env is None else env)
    go_cmd = go or effective_env.get('GO', 'go')
    cwd_path = pathlib.Path(cwd).resolve()
    module_root = _find_module_root(cwd_path)
    include_tests = mode == 'test'

    hasher = hashlib.sha256()
    hasher.update(FORMAT_VERSION.encode())
    hasher.update(b'\0')
    hasher.update(_toolchain_identity(go_cmd, effective_env).encode())
    hasher.update(b'\0')
    hasher.update(f'{mode}\0{cwd_path}\0{package}\0'.encode())
    for arg in extra_args:
        hasher.update(arg.encode())
        hasher.update(b'\0')
    hasher.update(_effective_go_env(go_cmd, cwd_path, effective_env).encode())
    hasher.update(b'\0')

    for root in _collect_module_roots(module_root):
        hasher.update(f'MODULE:{root}\0'.encode())
        for path in _walk_module_files(root, include_tests=include_tests):
            try:
                with path.open('rb') as stream:
                    rel = path.relative_to(root).as_posix()
                    hasher.update(rel.encode())
                    hasher.update(b'\0')
                    while chunk := stream.read(65536):
                        hasher.update(chunk)
                    hasher.update(b'\0')
            except OSError:
                continue
    return hasher.hexdigest()


def _cache_name(name: str, cwd: pathlib.Path, cache_dir: pathlib.Path) -> str:
    if not _SAFE_NAME.match(name):
        raise ValueError(f'unsafe cached binary name: {name!r}')
    if cache_dir.resolve() != BIN_DIR.resolve():
        return name
    resolved_cwd = cwd.resolve()
    resolved_root = ROOT.resolve()
    try:
        resolved_cwd.relative_to(resolved_root)
        return name
    except ValueError:
        suffix = hashlib.sha256(str(resolved_cwd).encode()).hexdigest()[:8]
        return f'{name}-{suffix}'


def _write_stamp_atomic(stamp_path: pathlib.Path, record: dict) -> None:
    fd, tmp_name = tempfile.mkstemp(prefix='.' + stamp_path.name + '.', dir=stamp_path.parent)
    tmp_path = pathlib.Path(tmp_name)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(record, stream, sort_keys=True)
            stream.write('\n')
        os.replace(tmp_path, stamp_path)
    finally:
        tmp_path.unlink(missing_ok=True)


def _stamp_matches(target: pathlib.Path, stamp_path: pathlib.Path, expected: str) -> bool:
    if not (target.is_file() and os.access(target, os.X_OK) and stamp_path.is_file()):
        return False
    try:
        record = json.loads(stamp_path.read_text(errors='replace'))
        st = target.stat()
    except (OSError, ValueError):
        return False
    return (
        isinstance(record, dict)
        and record.get('fingerprint') == expected
        and record.get('inode') == st.st_ino
        and record.get('size') == st.st_size
    )


@contextlib.contextmanager
def _binary_lock(directory: pathlib.Path, cached_name: str, *, exclusive: bool):
    directory.mkdir(parents=True, exist_ok=True)
    lock_path = directory / f'.{cached_name}.lock'
    with lock_path.open('a+') as lock_file:
        fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH)
        try:
            yield
        finally:
            fcntl.flock(lock_file.fileno(), fcntl.LOCK_UN)


def _paths(
    name: str,
    cwd: pathlib.Path,
    cache_dir: pathlib.Path | str | None,
) -> tuple[pathlib.Path, str, pathlib.Path, pathlib.Path]:
    directory = pathlib.Path(cache_dir) if cache_dir is not None else BIN_DIR
    cached_name = _cache_name(name, cwd, directory)
    return directory, cached_name, directory / cached_name, directory / (cached_name + '.stamp')


def _build_uncached(
    target: pathlib.Path,
    stamp_path: pathlib.Path,
    expected: str,
    cwd: pathlib.Path,
    package: str,
    *,
    go: str,
    mode: str,
    extra_args: tuple[str, ...],
    env: dict[str, str] | None,
) -> pathlib.Path:
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix='.' + target.name + '.', dir=target.parent)
    os.close(fd)
    tmp_path = pathlib.Path(tmp_name)
    try:
        command = [go, 'build', *extra_args, '-o', str(tmp_path), package] if mode == 'build' else [
            go, 'test', '-c', *extra_args, '-o', str(tmp_path), package
        ]
        subprocess.run(command, cwd=cwd, env=env, check=True)
        os.chmod(tmp_path, 0o755)
        os.replace(tmp_path, target)
        st = target.stat()
        _write_stamp_atomic(stamp_path, {'fingerprint': expected, 'inode': st.st_ino, 'size': st.st_size})
        return target
    finally:
        tmp_path.unlink(missing_ok=True)


def _ensure_locked(
    name: str,
    cwd: pathlib.Path,
    package: str,
    *,
    go: str,
    mode: str,
    cache_dir: pathlib.Path | str | None,
    extra_args: tuple[str, ...],
    env: dict[str, str] | None,
) -> pathlib.Path:
    if mode not in ('build', 'test'):
        raise ValueError(f'unsupported build mode: {mode!r}')
    directory, cached_name, target, stamp_path = _paths(name, cwd, cache_dir)
    expected = fingerprint(cwd, package, go=go, mode=mode, extra_args=extra_args, env=env)
    with _binary_lock(directory, cached_name, exclusive=False):
        if _stamp_matches(target, stamp_path, expected):
            return target
    with _binary_lock(directory, cached_name, exclusive=True):
        # Recompute inside the exclusive lock so concurrent builds never pair a
        # binary from one source revision with a stamp from another.
        expected = fingerprint(cwd, package, go=go, mode=mode, extra_args=extra_args, env=env)
        if _stamp_matches(target, stamp_path, expected):
            return target
        return _build_uncached(
            target, stamp_path, expected, cwd, package,
            go=go, mode=mode, extra_args=extra_args, env=env,
        )


def ensure(
    name: str,
    cwd: pathlib.Path | str,
    package: str = '.',
    *,
    go: str | None = None,
    mode: str = 'build',
    cache_dir: pathlib.Path | str | None = None,
    extra_args: tuple[str, ...] = (),
    env: dict[str, str] | None = None,
) -> pathlib.Path:
    """Return a cached Go binary in ``.scratch/bin``, rebuilding only when inputs changed."""
    cwd_path = pathlib.Path(cwd).resolve()
    go_cmd = go or (os.environ if env is None else env).get('GO', 'go')
    return _ensure_locked(
        name, cwd_path, package,
        go=go_cmd, mode=mode, cache_dir=cache_dir, extra_args=extra_args, env=env,
    )


def ensure_many(specs: list[dict], *, max_workers: int | None = None) -> dict[str, pathlib.Path]:
    """Ensure multiple Go binaries, running only cache misses in parallel."""
    results: dict[str, pathlib.Path] = {}
    pending: list[tuple[str, pathlib.Path, str, str, str, pathlib.Path | str | None, tuple[str, ...], dict[str, str] | None]] = []
    for spec in specs:
        name = spec['name']
        cwd_path = pathlib.Path(spec['cwd']).resolve()
        package = spec.get('package', '.')
        env = spec.get('env')
        go_cmd = spec.get('go') or (os.environ if env is None else env).get('GO', 'go')
        mode = spec.get('mode', 'build')
        if mode not in ('build', 'test'):
            raise ValueError(f'unsupported build mode: {mode!r}')
        cache_dir = spec.get('cache_dir')
        extra_args = tuple(spec.get('extra_args', ()))
        directory, cached_name, target, stamp_path = _paths(name, cwd_path, cache_dir)
        expected = fingerprint(cwd_path, package, go=go_cmd, mode=mode, extra_args=extra_args, env=env)
        with _binary_lock(directory, cached_name, exclusive=False):
            if _stamp_matches(target, stamp_path, expected):
                results[name] = target
                continue
        pending.append((name, cwd_path, package, go_cmd, mode, cache_dir, extra_args, env))

    if len(pending) == 1:
        name, cwd_path, package, go_cmd, mode, cache_dir, extra_args, env = pending[0]
        results[name] = _ensure_locked(
            name, cwd_path, package,
            go=go_cmd, mode=mode, cache_dir=cache_dir, extra_args=extra_args, env=env,
        )
    elif len(pending) > 1:
        workers = max_workers or min(len(pending), os.cpu_count() or 4, 4)
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = {
                item[0]: pool.submit(
                    _ensure_locked,
                    item[0], item[1], item[2],
                    go=item[3], mode=item[4], cache_dir=item[5], extra_args=item[6], env=item[7],
                )
                for item in pending
            }
            for name, future in futures.items():
                results[name] = future.result()
    return results


def materialize(cached: pathlib.Path | str, destination: pathlib.Path | str) -> pathlib.Path:
    """Expose a cached binary at ``destination`` via atomic hardlink (copy fallback)."""
    src = pathlib.Path(cached)
    dst = pathlib.Path(destination)
    dst.parent.mkdir(parents=True, exist_ok=True)
    if dst.exists():
        try:
            if os.path.samefile(src, dst):
                return dst
        except OSError:
            pass
    fd, tmp_name = tempfile.mkstemp(prefix='.' + dst.name + '.', dir=dst.parent)
    os.close(fd)
    tmp_path = pathlib.Path(tmp_name)
    try:
        tmp_path.unlink()
        try:
            os.link(src, tmp_path)
        except OSError:
            shutil.copy2(src, tmp_path)
        os.replace(tmp_path, dst)
        return dst
    finally:
        tmp_path.unlink(missing_ok=True)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    cmd = sub.add_parser('ensure', help='Build or reuse a cached Go binary and print its path')
    cmd.add_argument('--name', required=True)
    cmd.add_argument('--cwd', type=pathlib.Path, default=ROOT)
    cmd.add_argument('--package', default='.')
    cmd.add_argument('--mode', choices=('build', 'test'), default='build')
    cmd.add_argument('--go', default=None)
    cmd.add_argument('--cache-dir', type=pathlib.Path, default=None)
    cmd.add_argument('--materialize', type=pathlib.Path, default=None)
    args = parser.parse_args(argv)
    binary = ensure(
        args.name,
        args.cwd,
        args.package,
        go=args.go,
        mode=args.mode,
        cache_dir=args.cache_dir,
    )
    if args.materialize is not None:
        binary = materialize(binary, args.materialize)
    print(binary)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
