"""Dispatch owner: CI/holdout refusal, shared compute cap and evidence recovery.

Only the Modal remote call is fake. Real SQL and Results persistence protect
ordering regressions that the budget and provider owner tests cannot observe.
"""
import io
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import types
import unittest
import uuid
from unittest import mock

import control_store
import modal_search
import protected_inputs
import search_trial


class Refusal(unittest.TestCase):
    def test_dry_run_needs_no_keys_and_ci_or_holdout_refuses_before_dispatch(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            policy = {'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
                      'modal_usd_per_second': .001, 'price_revision': '2026-10-03'}
            (root / 'policy.json').write_text(json.dumps(policy))
            (root / 'candidate.json').write_text('{}')
            args = ['--policy', str(root / 'policy.json'), '--candidate', str(root / 'candidate.json')]
            with mock.patch('sys.stdout', new_callable=io.StringIO) as output:
                self.assertEqual(modal_search.main(args + ['--dry-run']), 0)
            self.assertIn('full Modal invoice', output.getvalue())
            with mock.patch.dict(os.environ, {'CI': 'true'}), mock.patch.object(modal_search, 'launch') as launch, mock.patch('sys.stderr', new_callable=io.StringIO) as error:
                with self.assertRaises(SystemExit):
                    modal_search.main(args + ['--allow-paid'])
                self.assertIn('CI measurements are refused', error.getvalue())
                launch.assert_not_called()
            policy['sets']['scifact']['diagnostic'] = True
            with self.assertRaisesRegex(ValueError, 'explicit reason'):
                modal_search.policy(policy)
            policy['sets']['scifact']['reason'] = 'small query sample'
            self.assertEqual(modal_search.policy(policy)['sets']['scifact']['reason'], 'small query sample')
            policy['sets']['scifact']['split'] = 'test'
            with self.assertRaises(PermissionError):
                modal_search.policy(policy)

    def test_resource_policy_derives_price_and_rejects_underestimated_bounds(self):
        # Policy owns resource/pricing consistency; no network or Modal calls.
        base = {'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
                'price_revision': '2026-10-05'}
        small = modal_search.policy(base)
        self.assertEqual((small['quality_concurrency'], small['modal_cpu'], small['modal_memory_mib']), (8, 2, 4096))
        self.assertAlmostEqual(small['modal_usd_per_second'], .00003508)
        large = modal_search.policy({**base, 'modal_cpu': 8, 'modal_memory_mib': 16384})
        self.assertAlmostEqual(large['modal_usd_per_second'], 4 * small['modal_usd_per_second'])
        custom = modal_search.policy({**base, 'modal_cpu': 3, 'modal_memory_mib': 2048,
            'modal_cpu_usd_per_second': .001, 'modal_gib_usd_per_second': .002})
        self.assertAlmostEqual(custom['modal_usd_per_second'], .007)
        self.assertEqual(modal_search.policy({**base, 'modal_usd_per_second': .001})['modal_usd_per_second'], .001)
        self.assertEqual(modal_search.policy(small), small)
        for override in ({'quality_concurrency': 0}, {'quality_concurrency': 33}, {'quality_concurrency': True},
                         {'modal_cpu': 0}, {'modal_memory_mib': 127}, {'modal_cpu_usd_per_second': 0},
                         {'modal_usd_per_second': .000001}):
            with self.subTest(override=override), self.assertRaises(ValueError):
                modal_search.policy({**base, **override})


class RemoteSerialization(unittest.TestCase):
    def test_remote_trial_loads_before_evaluation_modules_are_importable(self):
        # The container adds the evaluation directory to sys.path only inside the
        # call. Campaigns import this module, so launch must force a by-value payload.
        from modal._serialization import serialize
        from modal._vendor import cloudpickle
        try:
            payload = serialize(modal_search.shipped_trial())
        finally:
            cloudpickle.unregister_pickle_by_value(modal_search)
        with tempfile.TemporaryDirectory() as temp:
            child = subprocess.run([sys.executable, '-I', '-c', 'import pickle, sys; pickle.loads(sys.stdin.buffer.read())'],
                                   input=payload, cwd=temp, capture_output=True, timeout=30)
        self.assertEqual(child.returncode, 0, child.stderr.decode()[-500:])


class FailureLogging(unittest.TestCase):
    def test_private_failure_verdict_and_log_only_use_fixed_diagnostics(self):
        # Own the protected remote catch boundary, including the verdict.
        # Provider text and dynamic exception names cannot cross it.
        sensitive = 'private passage /private/input document-id-42 https://provider/path'
        class ChangingMessage:
            calls = 0
            def __str__(self):
                self.calls += 1
                return 'provider HTTP 429' if self.calls == 1 else sensitive
        cases = (
            (RuntimeError('provider HTTP 429'), 'RuntimeError', 'provider HTTP 429'),
            (RuntimeError('provider HTTP 503'), 'RuntimeError', 'provider HTTP 503'),
            (RuntimeError('provider transport failed after 8 attempts'), 'RuntimeError', 'provider transport failed after 8 attempts'),
            (RuntimeError('provider omitted confirmed usage; measurement rejected'),
             'RuntimeError', 'provider omitted confirmed usage; measurement rejected'),
            (protected_inputs.DecryptionError('protected input identity or ciphertext rejected'),
             'DecryptionError', 'protected input identity or ciphertext rejected'),
            (RuntimeError('provider HTTP 429 ' + sensitive), 'RuntimeError', 'protected measurement failed'),
            (RuntimeError(sensitive), 'RuntimeError', 'protected measurement failed'),
            (RuntimeError(ChangingMessage()), 'RuntimeError', 'provider HTTP 429'),
            (type('PrivateDocumentId42', (RuntimeError,), {})('provider HTTP 429'), 'RuntimeError', 'protected measurement failed'),
        )
        for case, (error, kind, expected) in enumerate(cases):
            with self.subTest(case=case, kind=kind):
                request = {'config': search_trial.configuration({}),
                           'policy': {'sets': {'private-example': {'split': 'dev', 'input': {}}}, 'max_seconds': 30},
                           'dataset': 'private-example', 'campaign': 'fixture', 'lease_key': 'trial', 'owner': 'owner'}
                with mock.patch.dict('sys.modules', {'modal': types.SimpleNamespace(Volume=mock.Mock())}), \
                        mock.patch.dict(os.environ, {'EVAL_CONTROL_DATABASE_URL': 'postgres://fixture'}), \
                        mock.patch.object(control_store, 'Store') as store, \
                        mock.patch.object(modal_search.results.Results, 'sync'), \
                        mock.patch.object(modal_search.private_working, 'trial', side_effect=error), \
                        self.assertLogs('modal_search', level='INFO') as logs:
                    row = modal_search.remote_trial(request)
                self.assertEqual(row, {'status': 'failed',
                    'reason': 'direct measurement failed; uncertain charges retained',
                    'error': {'kind': kind, 'message': expected}})
                failure = next(line for line in logs.output if 'trial failed' in line)
                self.assertIn('error=' + kind + ' message=' + expected, failure)
                for forbidden in ('private passage', '/private/input', 'document-id-42', 'https://provider', 'PrivateDocumentId42'):
                    self.assertNotIn(forbidden, failure + json.dumps(row))
                store.return_value.abandon.assert_called_once_with('fixture', 'trial', 'owner', 'failed')

    def test_remote_failure_logs_bounded_diagnostics_without_credentials(self):
        # Own diagnostics at the remote catch boundary. Only external setup
        # and dataset I/O are fake; exception formatting and logging are real.
        cfg = search_trial.configuration({})
        request = {'config': cfg, 'policy': {'sets': {'scifact': {'split': 'dev'}}, 'max_seconds': 30},
                   'dataset': 'scifact', 'campaign': 'fixture', 'lease_key': 'trial', 'owner': 'owner'}
        modal = types.SimpleNamespace(Volume=mock.Mock())
        credentials = {'AZURE_FOUNDRY_KEY': 'fixture-provider-credential',
                       'EVAL_CONTROL_DATABASE_URL': 'postgres://user:pass@host/db'}
        message = ('cache missing ' + credentials['AZURE_FOUNDRY_KEY'] + '\n'
                   'https://host/path?token=unknown postgres://user:pass@host/db '
                   'token=unknown-token password="unknown password" Bearer unknown-bearer '
                   'sk-unknownkey ' + 'x' * 200)
        with mock.patch.dict('sys.modules', {'modal': modal}), mock.patch.dict(os.environ, credentials), \
                mock.patch.object(control_store, 'Store') as store, \
                mock.patch.object(modal_search.results.Results, 'sync'), \
                mock.patch.object(modal_search.public_sets, 'prepare', side_effect=OSError(message)), \
                self.assertLogs('modal_search', level='INFO') as logs:
            row = modal_search.remote_trial(request)
        failure = next(line for line in logs.output if 'trial failed' in line)
        self.assertIn('error=OSError message=cache missing', failure)
        self.assertIn('[redacted]', failure)
        for secret in (*credentials.values(), 'https://host', 'unknown-token', 'unknown password',
                       'unknown-bearer', 'sk-unknownkey'):
            self.assertNotIn(secret, failure)
        diagnostic = failure.split('message=', 1)[1].split(' elapsed_seconds=', 1)[0]
        self.assertLessEqual(len(diagnostic), 160)
        self.assertNotIn('\n', diagnostic)
        self.assertEqual(row, {'status': 'failed', 'reason': 'direct measurement failed; uncertain charges retained'})
        store.return_value.abandon.assert_called_once_with('fixture', 'trial', 'owner', 'failed')


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN'), 'requires disposable PostgreSQL')
class Dispatch(unittest.TestCase):
    def test_compute_cap_precedes_second_call_and_completed_result_is_replayed(self):
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        cfg = search_trial.configuration({})
        policy = modal_search.policy({'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
            'modal_usd_per_second': .001, 'price_revision': '2026-10-03',
            'modal_daily_usd': .08, 'max_seconds': 30, 'startup_seconds': 10})
        campaign, calls = uuid.uuid4().hex, []
        def remote(request):
            calls.append(request)
            row = {'schema_version': 1, 'experiment': 'public/example', 'git_sha': 'a' * 40,
                'plugin_digest': 'sha256:fixture', 'config': request['config'], 'tier': 'direct',
                'machine': 'fake-modal', 'duration_seconds': 1, 'cost': {},
                'dataset': {'name': 'scifact', 'version': '1', 'split': 'dev', 'fingerprint': 'fixture', 'private': False},
                'metrics': {'ndcg@10': .5}, 'per_query': {'ndcg@10': {'q': .5}}}
            store.publish(campaign, request['lease_key'], request['owner'], row)
            return row
        with tempfile.TemporaryDirectory() as temp:
            # Unknown invocation charges remain reserved: no false settlement from a fake clock.
            def uncertain(_):
                calls.append('uncertain')
                raise RuntimeError('reflected-key')
            with mock.patch.dict(os.environ, {'MLFLOW_TRACKING_URI': ''}):
                first = modal_search.dispatch(store, campaign, policy, cfg, 'scifact', 'a' * 40,
                                               'sha256:fixture', remote, pathlib.Path(temp), True)
                self.assertEqual(first['status'], 'complete')
                replay = modal_search.dispatch(store, campaign, policy, cfg, 'scifact', 'a' * 40,
                                                'sha256:fixture', remote, pathlib.Path(temp), True)
                self.assertEqual(replay['status'], 'reused')
                self.assertEqual(len(calls), 1)
                candidate = dict(cfg, dense_weight=.5)
                modal_search.dispatch(store, campaign, policy, candidate, 'scifact', 'a' * 40,
                                      'sha256:fixture', uncertain, pathlib.Path(temp), True)
                capped = modal_search.dispatch(store, campaign, policy, dict(cfg, dense_weight=0),
                    'scifact', 'a' * 40, 'sha256:fixture', remote, pathlib.Path(temp), True)
                self.assertEqual(capped['status'], 'capped')
                self.assertEqual(len(calls), 2)
                self.assertNotIn('reflected-key', json.dumps(capped))


if __name__ == '__main__':
    unittest.main()
