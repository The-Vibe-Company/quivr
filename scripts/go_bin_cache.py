#!/usr/bin/env python3
"""Content-addressed cache for local Go engine and plugin binaries in .scratch/bin.

Rebuilds a Go binary only when its module sources, local ``go.mod`` replace
dependencies, Go toolchain binary, or relevant build environment variables
change. Publishes binaries and stamps atomically so concurrent runs never
observe partial files.
"""
from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
BIN_DIR = ROOT / '.scratch' / 'bin'
FORMAT_VERSION = 'go-bin-cache-v1'
BUILD_ENV_KEYS = (
    'GOOS',
    'GOARCH',
    'CGO_ENABLED',
    'GOFLAGS',
    'GOEXPERIMENT',
    'GOTOOLCHAIN',
    'GOWORK',
    'CC',
    'CXX',
)
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
BINARY_MAGICS = frozenset({
    b'\x7fELF',          # Linux ELF
    b'\xfe\xed\xfa\xce', # Mach-O 32-bit BE
    b'\xce\xfa\xed\xfe', # Mach-O 32-bit LE
    b'\xfe\xed\xfa\xcf', # Mach-O 64-bit BE
    b'\xcf\xfa\xed\xfe', # Mach-O 64-bit LE
    b'\xca\xfe\xba\xbe', # Mach-O universal / fat
})

_SAFE_NAME = re.compile(r'^[A-Za-z0-9._-]+$')


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


def _walk_module_files(module_root: pathlib.Path, *, include_tests: bool, include_testdata: bool) -> list[pathlib.Path]:
    """Return sorted source and embeddable files belonging to one Go module.

    Follows Go's module and ``//go:embed`` boundaries: skips nested ``go.mod``
    submodules, skips ignored VCS/cache directories, and only hashes files under
    directories that have a relevant ``.go`` file in themselves or an ancestor
    within the module (plus ``go.mod`` and ``go.sum`` at the module root).
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
            if d == 'testdata' and not include_testdata:
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
    effective_env = os.environ if env is None else env
    go_cmd = go or effective_env.get('GO', 'go')
    cwd_path = pathlib.Path(cwd).resolve()
    module_root = _find_module_root(cwd_path)
    include_tests = mode == 'test'
    include_testdata = include_tests or 'testdata' in pathlib.PurePosixPath(package).parts

    hasher = hashlib.sha256()
    hasher.update(FORMAT_VERSION.encode())
    hasher.update(b'\0')
    hasher.update(_toolchain_identity(go_cmd, dict(effective_env)).encode())
    hasher.update(b'\0')
    hasher.update(f'{mode}\0{cwd_path}\0{package}\0'.encode())
    for arg in extra_args:
        hasher.update(arg.encode())
        hasher.update(b'\0')
    for key in BUILD_ENV_KEYS:
        hasher.update(f'{key}={effective_env.get(key, "")}\0'.encode())

    for root in _collect_module_roots(module_root):
        hasher.update(f'MODULE:{root}\0'.encode())
        for path in _walk_module_files(root, include_tests=include_tests, include_testdata=include_testdata):
            try:
                with path.open('rb') as stream:
                    header = stream.read(4)
                    if header in BINARY_MAGICS:
                        continue
                    rel = path.relative_to(root).as_posix()
                    hasher.update(rel.encode())
                    hasher.update(b'\0')
                    hasher.update(header)
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


def _write_stamp_atomic(stamp_path: pathlib.Path, value: str) -> None:
    fd, tmp_name = tempfile.mkstemp(prefix='.' + stamp_path.name + '.', dir=stamp_path.parent)
    tmp_path = pathlib.Path(tmp_name)
    try:
        with os.fdopen(fd, 'w') as stream:
            stream.write(value + '\n')
        os.replace(tmp_path, stamp_path)
    finally:
        tmp_path.unlink(missing_ok=True)


def _check_cache(
    name: str,
    cwd: pathlib.Path | str,
    package: str = '.',
    *,
    go: str | None = None,
    mode: str = 'build',
    cache_dir: pathlib.Path | str | None = None,
    extra_args: tuple[str, ...] = (),
    env: dict[str, str] | None = None,
) -> tuple[pathlib.Path, pathlib.Path, str, bool]:
    if mode not in ('build', 'test'):
        raise ValueError(f'unsupported build mode: {mode!r}')
    cwd_path = pathlib.Path(cwd).resolve()
    directory = pathlib.Path(cache_dir) if cache_dir is not None else BIN_DIR
    cached_name = _cache_name(name, cwd_path, directory)
    target = directory / cached_name
    stamp_path = directory / (cached_name + '.stamp')
    expected = fingerprint(cwd_path, package, go=go, mode=mode, extra_args=extra_args, env=env)
    hit = (
        target.is_file()
        and os.access(target, os.X_OK)
        and stamp_path.is_file()
        and stamp_path.read_text(errors='replace').strip() == expected
    )
    return target, stamp_path, expected, hit


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
        _write_stamp_atomic(stamp_path, expected)
        return target
    finally:
        tmp_path.unlink(missing_ok=True)


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
    target, stamp_path, expected, hit = _check_cache(
        name, cwd_path, package, go=go_cmd, mode=mode, cache_dir=cache_dir, extra_args=extra_args, env=env
    )
    if hit:
        return target
    return _build_uncached(
        target, stamp_path, expected, cwd_path, package,
        go=go_cmd, mode=mode, extra_args=extra_args, env=env,
    )


def ensure_many(specs: list[dict], *, max_workers: int | None = None) -> dict[str, pathlib.Path]:
    """Ensure multiple Go binaries, running only cache misses in parallel."""
    results: dict[str, pathlib.Path] = {}
    pending: list[tuple[str, pathlib.Path, pathlib.Path, str, pathlib.Path, str, str, str, tuple[str, ...], dict[str, str] | None]] = []
    for spec in specs:
        name = spec['name']
        cwd_path = pathlib.Path(spec['cwd']).resolve()
        package = spec.get('package', '.')
        env = spec.get('env')
        go_cmd = spec.get('go') or (os.environ if env is None else env).get('GO', 'go')
        mode = spec.get('mode', 'build')
        cache_dir = spec.get('cache_dir')
        extra_args = tuple(spec.get('extra_args', ()))
        target, stamp_path, expected, hit = _check_cache(
            name, cwd_path, package, go=go_cmd, mode=mode, cache_dir=cache_dir, extra_args=extra_args, env=env
        )
        if hit:
            results[name] = target
        else:
            pending.append((name, target, stamp_path, expected, cwd_path, package, go_cmd, mode, extra_args, env))

    if len(pending) == 1:
        name, target, stamp_path, expected, cwd_path, package, go_cmd, mode, extra_args, env = pending[0]
        results[name] = _build_uncached(
            target, stamp_path, expected, cwd_path, package,
            go=go_cmd, mode=mode, extra_args=extra_args, env=env,
        )
    elif len(pending) > 1:
        workers = max_workers or min(len(pending), os.cpu_count() or 4, 4)
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = {
                item[0]: pool.submit(
                    _build_uncached,
                    item[1], item[2], item[3], item[4], item[5],
                    go=item[6], mode=item[7], extra_args=item[8], env=item[9],
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
