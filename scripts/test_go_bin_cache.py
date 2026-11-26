"""Content-addressed caching for local Go engine and plugin binaries (OB-620).

Exercises real `go build` on tiny throwaway modules with a local `replace`
dependency and an embedded file, with zero network access or sleeps.
"""
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


def _write_module(root: pathlib.Path, *, dep_msg: str = 'dep-v1', embed_msg: str = 'embed-v1') -> tuple[pathlib.Path, pathlib.Path]:
    dep = root / 'dep'
    app = root / 'app'
    dep.mkdir(parents=True, exist_ok=True)
    app.mkdir(parents=True, exist_ok=True)
    (dep / 'go.mod').write_text('module example.com/dep\n\ngo 1.21\n')
    (dep / 'dep.go').write_text(f'package dep\n\nfunc Value() string {{ return {json.dumps(dep_msg)} }}\n')
    (app / 'go.mod').write_text(
        'module example.com/app\n\n'
        'go 1.21\n\n'
        'require example.com/dep v0.0.0\n\n'
        'replace example.com/dep => ../dep\n'
    )
    (app / 'data.txt').write_text(embed_msg)
    (app / 'main.go').write_text(
        'package main\n\n'
        'import (\n'
        '\t_ "embed"\n'
        '\t"fmt"\n'
        '\t"example.com/dep"\n'
        ')\n\n'
        '//go:embed data.txt\n'
        'var data string\n\n'
        'func main() { fmt.Printf("%s:%s", dep.Value(), data) }\n'
    )
    return app, dep


class GoBinCache(unittest.TestCase):
    def setUp(self):
        self.env = {**os.environ, 'GOWORK': 'off', 'GOFLAGS': ''}

    def test_caches_binary_and_rebuilds_only_when_module_or_replace_source_changes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            cache = root / 'bin'
            app, dep = _write_module(root)

            binary = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(binary, cache / 'sample')
            self.assertTrue((cache / 'sample.stamp').is_file())
            self.assertEqual(subprocess.check_output([str(binary)], text=True), 'dep-v1:embed-v1')

            # Unchanged inputs hit the cache without spawning `go build`.
            with mock.patch.object(go_bin_cache.subprocess, 'run') as spawned:
                again = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(again, binary)
            spawned.assert_not_called()

            # Editing a test-only file does not invalidate a non-test build.
            (app / 'sample_test.go').write_text('package main\nimport "testing"\nfunc TestNoop(t *testing.T) {}\n')
            with mock.patch.object(go_bin_cache.subprocess, 'run') as spawned:
                go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            spawned.assert_not_called()

            # Editing a local `replace` dependency invalidates and rebuilds.
            (dep / 'dep.go').write_text('package dep\n\nfunc Value() string { return "dep-v2" }\n')
            rebuilt = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(subprocess.check_output([str(rebuilt)], text=True), 'dep-v2:embed-v1')

            # Editing an embedded file invalidates and rebuilds.
            (app / 'data.txt').write_text('embed-v2')
            rebuilt = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertEqual(subprocess.check_output([str(rebuilt)], text=True), 'dep-v2:embed-v2')

            # Changing a relevant build env var changes the fingerprint.
            fp_default = go_bin_cache.fingerprint(app, '.', go=GO, env=self.env)
            fp_trimmed = go_bin_cache.fingerprint(app, '.', go=GO, env={**self.env, 'GOFLAGS': '-trimpath'})
            self.assertNotEqual(fp_default, fp_trimmed)

    def test_materialize_exposes_distinct_path_and_tracks_rebuilds(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            cache = root / 'bin'
            app, dep = _write_module(root)

            cached = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            dest = root / 'stack' / 'sample'
            materialized = go_bin_cache.materialize(cached, dest)
            self.assertEqual(materialized, dest)
            self.assertTrue(os.path.samefile(cached, dest))
            self.assertEqual(subprocess.check_output([str(dest)], text=True), 'dep-v1:embed-v1')

            # Rebuilding replaces the cached inode atomically; rematerializing updates dest.
            (dep / 'dep.go').write_text('package dep\n\nfunc Value() string { return "dep-v2" }\n')
            cached_v2 = go_bin_cache.ensure('sample', app, '.', go=GO, cache_dir=cache, env=self.env)
            self.assertFalse(os.path.samefile(cached_v2, dest))
            go_bin_cache.materialize(cached_v2, dest)
            self.assertTrue(os.path.samefile(cached_v2, dest))
            self.assertEqual(subprocess.check_output([str(dest)], text=True), 'dep-v2:embed-v1')

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
            self.assertEqual(subprocess.check_output([str(built['mod-a'])], text=True), 'a1:embed-v1')
            self.assertEqual(subprocess.check_output([str(built['mod-b'])], text=True), 'b1:embed-v1')

            with mock.patch.object(go_bin_cache, '_build_uncached', wraps=go_bin_cache._build_uncached) as build:
                go_bin_cache.ensure_many(specs)
                self.assertEqual(build.call_count, 0)
                (app_b / 'data.txt').write_text('b-updated')
                updated = go_bin_cache.ensure_many(specs)
                self.assertEqual(build.call_count, 1)
            self.assertEqual(subprocess.check_output([str(updated['mod-b'])], text=True), 'b1:b-updated')

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
            self.assertEqual(subprocess.check_output([str(printed)], text=True), 'dep-v1:embed-v1')


if __name__ == '__main__':
    unittest.main()
