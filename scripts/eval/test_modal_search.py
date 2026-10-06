"""Dispatch owner: holdout refusal, shared compute cap and evidence recovery.

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
import direct_bakeoff
import results
import trec
import modal_search
import protected_inputs
import search_trial


class Refusal(unittest.TestCase):
    def test_dry_run_needs_no_keys_and_holdout_is_refused(self):
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
    def test_acknowledged_modal_call_reattaches_without_respawn_and_gives_up_boundedly(self):
        import modal
        from grpclib import GRPCError, Status
        from modal._grpc_client import grpc_error_to_modal_exception
        from modal._utils.grpc_utils import RetryTimeoutError
        clock, waits, identities = [0], [], []
        def wait(seconds):
            waits.append(seconds)
            clock[0] += seconds
        call = mock.Mock(object_id='fc-survivor')
        remote = mock.Mock()
        remote.spawn.return_value = call
        result = {'status': 'complete'}
        call.get.side_effect = [modal.exception.ConnectionError('secret'),
                               grpc_error_to_modal_exception(GRPCError(Status.UNAVAILABLE, 'secret')),
                               RetryTimeoutError(grpc_error_to_modal_exception(
                                   GRPCError(Status.DEADLINE_EXCEEDED, 'secret'))), result]
        def attach(identity):
            identities.append(identity)
            return call
        with mock.patch('modal.FunctionCall.from_id', side_effect=attach), \
             mock.patch('time.monotonic', side_effect=lambda: clock[0]), \
             mock.patch('time.sleep', side_effect=wait), \
             mock.patch.dict(os.environ, EVAL_NETWORK_OUTAGE_SECONDS='12'):
            self.assertEqual(modal_search.invoke(remote, {'campaign': 'opaque'}, lambda: None), result)
            remote.spawn.assert_called_once()
            self.assertEqual(identities, ['fc-survivor'] * 4)
            self.assertTrue(waits)
            waits.clear()
            call.get.side_effect = modal.exception.ConnectionError('secret')
            import network_recovery
            with self.assertRaises(network_recovery.Outage) as error:
                modal_search.invoke(remote, {'campaign': 'opaque'}, lambda: None)
            self.assertTrue(waits)
            self.assertNotIn('secret', str(error.exception))
            self.assertEqual(remote.spawn.call_count, 2, 'one spawn per admitted invocation')
            waits.clear()
            for refusal in (modal.exception.AuthError('secret'),
                            grpc_error_to_modal_exception(GRPCError(Status.UNKNOWN, 'secret')),
                            grpc_error_to_modal_exception(GRPCError(Status.CANCELLED, 'secret'))):
                call.get.side_effect = refusal
                with self.assertRaises(type(refusal)):
                    modal_search.invoke(remote, {'campaign': 'opaque'}, lambda: None)
                self.assertFalse(waits, 'a refusal must not retry')
            # A lost spawn acknowledgement is ambiguous: never replay it.
            remote.spawn.side_effect = modal.exception.ConnectionError('secret')
            with self.assertRaises(network_recovery.Outage):
                modal_search.invoke(remote, {'campaign': 'opaque'}, lambda: None)
            self.assertEqual(remote.spawn.call_count, 6)
            self.assertFalse(waits)
            with self.assertRaises(control_store.LeaseLost):
                modal_search.invoke(remote, {'campaign': 'opaque'},
                                    mock.Mock(side_effect=control_store.LeaseLost('owner replaced')))
            self.assertEqual(remote.spawn.call_count, 6, 'lost ownership must refuse before paid spawn')

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
    def test_public_trial_pairs_fresh_samples_and_replays_both_records(self):
        # Own public pairing at the dispatch/remote/publication boundary. Real
        # SQL, cache, ranking and scorer; fake only mounts, dataset and provider I/O.
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1',
                                          'dimensions': 2, 'dense_weight': .7})
        policy = modal_search.policy({'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
            'baseline': dict(cfg, dense_weight=.5), 'price_revision': 'fixture-v1'})
        campaign, served, calls, clock = uuid.uuid4().hex, [], [], [0.]
        original_rank, original_measure, original_results = search_trial.SearchIndex.rank, search_trial.measure_pair, results.Results
        def rank(index, query, *args, **kwargs):
            served.append((index.cfg['dense_weight'], query))
            clock[0] += .02
            return original_rank(index, query, *args, **kwargs)
        def provider(request, timeout):
            body = json.loads(request.data)
            with store.transaction() as db:
                window = db.execute("SELECT owner FROM eval_control.leases WHERE campaign=%s AND key='campaign-latency-slot' AND expires_at>clock_timestamp()", (campaign,)).fetchone()
            self.assertEqual(window is not None, len(body['texts']) == 1,
                             'only fresh paired serving, including warmup, holds the latency window')
            calls.append(body)
            clock[0] += .1 + .001 * len(calls)
            count = len(calls[-1]['texts'])
            return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                'meta': {'billed_units': {'input_tokens': count}}}).encode())
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            trec.write(root / 'data', {'a': {'text': 'apple'}, 'b': {'text': 'pear'}},
                {f'q{i:02}': f'apple question {i}' for i in range(20)},
                {f'q{i:02}': {'a': 1} for i in range(20)})
            def measure(configs, data, dataset, cache, *args, **kwargs):
                return original_measure(configs, data, dataset, root / 'vectors', *args, **kwargs)
            def outbox(*args, **kwargs):
                return original_results(directory=root / 'outbox')
            environment = {'EVAL_CONTROL_DATABASE_URL': store.dsn, 'AZURE_FOUNDRY_ENDPOINT': 'https://example.com',
                           'AZURE_FOUNDRY_KEY': 'fixture-key', 'MLFLOW_TRACKING_URI': ''}
            with mock.patch.dict(os.environ, environment), mock.patch('modal.Volume'), \
                    mock.patch.object(modal_search.public_sets, 'prepare', return_value=root / 'data'), \
                    mock.patch.object(search_trial, 'measure_pair', side_effect=measure), \
                    mock.patch.object(results, 'Results', side_effect=outbox), \
                    mock.patch.object(search_trial.SearchIndex, 'rank', rank), \
                    mock.patch('time.monotonic', side_effect=lambda: clock[0]), \
                    mock.patch('urllib.request.OpenerDirector.open', side_effect=provider):
                def run(config):
                    return modal_search.dispatch(store, campaign, policy, config, 'scifact', 'a' * 40,
                        'sha256:fixture', modal_search.remote_trial, root / 'local', True)
                outcome = run(cfg)
                self.assertEqual(outcome['status'], 'complete')
                self.assertIn('baseline_record', outcome)
                fresh = served[40:]
                self.assertEqual([weight for weight, _ in fresh], [.5, .7] * 21)
                self.assertTrue(all(a[1] == b[1] for a, b in zip(fresh[::2], fresh[1::2])))
                baseline, candidate = outcome['baseline_record'], outcome['record']
                self.assertEqual(baseline['machine'], candidate['machine'])
                self.assertEqual(baseline['cost']['latency_sample'], candidate['cost']['latency_sample'])
                self.assertLess(candidate['metrics']['latency_p95_ms'] / baseline['metrics']['latency_p95_ms'], 1.02)
                before = len(calls)
                replay = run(cfg)
                self.assertEqual(replay['status'], 'reused')
                self.assertEqual(len(calls), before)
                self.assertEqual(replay['baseline_record'], baseline)
                other = run(dict(cfg, dense_weight=.8))
                self.assertNotEqual(other['baseline_record']['config'], baseline['config'],
                                    'each candidate needs its own paired baseline identity')

    def test_paired_aa_latency_stays_exclusive_during_concurrent_quality(self):
        # Own phase placement end to end: real paired measurement, scorer,
        # budgets and SQL. Fake only provider I/O, mounts, inputs and time.
        import threading
        from concurrent.futures import ThreadPoolExecutor
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1',
                                          'dimensions': 2, 'dense_weight': .5})
        policy = modal_search.policy({'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
            'baseline': cfg, 'price_revision': 'fixture-v1'})
        campaign = uuid.uuid4().hex
        local = threading.local()
        indexing = threading.Barrier(2)
        quality_active, first_window, second_wait, first_done = [threading.Event() for _ in range(4)]
        fresh_order = []
        original_measure, original_results = search_trial.measure_pair, results.Results
        original_rank = search_trial.SearchIndex.rank
        def clock():
            return getattr(local, 'clock', 0.)
        def rank(*args, **kwargs):
            local.clock += .02
            return original_rank(*args, **kwargs)
        def provider(request, timeout):
            body = json.loads(request.data)
            if not hasattr(local, 'trial'):
                local.trial = 'first' if 'first' in body['texts'][0] else 'second'
                local.clock = 0.
            if body['input_type'] == 'search_document':
                indexing.wait(5)  # Both trial preparations must overlap.
            elif len(body['texts']) > 1 and local.trial == 'second':
                quality_active.set()
                self.assertTrue(first_window.wait(5))
            elif len(body['texts']) == 1:
                if local.trial == 'first' and not first_window.is_set():
                    self.assertTrue(quality_active.wait(5))
                    first_window.set()
                    self.assertTrue(second_wait.wait(5))
                fresh_order.append(local.trial)
            local.clock += .1
            count = len(body['texts'])
            return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                'meta': {'billed_units': {'input_tokens': count}}}).encode())
        def wait(_):
            self.assertEqual(local.trial, 'second')
            second_wait.set()
            self.assertTrue(first_done.wait(5))
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            for trial in ('first', 'second'):
                trec.write(root / trial / 'data', {'a': {'text': 'apple ' + trial}, 'b': {'text': 'pear ' + trial}},
                    {f'q{i}': 'apple ' + trial + ' question ' + str(i) for i in range(4)},
                    {f'q{i}': {'a': 1} for i in range(4)})
            def measure(configs, data, dataset, cache, *args, **kwargs):
                return original_measure(configs, data, dataset, root / local.trial / 'vectors', *args, **kwargs)
            def outbox(*args, **kwargs):
                return original_results(directory=root / local.trial / 'outbox')
            def run(trial):
                local.trial, local.clock = trial, 0.
                # Separate canonical input identity, same A/A search settings.
                # One campaign policy must stay immutable: dataset requests have
                # separate invocation leases but identical frozen settings.
                frozen = modal_search.frozen_policy(policy, 'a' * 40, 'sha256:fixture')
                store.campaign(campaign, frozen)
                key = 'trial/' + trial
                owner = store.claim(campaign, key)['owner']
                request = {'campaign': campaign, 'policy': frozen, 'config': cfg, 'dataset': 'scifact',
                    'lease_key': key, 'owner': owner, 'git_sha': 'a' * 40,
                    'scorer_digest': 'sha256:fixture', 'fresh_latency': True}
                try:
                    return modal_search.remote_trial(request)
                finally:
                    if trial == 'first':
                        first_done.set()
            environment = {'EVAL_CONTROL_DATABASE_URL': store.dsn, 'AZURE_FOUNDRY_ENDPOINT': 'https://example.com',
                           'AZURE_FOUNDRY_KEY': 'fixture-key', 'MLFLOW_TRACKING_URI': ''}
            with mock.patch.dict(os.environ, environment), mock.patch('modal.Volume'), \
                    mock.patch.object(modal_search.public_sets, 'prepare', side_effect=lambda *a, **kw: root / local.trial / 'data'), \
                    mock.patch.object(search_trial, 'measure_pair', side_effect=measure), \
                    mock.patch.object(results, 'Results', side_effect=outbox), \
                    mock.patch.object(search_trial.SearchIndex, 'rank', rank), \
                    mock.patch('time.monotonic', side_effect=clock), mock.patch('time.sleep', side_effect=wait), \
                    mock.patch('urllib.request.OpenerDirector.open', side_effect=provider), \
                    ThreadPoolExecutor(max_workers=2) as pool:
                a, b = pool.submit(run, 'first'), pool.submit(run, 'second')
                rows = [a.result(15), b.result(15)]
            self.assertEqual(fresh_order, ['first'] * 10 + ['second'] * 10)
            for row in rows:
                self.assertIn('metrics', row, row)
                baseline_key = row['provenance']['public_pair']['baseline_lease_key']
                baseline = store.evidence(campaign, [baseline_key])[baseline_key]
                self.assertAlmostEqual(row['metrics']['latency_p95_ms'] / baseline['metrics']['latency_p95_ms'], 1.)
                self.assertEqual(row['cost']['search_timing_ms']['retried_samples'], 0)

    def test_launch_survives_network_gap_with_detached_app_and_one_paid_spawn_per_measurement(self):
        import modal
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        campaign = uuid.uuid4().hex
        policy = modal_search.policy({'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
            'modal_usd_per_second': .001, 'price_revision': '2026-10-03',
            'modal_daily_usd': 10, 'max_seconds': 30, 'startup_seconds': 10})
        app, remote = mock.MagicMock(), mock.Mock()
        app.function.return_value = lambda _: remote
        app.app_id = 'ap-survivor'
        launch_started = mock.Mock()
        def run_app(**kwargs):
            launch_started.assert_called_once_with()
            return mock.MagicMock()
        app.run.side_effect = run_app
        requests = []
        calls = {}
        def spawn(request):
            requests.append(request)
            identity = 'fc-' + str(len(requests))
            attempt = [0]
            def get(**_):
                attempt[0] += 1
                if len(requests) == 1 and attempt[0] <= 2:
                    raise modal.exception.ConnectionError('network secret')
                row = {'schema_version': 1, 'experiment': 'public/example', 'git_sha': 'a' * 40,
                    'plugin_digest': 'sha256:fixture', 'config': request['config'], 'tier': 'direct',
                    'machine': 'fake-modal', 'duration_seconds': 1, 'cost': {},
                    'dataset': {'name': 'scifact', 'version': '1', 'split': 'dev', 'fingerprint': 'fixture', 'private': False},
                    'metrics': {'ndcg@10': .5, 'latency_p95_ms': 10, 'cost_per_search_usd': .0001,
                                'cost_per_1000_documents_usd': 1},
                    'per_query': {'ndcg@10': {'q1': .5, 'q2': .5}}}
                baseline = {**row, 'config': {**policy['baseline'], 'paired_side': 'baseline',
                    'paired_candidate_hash': search_trial.digest(request['config'])}}
                row['provenance'] = {'public_pair': {
                    'baseline_lease_key': request['lease_key'] + '/' + request['owner'] + '/baseline'}}
                return search_trial.publish_pair(store, request, {'baseline': baseline, 'candidate': row})
            call = mock.Mock(object_id=identity)
            call.get.side_effect = get
            calls[identity] = call
            return call
        remote.spawn.side_effect = spawn
        def backoff(seconds):
            self.assertEqual(len(requests), 1, 'network retries cannot create paid invocations')
            blocked = modal_search.launch(policy, search_trial.configuration({'dense_weight': .8}), campaign, temp, True)
            self.assertEqual(blocked['status'], 'leased')
            self.assertEqual(len(requests), 1, 'another trial cannot spawn while the slot is held')
            import network_recovery
            # Zero admission wait proves the other parallel trial is refused
            # while this transport is disconnected, without sleeping.
            with mock.patch.dict(os.environ, EVAL_NETWORK_OUTAGE_SECONDS='0'):
                with self.assertRaises(network_recovery.Outage):
                    store.reserve(campaign, 'provider', .01)
        with tempfile.TemporaryDirectory() as temp, \
             mock.patch.dict(os.environ, EVAL_CONTROL_DATABASE_URL=store.dsn, MLFLOW_TRACKING_URI=''), \
             mock.patch('modal.App', return_value=app), mock.patch('modal.Image'), \
             mock.patch('modal.Secret'), mock.patch('modal.Volume'), \
             mock.patch('modal.FunctionCall.from_id', side_effect=lambda identity: calls[identity]), \
             mock.patch('modal_search.shipped_trial'), \
             mock.patch('modal_search.subprocess.run', return_value=subprocess.CompletedProcess([], 0)), \
             mock.patch('modal_search.subprocess.check_output', side_effect=lambda args, **kw: b'' if 'ls-files' in args else 'a' * 40), \
             mock.patch('time.sleep', side_effect=backoff):
            report = modal_search.launch(policy, search_trial.configuration({'dense_weight': .5}),
                                         campaign, temp, True, on_launch=launch_started)
        app.run.assert_called_once_with(detach=True)
        self.assertEqual(len(requests), 1)
        self.assertEqual(len(report['work']), 2)
        self.assertEqual(store.summary(campaign)['modal']['unknown_usd'], 0)
        self.assertNotIn('provider', store.summary(campaign))
        self.assertTrue(store.reserve(campaign, 'provider', .01), 'admission resumes after reconnection')
        self.assertEqual(store.claim(campaign, 'campaign-measurement-slot')['status'], 'claimed',
                         'acknowledged completion releases the slot')

    def test_launch_admits_parallel_trials_before_latency(self):
        # Regression: preparation used to hold the only campaign slot. Real SQL
        # admission must admit three apps and refuse a fourth before app creation.
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        campaign = uuid.uuid4().hex
        policy = modal_search.policy({'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
            'price_revision': 'fixture-v1', 'max_seconds': 30, 'startup_seconds': 10})
        app, remote = mock.MagicMock(), mock.Mock()
        app.function.return_value = lambda _: remote
        app.app_id = 'ap-detached'
        with tempfile.TemporaryDirectory() as temp, \
                mock.patch.dict(os.environ, EVAL_CONTROL_DATABASE_URL=store.dsn), \
                mock.patch('modal.App', return_value=app) as apps, mock.patch('modal.Image'), \
                mock.patch('modal.Secret'), mock.patch('modal.Volume'), mock.patch('modal_search.shipped_trial'), \
                mock.patch('modal_search.subprocess.run', return_value=subprocess.CompletedProcess([], 0)), \
                mock.patch('modal_search.subprocess.check_output', side_effect=lambda args, **kw: b'' if 'ls-files' in args else 'a' * 40), \
                mock.patch('modal_search.invoke', side_effect=control_store.LeaseLost('detached')):
            for weight in (.2, .4, .6):
                with self.assertRaises(control_store.LeaseLost):
                    modal_search.launch(policy, search_trial.configuration({'dense_weight': weight}),
                        campaign, temp, True, parallelism=3)
            refused = modal_search.launch(policy, search_trial.configuration({'dense_weight': .8}),
                campaign, temp, True, parallelism=3)
            self.assertEqual(refused['status'], 'leased')
            self.assertEqual(apps.call_count, 3, 'trial bound precedes allocating paid apps')
        self.assertEqual(store.claim(campaign, 'campaign-latency-slot')['status'], 'claimed',
                         'preparation does not own the latency window')

    def test_slot_expiry_during_admission_creates_no_modal_reservation(self):
        # Own dual-lease admission at dispatch. Simulate an admission wait by
        # expiring only the slot in SQL; the later trial lease remains valid.
        import network_recovery
        import psycopg
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        campaign = uuid.uuid4().hex
        policy = modal_search.policy({'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
            'price_revision': 'fixture-v1', 'max_seconds': 30, 'startup_seconds': 10})
        store.campaign(campaign, modal_search.frozen_policy(policy, 'a' * 40, 'sha256:fixture'))
        slot_key = 'campaign-measurement-slot'
        slot = store.claim(campaign, slot_key, 40)
        def expire():
            with psycopg.connect(store.dsn) as db:
                db.execute("UPDATE eval_control.leases SET expires_at=clock_timestamp() WHERE campaign=%s AND key=%s",
                           (campaign, slot_key))
        def refuse_spawn(request):
            store.renew(campaign, slot_key, slot['owner'], 40)
            self.fail('expired slot must refuse before invoking Modal')
        invoke = mock.Mock(side_effect=refuse_spawn)
        with tempfile.TemporaryDirectory() as temp, mock.patch.dict(os.environ, MLFLOW_TRACKING_URI=''), \
                mock.patch.object(network_recovery.admission(campaign), 'wait', side_effect=expire):
            with self.assertRaises(control_store.LeaseLost):
                modal_search.dispatch(store, campaign, policy, search_trial.configuration({}), 'scifact',
                    'a' * 40, 'sha256:fixture', invoke, temp, True, measurement_slot=(slot_key, slot['owner']))
        invoke.assert_not_called()
        self.assertNotIn('modal', store.summary(campaign), 'unspawned work cannot consume a cap')

    def test_campaign_slot_retains_detached_work_after_outage_or_owner_loss(self):
        # Own slot failure lifecycle: losing an acknowledgement/owner cannot let
        # another trial overlap detached work. Expiry uses SQL, never wall time.
        import modal
        import network_recovery
        import psycopg
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        policy = modal_search.policy({'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
            'price_revision': 'fixture-v1', 'max_seconds': 30, 'startup_seconds': 10})
        for failure in ('setup', 'outage', 'owner'):
            lost_owner = failure == 'owner'
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temp:
                campaign = uuid.uuid4().hex
                app, remote, spawned = mock.MagicMock(), mock.Mock(), []
                app.function.return_value = lambda _: remote
                app.app_id = 'ap-detached'
                if failure == 'setup':
                    app.function.side_effect = ValueError('local setup failed')
                label = mock.Mock(return_value='tracked-app')
                def spawn(request):
                    spawned.append(request)
                    if not lost_owner:
                        raise modal.exception.ConnectionError('lost acknowledgement')
                    return mock.Mock(object_id='fc-detached')
                remote.spawn.side_effect = spawn
                def check():
                    if lost_owner and spawned:
                        raise control_store.LeaseLost('owner replaced')
                with mock.patch.dict(os.environ, EVAL_CONTROL_DATABASE_URL=store.dsn), \
                        mock.patch('modal.App', return_value=app), mock.patch('modal.Image'), \
                        mock.patch('modal.Secret'), mock.patch('modal.Volume'), mock.patch('modal_search.shipped_trial'), \
                        mock.patch('modal_search.subprocess.run', return_value=subprocess.CompletedProcess([], 0)), \
                        mock.patch('modal_search.subprocess.check_output', side_effect=lambda args, **kw: b'' if 'ls-files' in args else 'a' * 40):
                    with self.assertRaises(ValueError if failure == 'setup' else control_store.LeaseLost if lost_owner else network_recovery.Outage):
                        modal_search.launch(policy, search_trial.configuration({}), campaign, temp, True, check=check, app_name=label)
                    if failure == 'setup':
                        app.run.assert_not_called()
                        self.assertEqual(spawned, [])
                        self.assertEqual(store.claim(campaign, 'campaign-measurement-slot')['status'], 'claimed')
                        continue
                    blocked = modal_search.launch(policy, search_trial.configuration({'dense_weight': .5}), campaign, temp, True, app_name=label)
                    self.assertEqual(blocked['status'], 'leased')
                    self.assertEqual(len(spawned), 1)
                    self.assertEqual(app.run.call_count, 1, 'busy trials must not start paid apps')
                    label.assert_called_once()
                with psycopg.connect(store.dsn) as db:
                    db.execute("UPDATE eval_control.leases SET expires_at=clock_timestamp() WHERE campaign=%s AND key=%s",
                               (campaign, 'campaign-measurement-slot'))
                self.assertEqual(store.claim(campaign, 'campaign-measurement-slot')['status'], 'claimed')

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
