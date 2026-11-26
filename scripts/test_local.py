"""Harness isolation, interrupt/failure capture, redaction and reporting (THE-662).

These run without Docker: they exercise the pieces `make verify` relies on to
stay isolated and to explain a failed run. The live proof of stop/migrate/reset
is scripts/lifecycle.py inside `make verify`.
"""
import base64
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

    def test_generated_configs_bind_credentials_without_storing_them(self):
        stack = local.Stack(self.names[0])
        local.connector_plugin.select_first_party(stack, [])
        with mock.patch.object(local, 'MACOS', True), \
                mock.patch.object(stack, 'compose', return_value=mock.Mock(stdout='127.0.0.1:43001')):
            stack.config()
        for name, token_name in (('config.json', 'admin'), ('worker.json', 'admin'),
                                 ('short-retention.json', 'admin'), ('keyless.json', 'keyless'),
                                 ('keyless-worker.json', 'keyless')):
            with self.subTest(config=name):
                path = stack.directory / name
                raw = path.read_text()
                for key in ('password', 'cursor_key', 'credential_key', 's3_access', 's3_secret', 'admin', 'keyless'):
                    self.assertNotIn(stack.state[key], raw)
                self.assertNotIn(local.CAPTURE_SECRET, raw)
                bindings = stack.engine_environment(name)
                keys = pathlib.Path(bindings['QUIVR_API_KEYS_FILE'])
                self.assertEqual(stat.S_IMODE(keys.stat().st_mode), 0o600)
                self.assertIn(stack.state[token_name], json.loads(keys.read_text()))
                expected = {
                    'DATABASE_URL': f"postgres://quivr:{stack.state['password']}@127.0.0.1:43001/quivr?sslmode=disable",
                    'QUIVR_CURSOR_KEY': stack.state['cursor_key'],
                    'S3_ACCESS_KEY': stack.state['s3_access'],
                    'S3_SECRET_KEY': stack.state['s3_secret'],
                }
                if token_name == 'admin':
                    expected['QUIVR_CREDENTIAL_KEY'] = stack.state['credential_key']
                else:
                    self.assertNotIn('QUIVR_CREDENTIAL_KEY_FILE', bindings)
                destinations = json.loads(raw)['destinations']
                for suffix in ('a', 'b', 'k'):
                    key = destinations[f'local-receiver-org-{suffix}']['secret_env']
                    expected[key] = 'whsec_' + base64.b64encode(f'local-test-signing-secret-org-{suffix}!'.encode()).decode()
                expected[destinations[local.CAPTURE_DESTINATION]['secret_env']] = local.CAPTURE_SECRET
                guide_secret = 'whsec_' + base64.b64encode(b'local-test-signing-secret-guides').decode()
                for destination in destinations.values():
                    self.assertNotIn('secret', destination)
                    expected.setdefault(destination['secret_env'], guide_secret)
                for variable, value in expected.items():
                    self.assertEqual(pathlib.Path(bindings[variable + '_FILE']).read_text(), value, variable)

    def test_keyless_environment_discards_credentials_before_files_exist(self):
        stack = local.Stack(self.names[0])
        for name in ('keyless.json', 'keyless-worker.json'):
            with self.subTest(config=name), mock.patch.dict(os.environ, {
                    'QUIVR_CREDENTIAL_KEY': 'fixture-inline-key',
                    'QUIVR_CREDENTIAL_KEY_FILE': '/fixture/credential-key'}):
                bindings = stack.engine_environment(name)
                self.assertNotIn('QUIVR_CREDENTIAL_KEY', bindings)
                self.assertNotIn('QUIVR_CREDENTIAL_KEY_FILE', bindings)

    def test_migrate_launch_can_sign_calls_to_the_local_archive_plugin(self):
        stack = local.Stack(self.names[0])
        local.connector_plugin.select_first_party(stack, ['object-storage-archive'])
        with mock.patch.object(local, 'MACOS', True), \
                mock.patch.object(stack, 'compose', return_value=mock.Mock(stdout='127.0.0.1:43001')), \
                mock.patch.object(local, 'run') as launch:
            stack.migrate()
        self.assertEqual(launch.call_args.args[0][-1], 'migrate')
        env = launch.call_args.kwargs['env']
        self.assertEqual(env['QUIVR_CONFIG'], str(stack.directory / 'config.json'))
        self.assertIn(stack.state['password'], pathlib.Path(env['DATABASE_URL_FILE']).read_text())
        archive, = local.connector_plugin.first_party(stack)
        ring = json.loads(env['QUIVR_ENGINE_PLUGIN_KEYS'])['connector.object_storage_archive']
        self.assertEqual(ring, local.connector_plugin.signing_ring(stack, archive))

    def test_first_party_build_and_launch_filter_engine_secret_bindings(self):
        stack = local.Stack(self.names[0])
        plugins = local.connector_plugin
        plugins.select_first_party(stack, ['object-storage-archive'])
        archive, = plugins.first_party(stack)
        engine_secrets = {
            'QUIVR_ENGINE_PLUGIN_KEYS_FILE': '/fixture/engine-keys',
            'QUIVR_ENGINE_PLUGIN_KEYS': 'fixture-engine-rings',
            'DATABASE_URL_FILE': '/fixture/database-url',
            'QUIVR_API_KEYS_FILE': '/fixture/api-keys',
        }
        with mock.patch.dict(os.environ, {**engine_secrets, 'PATH': '/fixture/bin'}, clear=True), \
                mock.patch.object(plugins.ports, 'allocate', return_value=41001), \
                mock.patch.object(plugins.go_bin_cache, 'ensure_many', return_value={'quivr-object-storage-archive': pathlib.Path('/bin/true')}) as build, \
                mock.patch.object(plugins.subprocess, 'Popen', return_value=mock.Mock(pid=12345)) as launch, \
                mock.patch.object(plugins, 'healthy_port', return_value=True):
            plugins.start_first_party(stack)

        build_env = build.call_args.args[0][0]['env']
        launch_env = launch.call_args.kwargs['env']
        for env in (build_env, launch_env):
            self.assertTrue(engine_secrets.keys().isdisjoint(env))
            self.assertEqual(env['PATH'], '/fixture/bin')
        self.assertNotIn('QUIVR_PLUGIN_SIGNING_KEYS', build_env)
        self.assertEqual(json.loads(launch_env['QUIVR_PLUGIN_SIGNING_KEYS']), plugins.signing_ring(stack, archive))

    def test_plugin_go_builds_filter_engine_secrets(self):
        import hosted_embed_plugin

        stack = local.Stack(self.names[0])
        engine_secrets = {
            'DATABASE_URL': 'postgres://secret',
            'QUIVR_ADMIN_KEYS': 'admin-ring',
            'QUIVR_ADMIN_KEY': 'admin-token',
            'QUIVR_DEMO_ADMIN_KEY': 'demo-admin',
            'QUIVR_ENGINE_PLUGIN_KEYS': 'engine-rings',
        }
        sentinel = RuntimeError('stop-after-build')
        with mock.patch.dict(os.environ, {**engine_secrets, 'PATH': '/fixture/bin'}, clear=True):
            with mock.patch.object(local.fixture_plugin.go_bin_cache, 'ensure', return_value=pathlib.Path('/bin/true')) as ensure:
                local.fixture_plugin.binary(stack)
                self.assertEqual(ensure.call_args.kwargs['env'], {'PATH': '/fixture/bin'})
            with mock.patch.object(local.ingestion_plugin.go_bin_cache, 'ensure', return_value=pathlib.Path('/bin/true')) as ensure:
                local.ingestion_plugin.hash_embedder_binary(stack.source)
                self.assertEqual(ensure.call_args.kwargs['env'], {'PATH': '/fixture/bin'})
            with mock.patch.object(local.connector_plugin.go_bin_cache, 'ensure', return_value=pathlib.Path('/bin/true')) as ensure:
                local.connector_plugin.binary(stack)
                self.assertEqual(ensure.call_args.kwargs['env'], {'PATH': '/fixture/bin'})
            with mock.patch.object(hosted_embed_plugin.go_bin_cache, 'ensure', return_value=pathlib.Path('/bin/true')) as ensure, \
                    mock.patch.object(hosted_embed_plugin.go_bin_cache, 'materialize', return_value=stack.directory / 'hosted-embed'):
                hosted_embed_plugin.build(stack.directory)
                self.assertEqual(ensure.call_args.kwargs['env'], {'PATH': '/fixture/bin'})
            with mock.patch.object(local.retrieval_plugin.go_bin_cache, 'ensure', return_value=pathlib.Path('/bin/true')) as ensure, \
                    mock.patch.object(local.retrieval_plugin.ports, 'allocate', side_effect=sentinel):
                with self.assertRaises(RuntimeError) as caught:
                    local.retrieval_plugin.verify(stack)
                self.assertIs(caught.exception, sentinel)
                self.assertEqual(ensure.call_count, 2)
                for call in ensure.call_args_list:
                    self.assertEqual(call.kwargs['env'], {'PATH': '/fixture/bin'})
            with mock.patch.object(local.normalizer_plugin.go_bin_cache, 'ensure', return_value=pathlib.Path('/bin/true')) as ensure, \
                    mock.patch.object(local.normalizer_plugin, 'stack_port', side_effect=sentinel):
                with self.assertRaises(RuntimeError) as caught:
                    local.normalizer_plugin.failures(stack)
                self.assertIs(caught.exception, sentinel)
                self.assertEqual(ensure.call_args.kwargs['env'], {'PATH': '/fixture/bin'})

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
                                        'QUIVR_INFRASTRUCTURE_OVERRIDES': str(override)}, clear=True), \
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
        # Outage restart of an individual dependency uses the same bounded crash-retry and --wait path.
        targeted = []
        def compose_targeted(*args, **kwargs):
            targeted.append(args)
            if args[0] == 'up' and sum(1 for item in targeted if item[0] == 'up') == 1:
                raise local.subprocess.CalledProcessError(1, 'compose')
            return mock.Mock(stdout='seaweed\n')
        with mock.patch.object(stack, 'compose', side_effect=compose_targeted):
            stack.start_dependencies('seaweed', wait_timeout=90)
        self.assertEqual([args for args in targeted if args[0] == 'up'], [
            ('up', '-d', '--wait', '--wait-timeout', '90', 'seaweed'),
            ('up', '-d', '--wait', '--wait-timeout', '90', 'seaweed'),
        ])

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


