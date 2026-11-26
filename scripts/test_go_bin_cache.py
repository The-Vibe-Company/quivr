"""Content-addressed caching for local Go engine and plugin binaries (OB-620).

Exercises real `go build` on tiny throwaway modules with a local `replace`
dependency, embedded binary assets, and `testdata`, with zero network access
or sleeps.
"""
from concurrent.futures import ThreadPoolExecutor
import errno
import io
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

import go_bin_cache

GO = os.environ.get('GO', 'go')


def _write_module(
    root: pathlib.Path,
    *,
    dep_msg: str = 'dep-v1',
    embed_msg: str = 'embed-v1',
    testdata_msg: str = 'td-v1',
    elf_payload: bytes = b'\x7fELF\x01',
) -> tuple[pathlib.Path, pathlib.Path]:
    dep = root / 'dep'
    app = root / 'app'
    dep.mkdir(parents=True, exist_ok=True)
    (app / 'testdata').mkdir(parents=True, exist_ok=True)
    (dep / 'go.mod').write_text('module example.com/dep\n\ngo 1.21\n')
    (dep / 'dep.go').write_text(f'package dep\n\nfunc Value() string {{ return {json.dumps(dep_msg)} }}\n')
    (app / 'go.mod').write_text(
        'module example.com/app\n\n'
        'go 1.21\n\n'
        'require example.com/dep v0.0.0\n\n'
        'replace example.com/dep => ../dep\n'
    )
    (app / 'data.txt').write_text(embed_msg)
    (app / 'testdata' / 'fixture.txt').write_text(testdata_msg)
    (app / 'asset.bin').write_bytes(elf_payload)
    (app / 'main.go').write_text(
        'package main\n\n'
        'import (\n'
        '\t_ "embed"\n'
        '\t"fmt"\n'
        '\t"example.com/dep"\n'
        ')\n\n'
        '//go:embed data.txt\n'
        'var data string\n\n'
        '//go:embed testdata/fixture.txt\n'
        'var fixture string\n\n'
        '//go:embed asset.bin\n'
        'var asset []byte\n\n'
        'func main() { fmt.Printf("%s:%s:%s:%x", dep.Value(), data, fixture, asset) }\n'
    )
    return app, dep


