"""Owner tests for declarative input safety and evidence classification; no live stack."""
import json
import io
from contextlib import redirect_stdout, redirect_stderr, nullcontext
import pathlib
import tempfile
import types
import sys
import unittest
from unittest.mock import patch, Mock

from conformance import runner
from conformance.checks import CHECKS, Context, Observation, Skip


class ConformanceTests(unittest.TestCase):
    def test_case_acceptance_and_rejection(self):
        # A typo or executable YAML must fail before any target starts. No previous owner exists.
        base = 'id: EX-001\ncheck: http_probe\nparameters: {path: /healthz, target: probe}\nthreshold: {status: 204, max_latency_ms: 100}\n'
        with tempfile.TemporaryDirectory() as directory:
            suite = pathlib.Path(directory)
            path = suite / 'case.yaml'
            for content, valid in [
                (base, True),
                (base.replace('status: 204', 'status: "204"'), False),
                (base.replace('/healthz', '//example.com'), False),
                (base + 'command: echo unsafe\n', False),
                (base + 'id: OTHER\n', False),
                ('!!python/object/apply:os.system [echo unsafe]', False),
                (base.replace('max_latency_ms: 100', 'max_latency_ms: .nan'), False),
                (base.replace('threshold: {status: 204, max_latency_ms: 100}', 'threshold: {}'), False),
                ('id: EX-001\ncheck: future_check\nparameters: {}\nthreshold: {}\n', False),
                ('id: EX-001\ncheck: future_check\nparameters: {}\nthreshold: {}\nmaintainer_ticket: https://example.com/issues/1\n', True),
            ]:
                with self.subTest(content=content):
                    path.write_text(content)
                    if valid:
                        self.assertEqual(len(runner.load_cases(suite)), 1)
                    else:
                        with self.assertRaises(ValueError):
                            runner.load_cases(suite)
            path.write_text(base)
            (suite / 'duplicate.yaml').write_text(base)
            with self.assertRaisesRegex(ValueError, 'duplicate requirement'):
                runner.load_cases(suite)

    def test_explicit_suite_selection_accepts_both_cli_spellings(self):
        # Argparse accepts --suite=name too; silently validating all suites is false success.
        for arguments in [['--suite', 'does-not-exist'], ['--suite=does-not-exist']]:
            with self.subTest(arguments=arguments), redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
                self.assertEqual(runner.main(['--validate', *arguments]), 2)

    def test_report_preserves_every_outcome_and_evidence(self):
        # Exceptions/skips must not stop later requirements or disappear from either report.
        def failed(context, parameters, threshold):
            raise OSError('target unreachable')
        def skipped(context, parameters, threshold):
            raise Skip('no image selected')
        checks = {
            'pass': lambda *args: Observation(True, 3, {'sha256': 'abc'}),
            'fail': lambda *args: Observation(False, 9, {'sha256': 'def'}),
            'error': failed, 'skip': skipped,
        }
        cases = [(pathlib.Path(f'{kind}.yaml'), {'id': kind, 'check': kind,
                  'parameters': {}, 'threshold': {'max': 5}})
                 for kind in ['pass', 'fail', 'error', 'skip', 'unknown']]
        with patch.dict(CHECKS, checks):
            results = runner.run_cases(cases, Context())
        self.assertEqual([r['status'] for r in results], ['met', 'not met', 'error', 'skipped', 'skipped'])
        self.assertEqual(results[-1]['reason'], 'needs check type unknown')
        with tempfile.TemporaryDirectory() as directory:
            runner.write_report(pathlib.Path(directory), {'results': results, 'version': 'abc',
                                'harness_working_tree_dirty': True, 'target': {'binary_sha256': '123'}})
            report = json.loads((pathlib.Path(directory) / 'report.json').read_text())
            markdown = (pathlib.Path(directory) / 'report.md').read_text()
            for result in report['results']:
                self.assertIn(result['case_file'], markdown)
                self.assertIn(result['status'], markdown)
                for field in ['measurement', 'threshold', 'evidence', 'reason']:
                    self.assertIn(field, result)
            self.assertIn('sha256', markdown)
            self.assertIn('needs check type unknown', markdown)
            self.assertIn('harness_working_tree_dirty', markdown)
            self.assertIn('binary_sha256', markdown)

    def test_setup_failure_retains_ticket_and_distinguishes_harness_provenance(self):
        # A failure before checks start must still explain every case, including future checks.
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            suite = root / 'conformance/suites/example'
            suite.mkdir(parents=True)
            schema = root / 'conformance/schema'
            schema.mkdir()
            (schema / 'case.schema.json').write_bytes((runner.ROOT / 'conformance/schema/case.schema.json').read_bytes())
            (suite / 'future.yaml').write_text('id: EX-001\ncheck: future_check\nparameters: {}\nthreshold: {}\nmaintainer_ticket: https://example.com/issues/1\n')
            def git(*args):
                return ' M scripts/local.py' if args[0] == 'status' else 'harness-sha'
            with patch.object(runner, 'ROOT', root), patch.object(runner, 'git', side_effect=git), \
                 patch.object(runner, 'source_tree', return_value=nullcontext((root, 'source-sha', False))), \
                 patch.object(runner, 'target', side_effect=OSError('setup refused')), \
                 redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
                self.assertEqual(runner.main(['--suite', 'example', '--version', 'HEAD', '--output', str(root / 'out')]), 1)
            report = json.loads((root / 'out/report.json').read_text())
            self.assertFalse(report['source_working_tree_dirty'])
            self.assertTrue(report['harness_working_tree_dirty'])
            self.assertEqual(report['results'][0]['maintainer_ticket'], 'https://example.com/issues/1')
            self.assertEqual(report['results'][0]['status'], 'skipped')
            self.assertEqual(report['run_error'], 'OSError')

    def test_cleanup_attempts_core_and_containers_after_plugin_failure(self):
        # Plugin refusal must not short-circuit unrelated owned-resource teardown.
        sys.path.insert(0, str(runner.ROOT / 'scripts'))
        import push_plugin
        stack = types.SimpleNamespace(state={}, save=Mock(), stop_processes=Mock(), compose=Mock())
        with patch.object(push_plugin, 'stop', side_effect=RuntimeError('still healthy')):
            errors = runner.stop_stack(stack)
        stack.stop_processes.assert_called_once()
        stack.compose.assert_called_once_with('down', '--volumes')
        self.assertEqual(errors, [{'component': 'push_source', 'error': 'RuntimeError'}])

    def test_http_measurements_use_status_latency_and_typed_error_fields(self):
        context = Context(api_url='http://127.0.0.1:1', probe_url='http://127.0.0.1:2')
        response = (401, {'Content-Type': 'application/json'}, b'{"code":"unauthorized","message":"refused"}', 12.5)
        with patch.object(Context, 'request', return_value=response):
            probe = CHECKS['http_probe'](context, {'path': '/v0/corpora'}, {'status': 401, 'max_latency_ms': 10})
            self.assertFalse(probe.met)
            self.assertEqual(probe.measurement['latency_ms'], 12.5)
            shape = CHECKS['error_shape'](context, {'path': '/v0/corpora'}, {'status': 401, 'fields': {'code': 'string', 'message': 'string'}})
            self.assertTrue(shape.met)
        for token in [b'NaN', b'Infinity', b'-Infinity', b'1e999']:
            with self.subTest(token=token), patch.object(Context, 'request', return_value=(401, {'Content-Type': 'application/json'}, b'{"score":' + token + b'}', 1)):
                self.assertFalse(CHECKS['error_shape'](context, {'path': '/v0/corpora'}, {'status': 401, 'fields': {'score': 'number'}}).met)
        with patch.object(Context, 'request', return_value=(401, {'Content-Type': 'text/html'}, b'{}', 1)):
            shape = CHECKS['error_shape'](context, {'path': '/v0/corpora'}, {'status': 401, 'fields': {'code': 'string'}})
            self.assertFalse(shape.met)

    def test_metric_requires_matching_type_and_valid_family_sample(self):
        context = Context()
        for kind, body, met in [
            ('counter', '# TYPE requests counter\n', False),
            ('counter', '# TYPE requests counter\nrequests garbage\n', False),
            ('counter', '# TYPE requests counter\nrequests{route="x"} 2\n', True),
            ('counter', '# TYPE requests gauge\nrequests 2\n', False),
            ('histogram', '# TYPE requests histogram\nrequests 1\n', False),
            ('histogram', '# TYPE requests histogram\nrequests_bucket{le="1"} 2\n', True),
            ('histogram', '# TYPE requests histogram\nrequests_bucket 2\n', False),
            ('summary', '# TYPE requests summary\nrequests 1\n', False),
            ('summary', '# TYPE requests summary\nrequests{quantile="0.5"} 1\n', True),
            ('summary', '# TYPE requests summary\nrequests{route="x",quantile="0.5"} 1\n', True),
            ('summary', '# TYPE requests summary\nrequests_sum 3\n', True),
        ]:
            with self.subTest(kind=kind, body=body), patch.object(Context, 'request', return_value=(200, {}, body.encode(), 1)):
                self.assertEqual(CHECKS['metric_exposed'](context, {'name': 'requests'}, {'type': kind}).met, met)

    def test_isolated_setup_restores_signal_handler_on_constructor_and_selector_failures(self):
        sys.path.insert(0, str(runner.ROOT / 'scripts'))
        import local, normalizer_plugin
        import signal
        original = signal.getsignal(signal.SIGTERM)
        try:
            with patch.object(local.Stack, '__init__', side_effect=OSError('constructor refused')):
                with self.assertRaises(OSError):
                    with runner.target(Context(), runner.ROOT, isolated=True):pass
            self.assertIs(signal.getsignal(signal.SIGTERM), original)
            with patch.object(local.Stack, '__init__', return_value=None), \
                 patch.object(normalizer_plugin, 'select', side_effect=OSError('selector refused')), \
                 patch.object(runner, 'stop_stack', return_value=[]) as stop:
                with self.assertRaises(OSError):
                    with runner.target(Context(), runner.ROOT, isolated=True):pass
                stop.assert_called_once()
            self.assertIs(signal.getsignal(signal.SIGTERM), original)
        finally:
            signal.signal(signal.SIGTERM, original)

    def test_logs_require_nonempty_json_on_the_named_stream(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'stdout.log'
            context = Context(logs={'stdout': path})
            for data, met in [('', False), ('{"time":"now","level":"INFO","msg":"ready"}\n', True),
                              ('{"time":"now"}\n', False), ('plain text\n', False), ('{"time":NaN,"level":"INFO","msg":"ready"}\n', False)]:
                path.write_text(data)
                result = CHECKS['log_format'](context, {'stream': 'stdout'}, {'fields': ['time', 'level', 'msg']})
                self.assertEqual(result.met, met)
            with self.assertRaises(Skip):
                CHECKS['log_format'](context, {'stream': 'stderr'}, {'fields': ['time']})

    def test_image_user_and_sbom_are_observed_not_assumed(self):
        context = Context(image='example.invalid/quivr:local')
        inspection = {'Id': 'sha256:123', 'Config': {'User': '10001:10001', 'Labels': {'org.opencontainers.image.version': '1'}}}
        with patch('conformance.checks.subprocess.run') as run:
            run.return_value.stdout = json.dumps([inspection]).encode()
            result = CHECKS['image_property'](context, {}, {'non_root': True, 'labels': ['org.opencontainers.image.version']})
            self.assertTrue(result.met)
            inspection['Config']['User'] = '00:10001'
            run.return_value.stdout = json.dumps([inspection]).encode()
            self.assertFalse(CHECKS['image_property'](context, {}, {'non_root': True}).met)
            with self.assertRaises(Skip):
                CHECKS['image_property'](context, {}, {'sbom': True})
            with tempfile.TemporaryDirectory() as directory:
                context.sbom = pathlib.Path(directory) / 'sbom.json'
                for image_id, artifacts, met in [('sha256:123', [{'name': 'sample-component'}], True),
                                                 ('sha256:other', [{'name': 'sample-component'}], False),
                                                 ('sha256:123', [], False)]:
                    context.sbom.write_text(json.dumps({'source': {'target': {'imageID': image_id}}, 'artifacts': artifacts}))
                    self.assertEqual(CHECKS['image_property'](context, {}, {'sbom': True}).met, met)

    def test_config_rejection_requires_expected_diagnostic_not_a_crash(self):
        with tempfile.TemporaryDirectory() as directory:
            context = Context(binary=pathlib.Path('/usr/bin/false'), evidence_dir=pathlib.Path(directory))
            with patch('conformance.checks.subprocess.run') as run:
                run.return_value.returncode = 1
                # Fixed process output is written to the real capture files by the dependency fake.
                def output(*args, **kwargs):
                    kwargs['stderr'].write(b'{"msg":"invalid configuration JSON: unknown field"}\n')
                    return run.return_value
                run.side_effect = output
                self.assertTrue(CHECKS['config_refuses_invalid'](context, {'fixture': 'unknown_field'}, {'exit_code': 1, 'diagnostic': 'unknown field'}).met)
                self.assertFalse(CHECKS['config_refuses_invalid'](context, {'fixture': 'unknown_field'}, {'exit_code': 1, 'diagnostic': 'different validation'}).met)