class DevProfile(unittest.TestCase):
    """Fast dev profile skips TEI/model and optional sidecars without affecting verify (OB-620)."""

    def setUp(self):
        self.name = 'quivr-test-' + uuid.uuid4().hex[:10]

    def tearDown(self):
        shutil.rmtree(local.ROOT / '.scratch' / self.name, ignore_errors=True)

    def test_fast_profile_skips_tei_and_optional_sidecars_in_config_and_up(self):
        stack = local.Stack(self.name)
        stack.state.update(dev_profile='fast', push_plugin=False, fixture_plugin=False)
        stack.save()
        local.normalizer_plugin.select(stack, 'none')
        local.subscription_plugin.select(stack, False, 'off', template=False)
        local.connector_plugin.select(stack, False)
        local.connector_plugin.select_first_party(stack, ['core-retrieve'])

        self.assertEqual(stack.dev_profile, 'fast')
        self.assertFalse(stack.uses_tei())
        self.assertTrue(stack.uses_fast_ingestion())
        self.assertFalse(stack.use_push_plugin())
        self.assertFalse(stack.use_fixture_plugin())
        self.assertFalse(stack.needs_fake_graph())

        ports_map = {
            ('port', 'postgres', '5432'): '127.0.0.1:15432\n',
            ('port', 'weaviate', '8080'): '127.0.0.1:18080\n',
            ('port', 'temporal', '7233'): '127.0.0.1:17233\n',
            ('port', 'seaweed', '8333'): '127.0.0.1:18333\n',
        }
        compose_calls = []
        def fake_compose(*args, **kwargs):
            compose_calls.append(args)
            if args in ports_map:
                return mock.Mock(stdout=ports_map[args])
            raise AssertionError(f'unexpected compose call in fast mode: {args}')

        with mock.patch.object(stack, 'compose', side_effect=fake_compose):
            stack.config()

        cfg = json.loads((stack.directory / 'config.json').read_text())
        self.assertEqual(cfg['tei_url'], '')
        self.assertEqual(cfg['ingestion'], {'default': 'example.hash_embedder'})
        self.assertIsNone(cfg['plugin'])
        manifests = [p['manifest'] for p in cfg['plugins']]
        self.assertEqual(len(manifests), 2)
        self.assertTrue(any(m.endswith('/plugins/core-retrieve/quivr-plugin.yaml') for m in manifests))
        hash_pin = next(p for p in cfg['plugins'] if p['manifest'].endswith('/sdks/go/examples/hash-embedder/quivr-plugin.yaml'))
        self.assertEqual(hash_pin['spaces'], {'example.hash_embedder.small': 'served'})
        self.assertFalse((stack.directory / 'tokenizer-provenance.json').exists())

        # start_dependencies stops and removes tei (in case an existing full stack switched to fast)
        # before starting only core services, and scopes exited-service retry checks to CORE_SERVICES.
        with mock.patch.object(stack, 'compose') as compose:
            stack.start_dependencies()
        self.assertEqual(compose.call_args_list, [
            mock.call('rm', '-s', '-f', 'tei'),
            mock.call('up', '-d', '--wait', '--wait-timeout', '180', *local.CORE_SERVICES),
        ])
        ps_calls = []
        def fail_up(*args, **kwargs):
            if args[0] == 'up':
                raise local.subprocess.CalledProcessError(1, 'compose')
            if args[0] == 'ps':
                ps_calls.append(args)
                return mock.Mock(stdout='')
            return mock.Mock(stdout='')
        with mock.patch.object(stack, 'compose', side_effect=fail_up):
            with self.assertRaisesRegex(RuntimeError, r'after 1 bounded attempt\(s\) \(exited: none\)'):
                stack.start_dependencies()
        self.assertEqual(ps_calls, [('ps', '--all', '--status', 'exited', '--format', '{{.Service}}', *local.CORE_SERVICES)])

        # up() skips prepare_tokenizer/prepare_embeddings, fixture/push start, and double migrate.
        with mock.patch.object(stack, 'check_disk'), \
                mock.patch.object(stack, 'stop_processes'), \
                mock.patch.object(local, 'prepare_tokenizer') as tok, \
                mock.patch.object(local, 'prepare_embeddings') as emb, \
                mock.patch.object(stack, 'build_quivr') as build_quivr, \
                mock.patch.object(stack, 'start_dependencies') as deps, \
                mock.patch.object(local.normalizer_plugin, 'prepare'), \
                mock.patch.object(local.normalizer_plugin, 'start'), \
                mock.patch.object(local.subscription_plugin, 'prepare'), \
                mock.patch.object(local.subscription_plugin, 'start'), \
                mock.patch.object(local.push_plugin, 'start') as push_start, \
                mock.patch.object(local.push_plugin, 'stop') as push_stop, \
                mock.patch.object(local.connector_plugin, 'start_first_party'), \
                mock.patch.object(local.ingestion_plugin, 'start_fast') as start_fast, \
                mock.patch.object(local.fixture_plugin, 'prepare') as fix_prep, \
                mock.patch.object(local.fixture_plugin, 'start') as fix_start, \
                mock.patch.object(local.fixture_plugin, 'stop') as fix_stop, \
                mock.patch.object(stack, 'migrate') as migrate, \
                mock.patch.object(stack, 'start_processes'):
            stack.up()

        tok.assert_not_called()
        emb.assert_not_called()
        build_quivr.assert_called_once()
        deps.assert_called_once()
        migrate.assert_called_once()
        push_start.assert_not_called()
        push_stop.assert_called_once_with(stack)
        fix_prep.assert_not_called()
        fix_start.assert_not_called()
        fix_stop.assert_called_once_with(stack)
        start_fast.assert_called_once_with(stack)

    def test_fast_profile_defaults_and_opt_in_overrides(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(local.dev_profile_from_environment(), 'full')
            self.assertEqual(local.connector_plugin.from_environment(fast=True), ['core-retrieve'])
            self.assertEqual(local.normalizer_plugin.from_environment(default='none'), 'none')
            self.assertFalse(local.subscription_plugin.from_environment(default='off'))

        with mock.patch.dict(os.environ, {'QUIVR_DEV_PROFILE': 'fast', 'QUIVR_RSS': 'on', 'QUIVR_NORMALIZER': 'pdf-text', 'QUIVR_ALERTS': 'on'}, clear=True):
            self.assertEqual(local.dev_profile_from_environment(), 'fast')
            self.assertEqual(local.connector_plugin.from_environment(fast=True), ['rss', 'core-retrieve'])
            self.assertEqual(local.normalizer_plugin.from_environment(default='none'), 'pdf-text')
            self.assertTrue(local.subscription_plugin.from_environment(default='off'))

        with mock.patch.dict(os.environ, {'QUIVR_CORE_INGEST': 'on'}, clear=True):
            with self.assertRaisesRegex(ValueError, 'QUIVR_CORE_INGEST=on requires TEI'):
                local.connector_plugin.from_environment(fast=True)

        with mock.patch.dict(os.environ, {'QUIVR_CORE_INGEST': 'onn'}, clear=True):
            with self.assertRaisesRegex(ValueError, "QUIVR_CORE_INGEST is on or off, not 'onn'"):
                local.connector_plugin.from_environment(fast=True)

        with mock.patch.dict(os.environ, {'QUIVR_DEV_PROFILE': 'turbo'}, clear=True):
            with self.assertRaisesRegex(ValueError, 'QUIVR_DEV_PROFILE'):
                local.dev_profile_from_environment()

    def test_stop_fast_refuses_to_signal_an_unrelated_reused_pid(self):
        stack = local.Stack(self.name)
        stack.state['fast_ingestion_pid'] = os.getpid()
        stack.state['fast_ingestion_binary'] = str(local.ROOT / '.scratch' / 'bin' / 'hash-embedder')
        stack.save()
        stderr = __import__('io').StringIO()
        with mock.patch.object(local.ingestion_plugin.os, 'killpg') as killpg, \
                mock.patch('sys.stderr', stderr):
            local.ingestion_plugin.stop_fast(stack)
        killpg.assert_not_called()
        self.assertIn(str(os.getpid()), stderr.getvalue())
        self.assertNotIn('fast_ingestion_pid', json.loads(stack.statefile.read_text()))

    def test_start_first_party_persists_each_pid_before_starting_the_next_plugin(self):
        stack = local.Stack(self.name)
        local.connector_plugin.select_first_party(stack, ['rss', 'core-retrieve'])
        calls = 0
        def fake_popen(*args, **kwargs):
            nonlocal calls
            calls += 1
            if calls == 1:
                return mock.Mock(pid=424242)
            raise OSError('spawn failed')
        binaries = {'quivr-rss': pathlib.Path('/bin/true'), 'quivr-core-retrieve': pathlib.Path('/bin/true')}
        with mock.patch.object(local.connector_plugin, 'stop_first_party'), \
                mock.patch.object(local.connector_plugin.ports, 'allocate', side_effect=[41001, 41002]), \
                mock.patch.object(local.connector_plugin.go_bin_cache, 'ensure_many', return_value=binaries), \
                mock.patch.object(local.connector_plugin.subprocess, 'Popen', side_effect=fake_popen):
            with self.assertRaisesRegex(OSError, 'spawn failed'):
                local.connector_plugin.start_first_party(stack)
        saved = json.loads(stack.statefile.read_text())
        self.assertEqual(saved.get('rss_plugin_pid'), 424242)

    def test_verify_ignores_fast_dev_profile_and_retains_full_stack(self):
        created = []
        real_stack = local.Stack
        def capture_stack(name, *args, **kwargs):
            s = real_stack(self.name, *args, **kwargs)
            created.append(s)
            return s

        with mock.patch.dict(os.environ, {'QUIVR_DEV_PROFILE': 'fast'}, clear=False), \
                mock.patch.object(real_stack, 'up') as up, \
                mock.patch.object(local, 'Stack', side_effect=capture_stack), \
                mock.patch.object(local, 'verify') as run_verify, \
                mock.patch.object(local, 'finish'):
            local.run_stack('verify', part='core')

        up.assert_called_once()
        run_verify.assert_called_once()
        [stack] = created
        self.assertTrue(stack.verifying)
        self.assertEqual(stack.dev_profile, 'full')
        self.assertTrue(stack.uses_tei())
        self.assertFalse(stack.uses_fast_ingestion())
        self.assertTrue(stack.use_push_plugin())
        self.assertTrue(stack.use_fixture_plugin())
        self.assertTrue(stack.state['alerts_plugin'])
        self.assertTrue(stack.state['subscription_template'])
        self.assertEqual(stack.state['normalizer'], 'template')
        self.assertEqual(stack.state['first_party'], [r['id'] for r in local.connector_plugin.FIRST_PARTY])

    def test_main_rejects_profile_on_verify_and_skips_tokenizer_requirements_for_fast_dev(self):
        with mock.patch.object(sys, 'argv', ['local.py', 'verify', '--profile', 'fast']), \
                mock.patch('sys.stderr', new_callable=__import__('io').StringIO):
            with self.assertRaises(SystemExit) as caught:
                local.main()
            self.assertEqual(caught.exception.code, 2)

        with mock.patch.object(sys, 'argv', ['local.py', 'dev', '--profile', 'fast']), \
                mock.patch.object(local, 'requirements') as reqs, \
                mock.patch.object(local, 'run_stack') as run_stack:
            local.main()
        reqs.assert_not_called()
        run_stack.assert_called_once_with('dev', profile='fast')


if __name__ == '__main__':
    unittest.main()
