"""Unit tests for migration compatibility harness safety, secret handling, and teardown."""
from __future__ import annotations

import json
import os
import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock

import migration_compatibility as mc


class MigrationCompatibilityTests(unittest.TestCase):
    def _run_exercise(self, *, inline_keys: bool, fail_down_timeout: bool = False):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            prev_source = root / 'previous'
            (prev_source / 'scripts').mkdir(parents=True)
            output = root / 'output'
            output.mkdir()
            stack_dir = root / 'stack'
            stack_dir.mkdir()
            candidate = root / 'candidate'
            candidate.write_text('#!/bin/sh\n')

            events: list[tuple] = []
            keys_file = stack_dir / 'api-keys.json'

            def init_configs():
                if inline_keys:
                    for name in ('config.json', 'worker.json'):
                        (stack_dir / name).write_text(json.dumps({
                            'keys': {'tok-a': {'organization': 'org_a'}}
                        }))
                else:
                    keys_file.write_text(json.dumps({
                        'tok-a': {'organization': 'org_a'}
                    }))
                    for name in ('config.json', 'worker.json'):
                        (stack_dir / name).write_text(json.dumps({'listen': '127.0.0.1:0'}))
                (stack_dir / 'api-startup.log').write_text('api booted\n')
                (stack_dir / 'worker-startup.log').write_text('worker booted\n')

            class FakeStack:
                def __init__(self, name):
                    self.name = name
                    self.directory = stack_dir
                    self.verifying = False

                def up(self):
                    events.append(('up',))
                    init_configs()

                def engine_environment(self, config):
                    return {'QUIVR_CONFIG': str(stack_dir / config), 'QUIVR_API_KEYS_FILE': str(keys_file)}

                def await_ready(self, key):
                    events.append(('await_ready', key))

                def tests(self, pattern):
                    events.append(('tests', pattern))
                    (stack_dir / 'acceptance.log').write_text('PASS\n')

                def stop_processes(self):
                    events.append(('stop_processes',))

                def start_processes(self):
                    events.append(('start_processes',))

                def down(self, reset=False):
                    events.append(('down', reset))
                    if fail_down_timeout:
                        raise RuntimeError('process shutdown timed out; tracked children still alive')

                def compose(self, *args):
                    events.append(('compose', *args))

            fake_local = types.SimpleNamespace(Stack=FakeStack)
            args = types.SimpleNamespace(previous_source=prev_source, candidate=candidate, output=output)

            original_path = list(sys.path)
            try:
                with mock.patch.dict(sys.modules, {'local': fake_local}), \
                        mock.patch.object(mc, 'run', side_effect=lambda cmd, **kw: events.append(('run', cmd))):
                    if fail_down_timeout:
                        with self.assertRaisesRegex(RuntimeError, 'timed out'):
                            mc.exercise(args)
                    else:
                        mc.exercise(args)
            finally:
                sys.path[:] = original_path

            copied_logs = {p.name: p.read_text() for p in output.glob('previous-*-startup.log')}
            if inline_keys:
                orgs = {
                    name: json.loads((stack_dir / name).read_text())['keys']['tok-a']['organization']
                    for name in ('config.json', 'worker.json')
                }
            else:
                orgs = {'shared': json.loads(keys_file.read_text())['tok-a']['organization']}
            return events, copied_logs, orgs

    def test_file_secret_dedup_and_single_teardown(self):
        events, copied_logs, orgs = self._run_exercise(inline_keys=False)
        # Shared QUIVR_API_KEYS_FILE must be rewritten once (not _rollback_rollback).
        self.assertEqual(orgs['shared'], 'org_a_rollback')
        self.assertEqual(copied_logs, {
            'previous-api-startup.log': 'api booted\n',
            'previous-worker-startup.log': 'worker booted\n',
        })
        # Only one stop_processes call (before restart); no redundant stop_processes right before down(reset=True).
        self.assertEqual(events.count(('stop_processes',)), 1)
        self.assertIn(('down', True), events)
        self.assertNotIn(('compose', 'down', '--volumes'), events)

    def test_inline_keys_fallback(self):
        _, _, orgs = self._run_exercise(inline_keys=True)
        self.assertEqual(orgs, {'config.json': 'org_a_rollback', 'worker.json': 'org_a_rollback'})

    def test_teardown_preserves_logs_and_runs_compose_down_when_stop_processes_times_out(self):
        events, copied_logs, _ = self._run_exercise(inline_keys=False, fail_down_timeout=True)
        self.assertEqual(copied_logs, {
            'previous-api-startup.log': 'api booted\n',
            'previous-worker-startup.log': 'worker booted\n',
        })
        self.assertIn(('down', True), events)
        self.assertIn(('compose', 'down', '--volumes'), events)

    def test_pip_constraint_rewritten_when_present_and_popped_when_absent(self):
        for has_constraints in (True, False):
            with self.subTest(has_constraints=has_constraints), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                scratch = root / '.scratch'
                scratch.mkdir()
                captured_envs: list[dict[str, str]] = []

                def fake_run(cmd, **kwargs):
                    if cmd[:2] == ['git', 'archive']:
                        archive_path = pathlib.Path(cmd[cmd.index('-o') + 1])
                        import io
                        import tarfile
                        with tarfile.open(archive_path, 'w') as tar:
                            if has_constraints:
                                data = b'pip==25.0\n'
                                info = tarfile.TarInfo('scripts/ci-constraints.txt')
                                info.size = len(data)
                                tar.addfile(info, io.BytesIO(data))
                    elif '--previous-source' in cmd:
                        captured_envs.append(dict(kwargs['env']))

                with mock.patch.object(mc, 'ROOT', root), \
                        mock.patch.object(mc.subprocess, 'check_output', return_value='deadbeef\n'), \
                        mock.patch.object(mc, 'run', side_effect=fake_run), \
                        mock.patch.dict(os.environ, {'PIP_CONSTRAINT': '/candidate/scripts/ci-constraints.txt'}), \
                        mock.patch.object(sys, 'argv', ['migration_compatibility.py', '--base', 'origin/main']), \
                        mock.patch('builtins.print'):
                    mc.main()

                self.assertEqual(len(captured_envs), 1)
                if has_constraints:
                    self.assertTrue(captured_envs[0]['PIP_CONSTRAINT'].endswith('/previous/scripts/ci-constraints.txt'))
                else:
                    self.assertNotIn('PIP_CONSTRAINT', captured_envs[0])


if __name__ == '__main__':
    unittest.main()