class GoBinCache(unittest.TestCase):
    def setUp(self):
        self.env = {**os.environ, 'GOWORK': 'off', 'GOFLAGS': ''}

    def test_caches_binary_and_rebuilds_for_sources_replace_testdata_binary_assets_and_go_env(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            cache = root / 'bin'
            app, dep = _write_module(root)

            binary = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(binary, cache / 'sample')
            stamp = json.loads((cache / 'sample.stamp').read_text())
            self.assertEqual(stamp['inode'], binary.stat().st_ino)
            self.assertEqual(stamp['size'], binary.stat().st_size)
            # Mutating the cached binary in place (same inode, mismatched size) must force a rebuild.
            with binary.open('ab') as stream:
                stream.write(b'\0')
            with mock.patch.object(go_bin_cache, '_build_uncached', wraps=go_bin_cache._build_uncached) as build:
                binary = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            build.assert_called_once()
            self.assertEqual(subprocess.check_output([str(binary)], text=True), 'dep-v1:embed-v1:td-v1:7f454c4601')

            # Unchanged inputs hit the cache without spawning `go build`, even in a fresh
            # process where the in-memory `go env` cache is cold.
            go_bin_cache._GO_ENV_CACHE.clear()
            with mock.patch.object(go_bin_cache, '_build_uncached', wraps=go_bin_cache._build_uncached) as build:
                again = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(again, binary)
            build.assert_not_called()

            # Editing a test-only file does not invalidate a non-test build.
            (app / 'sample_test.go').write_text('package main\nimport "testing"\nfunc TestNoop(t *testing.T) {}\n')
            with mock.patch.object(go_bin_cache, '_build_uncached', wraps=go_bin_cache._build_uncached) as build:
                go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            build.assert_not_called()

            # Editing a local `replace` dependency invalidates and rebuilds.
            (dep / 'dep.go').write_text('package dep\n\nfunc Value() string { return "dep-v2" }\n')
            rebuilt = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(subprocess.check_output([str(rebuilt)], text=True), 'dep-v2:embed-v1:td-v1:7f454c4601')

            # Editing an embedded file inside testdata/ invalidates and rebuilds.
            (app / 'testdata' / 'fixture.txt').write_text('td-v2')
            rebuilt = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(subprocess.check_output([str(rebuilt)], text=True), 'dep-v2:embed-v1:td-v2:7f454c4601')

            # Editing an embedded binary asset with ELF magic invalidates and rebuilds.
            (app / 'asset.bin').write_bytes(b'\x7fELF\x02')
            rebuilt = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(subprocess.check_output([str(rebuilt)], text=True), 'dep-v2:embed-v1:td-v2:7f454c4602')

            # Changing effective Go build env vars (GOFLAGS, CGO_CFLAGS) triggers a real rebuild,
            # and a repeat call with the same env hits the cache again.
            for env_override in ({'GOFLAGS': '-trimpath'}, {'CGO_CFLAGS': '-O1'}):
                with self.subTest(env_override=env_override):
                    updated_env = {**self.env, **env_override}
                    with mock.patch.object(go_bin_cache, '_build_uncached', wraps=go_bin_cache._build_uncached) as build:
                        rebuilt = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=updated_env)
                        self.assertEqual(build.call_count, 1)
                        go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=updated_env)
                        self.assertEqual(build.call_count, 1)
                    self.assertEqual(subprocess.check_output([str(rebuilt)], text=True), 'dep-v2:embed-v1:td-v2:7f454c4602')

    def test_concurrent_ensure_serializes_and_detects_mismatched_inode_stamp(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            cache = root / 'bin'
            app, _ = _write_module(root)

            with mock.patch.object(go_bin_cache, '_build_uncached', wraps=go_bin_cache._build_uncached) as build:
                with ThreadPoolExecutor(max_workers=4) as pool:
                    results = list(pool.map(
                        lambda _: go_bin_cache.ensure('concurrent', app, '.', go=GO, cache_dir=cache, env=self.env),
                        range(4),
                    ))
            self.assertEqual(build.call_count, 1)
            self.assertTrue(all(p == cache / 'concurrent' for p in results))

            # A stamp whose recorded inode disagrees with the binary forces a clean rebuild.
            stamp_path = cache / 'concurrent.stamp'
            record = json.loads(stamp_path.read_text())
            record['inode'] = -1
            stamp_path.write_text(json.dumps(record))
            with mock.patch.object(go_bin_cache, '_build_uncached', wraps=go_bin_cache._build_uncached) as build:
                rebuilt = go_bin_cache.ensure('concurrent', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(build.call_count, 1)
            self.assertEqual(subprocess.check_output([str(rebuilt)], text=True), 'dep-v1:embed-v1:td-v1:7f454c4601')

    def test_materialize_exposes_distinct_executable_and_supports_copy_fallback(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            cache = root / 'bin'
            app, dep = _write_module(root)

            cached = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            dest = root / 'stack' / 'sample'
            materialized = go_bin_cache.materialize(cached, dest)
            self.assertEqual(materialized, dest)
            self.assertNotEqual(dest.resolve(), cached.resolve())
            self.assertTrue(os.access(dest, os.X_OK))
            self.assertEqual(subprocess.check_output([str(dest)], text=True), 'dep-v1:embed-v1:td-v1:7f454c4601')

            # Rebuilding cached replaces it atomically; dest keeps serving v1 until rematerialized.
            (dep / 'dep.go').write_text('package dep\n\nfunc Value() string { return "dep-v2" }\n')
            cached_v2 = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(subprocess.check_output([str(dest)], text=True), 'dep-v1:embed-v1:td-v1:7f454c4601')
            go_bin_cache.materialize(cached_v2, dest)
            self.assertEqual(subprocess.check_output([str(dest)], text=True), 'dep-v2:embed-v1:td-v1:7f454c4601')

            # Filesystems without hardlinks fall back to an atomic copy cleanly.
            copied = root / 'cross-device' / 'sample'
            with mock.patch.object(go_bin_cache.os, 'link', side_effect=OSError(errno.EXDEV, 'cross-device link')):
                go_bin_cache.materialize(cached_v2, copied)
            self.assertTrue(os.access(copied, os.X_OK))
            self.assertFalse(os.path.samefile(cached_v2, copied))
            self.assertEqual(subprocess.check_output([str(copied)], text=True), 'dep-v2:embed-v1:td-v1:7f454c4601')

    def test_ensure_many_builds_only_misses(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            cache = root / 'bin'
            app_a, _ = _write_module(root / 'a', dep_msg='a1')
            app_b, _ = _write_module(root / 'b', dep_msg='b1')
            specs = [
                {'name': 'mod-a', 'cwd': app_a, 'package': '.', 'go': GO, 'cache_dir': cache, 'env': self.env},
                {'name': 'mod-b', 'cwd': app_b, 'package': '.', 'go': GO, 'cache_dir': cache, 'env': self.env},
            ]
            built = go_bin_cache.ensure_many(specs)
            self.assertEqual(subprocess.check_output([str(built['mod-a'])], text=True), 'a1:embed-v1:td-v1:7f454c4601')
            self.assertEqual(subprocess.check_output([str(built['mod-b'])], text=True), 'b1:embed-v1:td-v1:7f454c4601')

            with mock.patch.object(go_bin_cache, '_build_uncached', wraps=go_bin_cache._build_uncached) as build:
                go_bin_cache.ensure_many(specs)
                self.assertEqual(build.call_count, 0)
                (app_b / 'data.txt').write_text('b-updated')
                updated = go_bin_cache.ensure_many(specs)
                self.assertEqual(build.call_count, 1)
            self.assertEqual(subprocess.check_output([str(updated['mod-b'])], text=True), 'b1:b-updated:td-v1:7f454c4601')

    def test_cli_ensure_prints_cached_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            cache = root / 'bin'
            app, _ = _write_module(root)
            out = io.StringIO()
            with mock.patch.dict(os.environ, self.env, clear=True), mock.patch('sys.stdout', out):
                rc = go_bin_cache.main([
                    'ensure', '--name', 'cli-sample', '--cwd', str(app),
                    '--package', '.', '--go', GO, '--cache-dir', str(cache),
                ])
            self.assertEqual(rc, 0)
            printed = pathlib.Path(out.getvalue().strip())
            self.assertEqual(printed, cache / 'cli-sample')
            self.assertEqual(subprocess.check_output([str(printed)], text=True), 'dep-v1:embed-v1:td-v1:7f454c4601')


if __name__ == '__main__':
    unittest.main()
