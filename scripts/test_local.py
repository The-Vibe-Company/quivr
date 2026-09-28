"""Harness isolation, interrupt/failure capture, redaction and reporting (THE-662).

These run without Docker: they exercise the pieces `make verify` relies on to
stay isolated and to explain a failed run. The live proof of stop/migrate/reset
is scripts/lifecycle.py inside `make verify`.
"""
import json, os, pathlib, re, shutil, stat, tempfile, unittest, uuid
from unittest import mock

import local
import verify_report as vr


class Isolation(unittest.TestCase):
    def setUp(self):
        self.names = ['quivr-test-' + uuid.uuid4().hex[:10] for _ in range(2)]

    def tearDown(self):
        for name in self.names:
            shutil.rmtree(local.ROOT / '.scratch' / name, ignore_errors=True)

    def test_each_stack_has_its_own_private_directory_and_secrets(self):
        a, b = (local.Stack(name) for name in self.names)
        self.assertNotEqual(a.directory, b.directory)
        for stack in (a, b):
            self.assertEqual(stat.S_IMODE(stack.directory.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE(stack.statefile.stat().st_mode), 0o600)
        for key in ['password', 'admin', 'cursor_key', 's3_secret', 'credential_key']:
            self.assertNotEqual(a.state[key], b.state[key], key)
            self.assertGreaterEqual(len(a.state[key]), 32, key)
        self.assertNotEqual(a.state['api_port'], b.state['api_port'])

    def test_state_survives_reload_so_down_then_dev_reuses_credentials(self):
        first = local.Stack(self.names[0])
        again = local.Stack(self.names[0])
        self.assertEqual(first.state['admin'], again.state['admin'])

    def test_reset_forgets_data_bound_state_only(self):
        stack = local.Stack(self.names[0])
        stack.state.update(scoped_id='corpus_1', worker_pid=123)
        with mock.patch.object(stack, 'stop_processes'), mock.patch.object(stack, 'compose') as compose:
            stack.down(True)
        compose.assert_called_once_with('down', '--volumes')
        reloaded = local.Stack(self.names[0])
        self.assertNotIn('scoped_id', reloaded.state)
        self.assertEqual(reloaded.state['admin'], stack.state['admin'])

    def test_down_keeps_volumes(self):
        stack = local.Stack(self.names[0])
        with mock.patch.object(stack, 'stop_processes'), mock.patch.object(stack, 'compose') as compose:
            stack.down(False)
        compose.assert_called_once_with('down')

    def test_dependency_start_retries_once_and_records_it(self):
        stack = local.Stack(self.names[0])
        calls = []
        def compose(*args, **kwargs):
            calls.append(args[0])
            if args[0] == 'up' and calls.count('up') == 1:
                raise local.subprocess.CalledProcessError(1, 'compose')
            return mock.Mock(stdout='seaweed\n')
        with mock.patch.object(stack, 'compose', side_effect=compose):
            stack.start_dependencies()
        self.assertEqual(calls.count('up'), 2)
        recorded = json.loads((stack.directory / 'readiness.json').read_text())
        self.assertEqual(recorded['dependency_start_retries'], [{'attempt': 1, 'exited': ['seaweed']}])
        with mock.patch.object(stack, 'compose', side_effect=lambda *a, **k: (_ for _ in ()).throw(local.subprocess.CalledProcessError(1, 'c')) if a[0] == 'up' else mock.Mock(stdout='')):
            with self.assertRaisesRegex(RuntimeError, r'after 1 bounded attempt\(s\) \(exited: none\)'):
                stack.start_dependencies()
        self.assertEqual(len(json.loads((stack.directory / 'readiness.json').read_text())['dependency_start_retries']), 2)  # no crash: not retried

    def test_migrate_refuses_a_stopped_project_with_guidance(self):
        stack = local.Stack(self.names[0])
        with mock.patch.object(stack, 'running', return_value=False):
            with self.assertRaisesRegex(RuntimeError, 'make dev'):
                stack.migrate()

    def test_harness_reads_no_personal_credentials_from_the_environment(self):
        allowed = {'GO', 'CONTRACT_PYTHON', 'QUIVR_PROJECT', 'QUIVR_KEEP_ON_FAILURE'}
        source = (local.ROOT / 'scripts/local.py').read_text()
        read = set(re.findall(r"os\.environ\.get\('([A-Z_]+)'", source))
        self.assertLessEqual(read, allowed)


class Readiness(unittest.TestCase):
    def test_timeout_names_the_probe_and_records_it(self):
        name = 'quivr-test-' + uuid.uuid4().hex[:10]
        self.addCleanup(shutil.rmtree, local.ROOT / '.scratch' / name, True)
        stack = local.Stack(name)
        stack.state['worker_probe_port'] = local.port()  # nothing listens there
        with self.assertRaisesRegex(RuntimeError, r'worker readiness timed out after 0\.2s .*worker-startup\.log'):
            stack.await_ready('worker_probe_port', timeout=0.2)
        recorded = json.loads((stack.directory / 'readiness.json').read_text())
        self.assertFalse(recorded['worker']['ready'])
        self.assertIn('last', recorded['worker'])


class StepsAndReport(unittest.TestCase):
    def test_steps_record_outcomes_and_name_the_failed_step(self):
        steps = vr.Steps()
        steps.run('first', lambda: None)
        with self.assertRaises(ValueError):
            steps.run('second', lambda: (_ for _ in ()).throw(ValueError('boom')))
        self.assertEqual([s['status'] for s in steps.items], ['passed', 'failed'])
        self.assertEqual(steps.failed_step(), 'second')
        self.assertIn('boom', steps.items[1]['error'])
        self.assertIn('seconds', steps.items[1])

    def test_housekeeping_failures_never_become_the_failed_step(self):
        steps = vr.Steps()
        steps.run('journey', lambda: None)
        with self.assertRaises(OSError):
            steps.run('capture_diagnostics', lambda: (_ for _ in ()).throw(OSError('docker gone')))
        self.assertIsNone(steps.failed_step())
        self.assertEqual(steps.items[1]['status'], 'failed')

    def test_interrupt_is_recorded_as_interrupted(self):
        steps = vr.Steps()
        def stop():
            raise KeyboardInterrupt()
        with self.assertRaises(KeyboardInterrupt):
            steps.run('journey', stop)
        self.assertEqual(steps.items[0]['status'], 'interrupted')
        self.assertEqual(steps.failed_step(), 'journey')

    def test_redaction_replaces_every_generated_secret_but_keeps_private_files(self):
        with tempfile.TemporaryDirectory() as d:
            d = pathlib.Path(d)
            state = {'admin': 'a' * 64, 'password': 'p' * 48, 'api_port': 1234, 'scoped_id': 'corpus_' + 'x' * 20}
            (d / 'api.log').write_text(f'{{"key":"{"a" * 64}"}} ok corpus_{"x" * 20}')
            (d / 'config.json').write_text('p' * 48)
            (d / 'nested').mkdir()
            (d / 'nested' / 'worker.log.1').write_text('pw=' + 'p' * 48)
            (d / 'worker.log.3').write_text('pw=' + 'p' * 48)
            changed = vr.redact_tree(d, vr.secrets_of(state))
            self.assertEqual(sorted(changed), ['api.log', 'worker.log.1', 'worker.log.3'])
            self.assertNotIn('a' * 64, (d / 'api.log').read_text())
            self.assertIn('corpus_' + 'x' * 20, (d / 'api.log').read_text())  # identifiers are not secrets
            self.assertEqual((d / 'config.json').read_text(), 'p' * 48)
            self.assertEqual((d / 'nested' / 'worker.log.1').read_text(), 'pw=' + vr.REDACTED)

    def test_markdown_names_failure_platform_and_limits(self):
        report = {'status': 'failed', 'failed_step': 'journey_after_restart', 'source': 'abc', 'dirty': False, 'duration_seconds': 1,
                  'steps': [{'step': 'journey_after_restart', 'status': 'failed', 'seconds': 1, 'error': 'AssertionError: x'}],
                  'timing_overrides': {'delivery': {}}, 'artifacts': '/tmp/x', 'kept_project': None,
                  'pins': {'platform': 'linux/amd64', 'unsupported_platforms': ['macOS'], 'images': ['img@sha256:1'], 'model_revision': 'r',
                           'tokenizer': ['tokenizers==1'], 'toolchain': {'go': 'go1', 'python': '3'}}}
        report['dependency_start_retries'] = [{'attempt': 1, 'exited': ['seaweed']}]
        text = vr.markdown(report)
        for want in ['Failed step: `journey_after_restart`', 'linux/amd64 only', 'macOS', vr.REMAINING_LIMITS, 'AssertionError: x', 'Dependency start retried (attempt 1, exited: seaweed)']:
            self.assertIn(want, text)


class FakeStack:
    def __init__(self, directory):
        self.name, self.directory, self.calls = 'quivr-verify-fake', pathlib.Path(directory), []
        self.state = {'admin': 's' * 64}

    def capture(self):
        self.calls.append('capture')
        (self.directory / 'api.log').write_text('token ' + 's' * 64)

    def down(self, reset=False):
        self.calls.append(('down', reset))


class Finish(unittest.TestCase):
    def run_finish(self, status, keep=False):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        stack, steps = FakeStack(d), vr.Steps()
        with self.assertRaises(KeyboardInterrupt):
            steps.run('journey_worker_stopped', lambda: (_ for _ in ()).throw(KeyboardInterrupt()))
        env = {'QUIVR_KEEP_ON_FAILURE': '1'} if keep else {}
        with mock.patch.dict(os.environ, env):
            local.finish(stack, steps, status, 0)
        return stack, json.loads((stack.directory / 'report.json').read_text()), (stack.directory / 'report.md').read_text()

    def test_interrupt_captures_before_scoped_cleanup_and_reports(self):
        stack, report, md = self.run_finish('interrupted')
        self.assertEqual(stack.calls, ['capture', ('down', True)])
        self.assertEqual(report['status'], 'interrupted')
        self.assertEqual(report['failed_step'], 'journey_worker_stopped')
        self.assertTrue((stack.directory / 'dependency-inventory.json').exists())
        self.assertNotIn('s' * 64, (stack.directory / 'api.log').read_text())
        self.assertIn('journey_worker_stopped', md)

    def test_keep_on_failure_preserves_the_project(self):
        stack, report, md = self.run_finish('failed', keep=True)
        self.assertEqual(stack.calls, ['capture'])
        self.assertEqual(report['kept_project'], stack.name)
        self.assertIn('kept for inspection', md.lower())


if __name__ == '__main__':
    unittest.main()
