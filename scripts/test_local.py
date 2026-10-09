"""Harness isolation, interrupt/failure capture, redaction and reporting (THE-662).

These run without Docker: they exercise the pieces `make verify` relies on to
stay isolated and to explain a failed run. The live proof of stop/migrate/reset
is scripts/lifecycle.py inside `make verify`.
"""
import http.server, json, os, pathlib, shutil, signal, socket, stat, subprocess, sys, tempfile, threading, time, unittest, uuid
from unittest import mock

import local
import prepare_tokenizer
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

    def test_infrastructure_profile_and_overrides_reach_the_compose_launch(self):
        stack = local.Stack(self.names[0])
        override = stack.directory / 'installation.json'
        override.write_text('{"services":{"weaviate":{"environment":{"GOMEMLIMIT":"3500MiB"},'
                            '"deploy":{"resources":{"limits":{"memory":"4294967296"}}}}}}')
        with mock.patch.dict(os.environ, {'QUIVR_INFRASTRUCTURE_PROFILE': 'large',
                                        'QUIVR_INFRASTRUCTURE_OVERRIDES': str(override)}), \
                mock.patch('local.run') as launched:
            stack.compose('up', '-d')
        model = stack.directory / 'infrastructure.json'
        self.assertIn(str(model), launched.call_args.args[0])
        effective = json.loads(model.read_text())['services']['weaviate']
        self.assertEqual(effective['environment']['ASYNC_INDEXING'], 'true')
        self.assertEqual(effective['environment']['GOMEMLIMIT'], '3500MiB')
        self.assertEqual(effective['deploy']['resources']['limits']['memory'], '4294967296')

    def test_reset_forgets_data_bound_state_only(self):
        stack = local.Stack(self.names[0])
        stack.state.update(scoped_id='corpus_1', worker_pid=123)
        with mock.patch.object(stack, 'stop_processes'), mock.patch.object(stack, 'compose') as compose:
            stack.down(True)
        compose.assert_called_once_with('down', '--volumes')
        reloaded = local.Stack(self.names[0])
        self.assertNotIn('scoped_id', reloaded.state)
        self.assertEqual(reloaded.state['admin'], stack.state['admin'])

    def test_stopped_process_releases_its_probe_before_restart(self):
        # THE-1230: real listeners, stale ownership and longer configured drains.
        for index, (config, environment, elapsed, stale) in enumerate([
            ({}, {'QUIVR_SHUTDOWN_GRACE': ''}, 0, True),
            ({'shutdown_grace': '1m30s'}, {'QUIVR_SHUTDOWN_GRACE': ''}, 71, False),
            ({'shutdown_grace': '1s'}, {'QUIVR_SHUTDOWN_GRACE': '90s'}, 71, False),
        ]):
            with self.subTest(config=config, environment=environment):
                name = self.names[0] + str(index)
                self.names.append(name)
                stack = local.Stack(name)
                executable = stack.directory / 'quivr'
                shutil.copy2(sys.executable, executable)
                (stack.directory / 'worker.json').write_text(json.dumps(config))
                child = subprocess.Popen([str(executable), '-c', """
import signal, socket, sys
listener = socket.socket()
listener.bind(('127.0.0.1', 0))
listener.listen()
def stop(*_):
    sys.stdin.readline()  # The external poll observer releases this drain gate.
    listener.close()
    sys.exit(0)
signal.signal(signal.SIGTERM, stop)
print(listener.getsockname()[1], flush=True)
while True: signal.pause()
"""], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
                self.addCleanup(child.stdout.close)
                self.addCleanup(child.stdin.close)
                self.addCleanup(child.wait, timeout=5)
                self.addCleanup(lambda child=child: child.kill() if child.poll() is None else None)
                probe_port = int(child.stdout.readline())
                # Start through the harness config boundary; the external launch is a real child.
                with mock.patch.dict(os.environ, environment), mock.patch('local.subprocess.Popen', return_value=child):
                    stack.spawn('worker', 'worker.json')
                if stale: stack.state['pids'].append(os.getpid()); stack.save()
                # Reload proves the launch-time grace survives a later cleanup command/environment.
                stack = local.Stack(name)
                released, observed = False, False
                real_alive = local.alive
                def observe_exit(pid):
                    nonlocal observed
                    observed = True
                    return real_alive(pid)
                def finish_drain(_):
                    nonlocal released
                    if observed and not released:
                        child.stdin.write('finish drain\n'); child.stdin.flush()
                        released = True
                clock = iter([0])
                with mock.patch('local.time.monotonic', side_effect=lambda: next(clock, 71 if stale and child.poll() is not None else elapsed)), mock.patch('local.time.sleep', side_effect=finish_drain), mock.patch('local.alive', side_effect=observe_exit):
                    stack.stop_processes()
                with socket.socket() as replacement:
                    replacement.bind(('127.0.0.1', probe_port))
                self.assertEqual(json.loads(stack.statefile.read_text())['pids'], [])
                self.assertTrue(local.alive(os.getpid()))

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

    def test_exited_process_fails_the_wait_at_once_with_its_last_log_lines(self):
        # THE-1138: a worker that could not bind its probe, still held by the draining one, exited in 0.5 s
        # and the wait polled for 20 s. The previous process answering 204 must not stand in for it, nor
        # may a second worker launched behind another probe.
        name = 'quivr-test-' + uuid.uuid4().hex[:10]
        self.addCleanup(shutil.rmtree, local.ROOT / '.scratch' / name, True)
        stack = local.Stack(name)
        previous = http.server.HTTPServer(('127.0.0.1', 0), type('Ready', (http.server.BaseHTTPRequestHandler,), {
            'do_GET': lambda self: (self.send_response(204), self.end_headers()), 'log_message': lambda *_: None}))
        self.addCleanup(previous.server_close)
        threading.Thread(target=previous.serve_forever, kwargs={'poll_interval': 0.01}, daemon=True).start()
        self.addCleanup(previous.shutdown)
        stack.state['worker_probe_port'] = previous.server_address[1]
        for config, port in [('worker.json', stack.state['worker_probe_port']), ('queue-bulk.json', local.port())]:
            (stack.directory / config).write_text(json.dumps({'probe_listen': f'127.0.0.1:{port}'}))
        with (stack.directory / 'worker-startup.log').open('a') as log:
            failed = subprocess.Popen([sys.executable, '-c', 'import sys; print("process failed: listen failed"); sys.exit(1)'], stdout=log)
        failed.wait(timeout=5)
        running = subprocess.Popen([sys.executable, '-c', 'import sys; sys.stdin.read()'], stdin=subprocess.PIPE)
        self.addCleanup(running.wait, timeout=5)
        self.addCleanup(running.stdin.close)
        for child, config in [(failed, 'worker.json'), (running, 'queue-bulk.json')]:
            with mock.patch('local.subprocess.Popen', return_value=child):
                stack.spawn('worker', config)
        # Synthetic time: the 20 s budget passes on the third reading, so only an exit check fails sooner.
        clock = iter([0, 0])
        with mock.patch('local.time.monotonic', side_effect=lambda: next(clock, 21)), mock.patch('local.time.sleep'):
            with self.assertRaisesRegex(RuntimeError, r'(?s)worker exited with status 1 before it was ready .*worker-startup\.log.*process failed: listen failed'):
                stack.await_ready('worker_probe_port')
        recorded = json.loads((stack.directory / 'readiness.json').read_text())
        self.assertEqual(recorded['worker'], {'ready': False, 'exited': 1, 'waited_seconds': 0})

    def test_verification_refuses_a_disk_near_weaviates_read_only_threshold(self):
        # THE-758: past 90% Weaviate turns read-only and Records stop becoming searchable.
        name = 'quivr-test-' + uuid.uuid4().hex[:10]
        self.addCleanup(shutil.rmtree, local.ROOT / '.scratch' / name, True)
        stack = local.Stack(name)
        with mock.patch.object(local, 'docker_disk', return_value=('/var/lib/docker', 89.9)):
            stack.verifying = True
            with self.assertRaisesRegex(RuntimeError, r'\(/var/lib/docker\) is 89\.9% full; Weaviate turns read-only at 90%'):
                stack.check_disk()
            stack.verifying = False
            with mock.patch('builtins.print') as warned:
                stack.check_disk()
            self.assertIn('89.9% full', warned.call_args.args[0])
        with mock.patch.object(local, 'docker_disk', return_value=('/var/lib/docker', 60.0)):
            stack.verifying = True
            stack.check_disk()
        self.assertEqual(json.loads((stack.directory / 'readiness.json').read_text())['docker_disk_used_percent'], 60.0)


class Platforms(unittest.TestCase):
    """make dev runs on Linux x86_64 and macOS arm64 (THE-808); anything else stops before building or pulling."""

    def test_other_hosts_fail_fast_naming_the_supported_platforms(self):
        for host in [('Linux', 'aarch64'), ('Darwin', 'x86_64'), ('Windows', 'AMD64')]:
            with self.subTest(host=host), self.assertRaisesRegex(RuntimeError, rf'Linux x86_64 and on macOS with Apple Silicon \(arm64\); this machine is {host[0]} {host[1]}'):
                prepare_tokenizer.requirements(host)

    def test_every_supported_host_pins_the_profiles_tokenizers_by_hash(self):
        version = prepare_tokenizer.PROFILE['implementation_version']
        for host in prepare_tokenizer.SUPPORTED:
            with self.subTest(host=host):
                pins = [line for line in prepare_tokenizer.requirements(host).read_text().splitlines() if not line.startswith('#')]
                self.assertEqual(len(pins), 1, pins)
                self.assertRegex(pins[0], rf'^tokenizers=={version} --hash=sha256:[0-9a-f]{{64}}$')

    def test_a_killed_child_is_not_alive_before_it_is_reaped(self):
        # The harness never reaps the processes it kills; /proc on Linux and ps on macOS must both see the zombie as gone.
        child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'])
        self.addCleanup(child.wait)
        self.addCleanup(child.kill)
        self.assertTrue(local.alive(child.pid))
        os.kill(child.pid, signal.SIGKILL)
        deadline = time.monotonic() + 5
        while local.alive(child.pid) and time.monotonic() < deadline:
            time.sleep(.01)
        self.assertFalse(local.alive(child.pid), f'pid {child.pid} still reported alive after SIGKILL')


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


class LifecycleRouting(unittest.TestCase):
    """Exercise the plugins lane against real Git changes, including uncertain CI bases."""

    def test_only_a_known_unchanged_harness_omits_lifecycle(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            def git(*args):
                return subprocess.run(['git', *args], cwd=root, check=True, capture_output=True, text=True).stdout.strip()
            git('init')
            git('config', 'user.email', 'test@example.org')
            git('config', 'user.name', 'Test')
            (root / 'scripts').mkdir()
            harness = root / 'scripts/local.py'
            harness.write_text('initial\n')
            git('add', 'scripts/local.py')
            git('commit', '-m', 'initial')
            base = git('rev-parse', 'HEAD')
            (root / 'other.txt').write_text('unrelated\n')
            git('add', 'other.txt')
            git('commit', '-m', 'unrelated')
            def selected(base):
                names = []
                steps = mock.Mock()
                steps.run.side_effect = lambda name, *_: names.append(name)
                with mock.patch.object(local, 'ROOT', root), mock.patch.dict(os.environ, {'QUIVR_VERIFY_BASE': base}):
                    local.verify(mock.Mock(), steps, 'plugins')
                return 'lifecycle' in names
            self.assertFalse(selected(base), 'unrelated edits must omit the expensive lifecycle proof')
            for uncertain in ['', '0' * 40, 'missing-base']:
                with self.subTest(base=uncertain):
                    self.assertTrue(selected(uncertain), 'uncertain metadata must retain lifecycle')
            harness.write_text('changed\n')
            git('add', 'scripts/local.py')
            git('commit', '-m', 'change harness')
            self.assertTrue(selected(base), 'a harness change must execute lifecycle')


class FailureDrill(unittest.TestCase):
    def test_samples_and_correlated_log_lines(self):
        import failure_drill as fd
        metrics = 'quivr_ingestion_pending 2\nquivr_commands_accepted_total{command="record"} 5\nquivr_x_bucket{le="1"} 0\n'
        self.assertEqual(fd.sample(metrics, 'quivr_ingestion_pending'), 2)
        self.assertEqual(fd.sample(metrics, 'quivr_commands_accepted_total', '{command="record"}'), 5)
        self.assertEqual(fd.sample(metrics, 'quivr_x_bucket', '{le="1"}'), 0)
        self.assertIsNone(fd.sample(metrics, 'quivr_missing'))
        with tempfile.TemporaryDirectory() as d:
            log = pathlib.Path(d) / 'api.log'
            log.write_text('\n'.join([json.dumps({'msg': 'command accepted', 'receipt_id': 'r1', 'request_id': 'q1'}),
                                      'not json', json.dumps({'msg': 'http request', 'request_id': 'q1'}),
                                      json.dumps({'msg': 'command accepted', 'receipt_id': 'r2', 'request_id': 'q2'})]))
            (pathlib.Path(d) / 'api.log.1').write_text(json.dumps({'msg': 'command accepted', 'receipt_id': 'r1', 'request_id': 'q0'}))
            self.assertEqual(sorted(e['request_id'] for e in fd._lines(log, 'r1', 'command accepted')), ['q0', 'q1'])
            self.assertEqual(fd._lines(pathlib.Path(d) / 'absent.log', 'r1', 'command accepted'), [])


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
        with mock.patch.dict(os.environ, env), mock.patch('builtins.print'):  # keep make check output readable
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
