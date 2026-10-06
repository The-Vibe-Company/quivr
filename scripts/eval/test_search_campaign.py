"""Campaign public CLI/loop owner: external measurement transports only are fake."""
import copy
import importlib.util
import io
import json
import os
import pathlib
import subprocess
import tempfile
import threading
import unittest
from concurrent.futures import ThreadPoolExecutor
from unittest import mock

import control_store
import embeddings


class ModalTermination(unittest.TestCase):
    def test_failed_stop_requires_visible_stopped_app_with_zero_tasks(self):
        import campaign_compute
        import campaign_store
        running = {'app_id': 'ap-trial', 'state': 'running', 'tasks': 1}
        for state, tasks, acknowledged in (('stopped', 0, True), ('stopped', 1, False),
                                           ('running', 0, False), (None, None, False)):
            with self.subTest(state=state, tasks=tasks):
                listing = [dict(running, state=state, tasks=tasks)] if state else []
                calls = []
                def cli(command, **kwargs):
                    calls.append(command)
                    if 'stop' in command:
                        raise subprocess.CalledProcessError(1, command,
                            stderr=b'already stopped token=fixture-secret https://provider/private')
                    rows = [running] if len(calls) == 1 else listing
                    return subprocess.CompletedProcess(command, 0, stdout=json.dumps(rows))
                with mock.patch('campaign_compute.subprocess.run', side_effect=cli), \
                     self.assertLogs('campaign_compute', level='WARNING') as diagnostic:
                    if acknowledged:
                        campaign_compute.ModalCompute().stop('ap-trial')
                    else:
                        with self.assertRaises(campaign_store.CleanupPending):
                            campaign_compute.ModalCompute().stop('ap-trial')
                self.assertIn('already stopped', ' '.join(diagnostic.output))
                self.assertNotIn('fixture-secret', ' '.join(diagnostic.output))
                self.assertNotIn('https://provider/private', ' '.join(diagnostic.output))


@unittest.skipUnless(importlib.util.find_spec('yaml'), 'requires PyYAML')
class Spec(unittest.TestCase):
    # Existing modal-policy tests own per-set/pricing validation. This owner
    # catches campaign files silently accepting e5, unknown keys or bad ranges.
    def test_validate_normalizes_baseline_and_rejects_unsafe_specs_before_network(self):
        import search_campaign
        import yaml
        example = pathlib.Path(__file__).parent / 'examples/search-campaign.yaml'
        value = yaml.safe_load(example.read_text())
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / 'campaign.yaml'
            path.write_text(yaml.safe_dump(value))
            with mock.patch('sys.stdout', new_callable=io.StringIO) as output:
                self.assertEqual(search_campaign.main(['validate', str(path)]), 0)
            parsed = json.loads(output.getvalue())
            self.assertEqual(parsed['policy']['baseline']['model'], 'Cohere-Embed-V5-Pro')
            self.assertEqual(parsed['policy']['baseline']['dimensions'], 1024)
            self.assertEqual(parsed['policy']['baseline']['dense_weight'], .5)
            private_spec = copy.deepcopy(value)
            from test_private_working import policy
            private_spec['policy']['sets'].update(policy()['sets'])
            private_spec['goal']['weights']['private-example'] = 2
            path.write_text(yaml.safe_dump(private_spec))
            with mock.patch('sys.stdout', new_callable=io.StringIO) as private_output:
                self.assertEqual(search_campaign.main(['validate', str(path)]), 0)
            self.assertEqual(json.loads(private_output.getvalue())['policy']['sets']['private-example']['input']['split'], 'working')
            private_spec['goal']['weights'].pop('private-example')
            with self.assertRaisesRegex(ValueError, 'eligible datasets'):
                search_campaign.specification(private_spec)
            path.write_text(yaml.safe_dump(value))
            for edit in (
                lambda s: s.update(secret='unsafe'),
                lambda s: s['policy']['baseline'].update(model='multilingual-e5-small (current)'),
                lambda s: s['space']['dense_weight'].update(low=2),
                lambda s: s.update(parallelism=0),
                lambda s: s['policy'].update(provider_total_usd=-1),
                lambda s: s['policy'].update(end_at='tomorrow'),
                lambda s: s['goal']['weights'].update(absent=1),
            ):
                bad = copy.deepcopy(value)
                edit(bad)
                with self.subTest(edit=edit):
                    with self.assertRaises((ValueError, PermissionError, KeyError)):
                        search_campaign.specification(bad)
            path.write_text('policy: [fixture-secret-must-not-reflect\n')
            with mock.patch('sys.stdout', new_callable=io.StringIO) as output:
                self.assertEqual(search_campaign.main(['validate', str(path)]), 2)
            self.assertNotIn('fixture-secret-must-not-reflect', output.getvalue())


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN') and importlib.util.find_spec('optuna'),
                     'requires Optuna and disposable PostgreSQL')
class Lifecycle(unittest.TestCase):
    def setUp(self):
        import uuid
        import yaml
        import psycopg
        import campaign_store
        self.value = yaml.safe_load((pathlib.Path(__file__).parent / 'examples/search-campaign.yaml').read_text())
        self.value['name'] = uuid.uuid4().hex
        self.dsn = os.environ['EVAL_CONTROL_TEST_DSN']
        with psycopg.connect(self.dsn) as db:
            db.execute((pathlib.Path(__file__).parents[2] / 'deploy/mlflow/eval-control.sql').read_text())
        self.store = campaign_store.CampaignStore(self.dsn)
        self.store.register(self.value, 'a' * 40, 'sha256:fixture')

    def test_cleanup_failure_preserves_report_and_watchdog_drains_before_next_trial(self):
        for failure in ('stop', 'slot-outage'):
            with self.subTest(failure=failure):
                self.assert_cleanup_recovery(failure)

    def assert_cleanup_recovery(self, failure):
        # Own the successful-result/cleanup ordering through the supervisor.
        # SQL, publication, aggregation, Optuna and watchdog are real; only
        # the Modal SDK/CLI, measured provider response and notifications are fake.
        import optuna
        import network_recovery
        import search_campaign
        import search_trial
        name = self.value['name']
        self.value.update(max_trials=2, parallelism=1)
        # Use a distinct frozen campaign because register forbids spec changes.
        import uuid
        name = self.value['name'] = uuid.uuid4().hex
        root = pathlib.Path(__file__).parent
        scorer = 'sha256:' + search_trial.digest({filename: (root / filename).read_text() for filename in
            ('scoring.py', 'gates.py', 'search_trial.py', 'embeddings.py', 'direct_bakeoff.py',
             'private_working.py', 'protected_inputs.py')})
        self.store.register(self.value, 'a' * 40, scorer)
        study = optuna.create_study(directions=['maximize', 'minimize', 'minimize'])
        study.enqueue_trial({'dense_weight': .2})
        study.enqueue_trial({'dense_weight': .8})
        apps, measurements, waits = {}, [], []
        fail_cleanup = True
        release_slot = control_store.Store.release_slot
        lost_release = []
        def release(store, campaign, slot):
            if failure == 'slot-outage' and not lost_release:
                lost_release.append(True)
                raise network_recovery.Outage('slot release unavailable')
            return release_slot(store, campaign, slot)
        def app(label):
            identity = 'ap-' + str(len(apps))
            apps[identity] = {'app_id': identity, 'description': label, 'state': 'running', 'tasks': 1}
            handle = mock.MagicMock(app_id=identity)
            handle.function.return_value = lambda _: mock.Mock()
            return handle
        def cli(command, **kwargs):
            if 'list' in command:
                return subprocess.CompletedProcess(command, 0, stdout=json.dumps(list(apps.values())))
            if 'stop' in command:
                if fail_cleanup:
                    raise subprocess.CalledProcessError(1, command, stderr=b'termination refused')
                apps[command[-1]].update(state='stopped', tasks=0)
                if len(apps) == 2:
                    # The watchdog/operator can fence this owner during cleanup.
                    self.store.stop(name)
            return subprocess.CompletedProcess(command, 0)
        def measured(remote, request, check):
            check()
            measurements.append(request)
            row = {'schema_version': 1, 'experiment': self.value['policy']['experiment'], 'git_sha': 'a' * 40,
                   'plugin_digest': 'sha256:fixture', 'config': request['config'], 'tier': 'direct',
                   'machine': 'fake-modal', 'duration_seconds': 1,
                   'dataset': {'name': request['dataset'], 'version': '1', 'split': 'dev',
                               'fingerprint': 'fixture', 'private': False},
                   'metrics': {'ndcg@10': .7, 'latency_p95_ms': 20, 'cost_per_search_usd': .0001,
                               'cost_per_1000_documents_usd': 1},
                   'cost': {'resource_class': 'fixture', 'latency_method': 'fixture',
                            'latency_sample': {'policy': 'sha256-query-id-v1; max=50',
                                              'query_ids': ['q1'], 'warmup_query_ids': ['q1']}},
                   'per_query': {'ndcg@10': {'q1': .7}}}
            baseline = {**row, 'config': {**request['policy']['baseline'], 'paired_side': 'baseline',
                        'paired_candidate_hash': search_trial.digest(request['config'])},
                        'metrics': {**row['metrics'], 'ndcg@10': .65}, 'per_query': {'ndcg@10': {'q1': .65}}}
            row['provenance'] = {'public_pair': {
                'baseline_lease_key': request['lease_key'] + '/' + request['owner'] + '/baseline'}}
            return search_trial.publish_pair(self.store, request, {'baseline': baseline, 'candidate': row})
        def retry(delay):
            nonlocal fail_cleanup
            waits.append(delay)
            self.assertEqual(len(apps), 1, 'cleanup must drain before the next app launches')
            status = search_campaign.public_status(self.store, name, study)
            report = status['trials'][0]['report']
            self.assertEqual(report['status'], 'rejected')  # One query cannot establish a corrected quality gain.
            self.assertTrue(report['gates']['no_loss']['passed'])
            self.assertEqual(len(report['evidence']), 6)
            expected_state = optuna.trial.TrialState.RUNNING if lost_release else optuna.trial.TrialState.COMPLETE
            self.assertEqual(study.trials[0].state, expected_state)
            for actual, expected in zip(search_campaign.objectives(report, self.value['goal']['weights']), [.7, .0001, 20]):
                self.assertAlmostEqual(actual, expected)
            self.assertTrue(status['cleanup_pending'])
            fail_cleanup = False
        with tempfile.TemporaryDirectory() as temp, \
             mock.patch.dict(os.environ, EVAL_CONTROL_DATABASE_URL=self.dsn, MLFLOW_TRACKING_URI=''), \
             mock.patch('modal.App', side_effect=app), mock.patch('modal.Image'), \
             mock.patch('modal.Secret'), mock.patch('modal.Volume'), mock.patch('modal_search.shipped_trial'), \
             mock.patch('modal_search.invoke', side_effect=measured), \
             mock.patch('subprocess.run', side_effect=cli), \
             mock.patch('subprocess.check_output', side_effect=lambda args, **kw: b'' if 'ls-files' in args else 'a' * 40), \
             mock.patch('control_store.Store.release_slot', autospec=True, side_effect=release), \
             mock.patch('campaign_reporting.Notifications.send'), mock.patch('time.sleep', side_effect=retry):
            if failure == 'slot-outage':
                with self.assertRaises(network_recovery.Outage):
                    search_campaign.supervise(self.store, name, study, temp)
                snapshot = self.store.snapshot(name)
                self.assertEqual(len(snapshot['trials']['0']['report']['evidence']), 6)
                # A restarted process gives up its old owner; no wall-clock expiry.
                self.store.release_owner(name, snapshot['owner'])
            status = search_campaign.supervise(self.store, name, study, temp)
        self.assertEqual(waits, [15])
        self.assertEqual(len(measurements), 6)
        self.assertEqual(len(apps), 2)
        self.assertFalse(status['cleanup_pending'])
        self.assertEqual(status['stopped'], 'operator stop')
        self.assertEqual(status['trials'][1]['report']['status'], 'rejected')
        self.assertEqual(len(status['trials'][1]['report']['evidence']), 6)
        self.assertTrue(all(r['status'] == 'closed' for r in self.store.snapshot(name)['resources'].values()))

    def test_busy_measurement_allocates_no_compute_intent(self):
        # Own intent lifecycle at the supervisor adapter; a busy slot should
        # leave nothing for the watchdog. Only Modal launch/termination is fake.
        import campaign_compute
        import campaign_store
        import network_recovery
        name = self.value['name']
        owner = self.store.acquire(name)
        slot_key = 'campaign-measurement-slot'
        slot = self.store.claim(name, slot_key)
        compute = mock.Mock()
        compute.running.return_value = False
        with tempfile.TemporaryDirectory() as temp:
            measure = campaign_compute.Measurement(self.store, name, owner, temp, compute)
            def launch(*args, **kwargs):
                if not admitted[0]:
                    return {'status': 'leased'}
                self.assertTrue(kwargs['app_name']((slot_key, slot['owner'])))
                if fail_setup[0] == 'outage':
                    raise network_recovery.Outage('control store unavailable')
                if fail_setup[0] is True:
                    # Known local setup failure before any AppCreate attempt.
                    with self.store.transaction() as db:
                        db.execute('UPDATE eval_control.campaign_runs SET expires_at=clock_timestamp() WHERE campaign=%s', (name,))
                    raise control_store.LeaseLost('setup lost its owner')
                kwargs['on_launch']()
                kwargs['on_app']('ap-tracked')
                if fail_setup[0] == 'measurement':
                    raise ValueError('original measurement failure')
                return {'status': 'rejected'}
            admitted, fail_setup = [False], [False]
            with mock.patch('modal_search.launch', side_effect=launch):
                self.assertEqual(measure({})['status'], 'leased')
                self.assertEqual(self.store.snapshot(name).get('resources', {}), {})
                compute.stop.assert_not_called()
                admitted[0] = True
                with self.store.transaction() as db:
                    db.execute('UPDATE eval_control.leases SET expires_at=clock_timestamp() WHERE campaign=%s AND key=%s', (name, slot_key))
                with self.assertRaises(control_store.LeaseLost):
                    measure({})
                self.assertEqual(self.store.snapshot(name).get('resources', {}), {})
                compute.stop.assert_not_called()
                slot = self.store.claim(name, slot_key)
                fail_setup[0] = 'outage'
                with mock.patch.object(self.store, 'abandon_intent', wraps=self.store.abandon_intent) as abandon:
                    with self.assertRaises(network_recovery.Outage):
                        measure({})
                    abandon.assert_not_called()
                resource = next(iter(self.store.snapshot(name)['resources'].values()))
                self.assertEqual(resource['status'], 'pending')
                self.store.abandon_intent(name, owner, resource['id'])
                fail_setup[0] = True
                with self.assertRaises(control_store.LeaseLost):
                    measure({})
                resources = list(self.store.snapshot(name)['resources'].values())
                self.assertEqual([r['status'] for r in resources], ['closed', 'closed'])
                compute.stop.assert_not_called()
                fail_setup[0] = False
                self.assertEqual(measure({})['status'], 'rejected')
            resources = list(self.store.snapshot(name)['resources'].values())
            self.assertEqual(len(resources), 3)
            self.assertTrue(all(r['status'] == 'closed' for r in resources))
            compute.stop.assert_called_once_with('ap-tracked')
            # Cleanup must not mask the error that explains an interrupted trial.
            fail_setup[0] = 'measurement'
            compute.stop.side_effect = campaign_store.CleanupPending('termination refused')
            with mock.patch('modal_search.launch', side_effect=launch):
                with self.assertRaisesRegex(ValueError, '^original measurement failure$'):
                    measure({})
            resources = list(self.store.snapshot(name)['resources'].values())
            self.assertEqual(sorted(r['status'] for r in resources), ['closed', 'closed', 'closed', 'running'])

    def test_network_gap_recovers_same_owner_and_watchdog_fences_cleanup_after_grace(self):
        import psycopg
        import search_campaign
        name = self.value['name']
        with mock.patch.dict(os.environ, EVAL_NETWORK_OUTAGE_SECONDS='600'):
            owner = self.store.acquire(name)
        resource = self.store.intent(name, owner)
        self.store.bind(name, owner, resource['id'], 'ap-survivor')
        connect = psycopg.connect
        with connect(self.dsn) as db:
            db.execute("UPDATE eval_control.campaign_runs SET expires_at=clock_timestamp()-interval '240 seconds' WHERE campaign=%s", (name,))
        compute = mock.Mock()
        with self.assertRaises(control_store.LeaseBusy):
            self.store.acquire(name)
        with self.assertRaises(control_store.LeaseLost):
            self.store.intent(name, owner)  # expired lease cannot admit a new app
        with self.assertRaises(control_store.LeaseLost):
            self.store.renew_owner(name, 'foreign-owner')
        attempts = [0]
        def reconnect(*args, **kwargs):
            attempts[0] += 1
            if attempts[0] <= 2:
                raise psycopg.OperationalError('network unavailable secret')
            return connect(*args, **kwargs)
        with mock.patch('psycopg.connect', side_effect=reconnect), \
             mock.patch.dict(os.environ, EVAL_NETWORK_OUTAGE_SECONDS='600'), \
             mock.patch('time.sleep') as wait:
            self.assertFalse(search_campaign.watchdog_once(self.store, name, compute)['stopped'])
        wait.assert_called()
        compute.stop.assert_not_called()
        self.store.renew_owner(name, owner)
        state = self.store.snapshot(name)
        self.assertTrue(state['live'])
        self.assertEqual(state['owner'], owner)
        self.assertEqual(state['resources'][resource['id']]['app_id'], 'ap-survivor')
        with connect(self.dsn) as db:
            db.execute("UPDATE eval_control.campaign_runs SET expires_at=clock_timestamp()-interval '721 seconds' WHERE campaign=%s", (name,))
        compute.find.return_value = ['ap-survivor']
        compute.running.return_value = False
        def stop(_):
            # Once cleanup starts, even a former owner cannot revive its fence.
            with self.assertRaises(control_store.LeaseLost):
                self.store.renew_owner(name, owner)
        compute.stop.side_effect = stop
        search_campaign.watchdog_once(self.store, name, compute)
        compute.stop.assert_called_once_with('ap-survivor')
        self.assertEqual(self.store.snapshot(name)['resources'][resource['id']]['status'], 'closed')
        replacement = self.store.acquire(name)
        self.assertNotEqual(replacement, owner)
        with self.assertRaises(control_store.LeaseLost):
            self.store.renew_owner(name, owner)

    def test_three_cache_validators_leave_watchdog_responsive_and_claims_unique(self):
        # SQL races alone miss locks held by the caller while reading a volume.
        # Hold all three actual trial readers; the watchdog must finish before
        # releasing them. Only provider I/O and unrelated scoring are fake.
        import hashlib
        import direct_bakeoff
        import search_campaign
        import search_trial
        name = self.value['name']
        self.store.acquire(name)
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture', 'dimensions': 2})
        identity = {k: cfg[k] for k in ('model', 'revision', 'dimensions', 'window_chars', 'overlap_chars')}
        identity['timing'] = 'local-compute-v2'
        prices = {'Cohere-Embed-V5-Fast': .08}
        ready = [threading.Event() for _ in range(3)]
        release = threading.Event()
        readers = threading.local()
        with tempfile.TemporaryDirectory() as temp:
            path = pathlib.Path(temp, 'cached.json')
            entry = {'vectors': [[1, 0]], 'tokens': 1, 'embedding_seconds': 0}
            path.write_text(json.dumps(entry))
            key = 'embedding/' + search_trial.digest({'config': identity, 'mode': 'document',
                'text_hash': hashlib.sha256(b'cached passage').hexdigest()})
            claim = self.store.claim(name, key)
            self.store.publish(name, key, claim['owner'], {'filename': path.name, 'digest': search_trial.digest(entry)})
            read_text = pathlib.Path.read_text
            def slow_read(cache_path, *args, **kwargs):
                if cache_path == path:
                    ready[readers.number].set()
                    if not release.wait(5):
                        raise AssertionError('watchdog did not release cache readers')
                return read_text(cache_path, *args, **kwargs)
            def respond(request, timeout):
                count = len(json.loads(request.data)['texts'])
                return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                    'meta': {'billed_units': {'input_tokens': count}}}).encode())
            def trial(number):
                readers.number = number
                claim = self.store.claim(name, 'trial/' + str(number))
                budget = control_store.Budget(self.store, name, ('trial/' + str(number), claim['owner']))
                client = direct_bakeoff.Hosted('https://example.com', 'fixture', budget, 'tiny', prices)
                client.opener.open = respond
                data = {'corpus': {'a': {'text': 'fresh passage ' + str(number)}, 'b': {'text': 'cached passage'}},
                        'queries': {'q': 'question ' + str(number)}, 'qrels': {'q': {'b': 1}}}
                dataset = {'name': 'tiny', 'version': '1', 'split': 'dev', 'private': False}
                return search_trial.measure(cfg, data, dataset, temp, budget, client, prices, .001, fresh_latency=False)
            scores = {'mean': {'ndcg@10': 1}, 'per_query': {}}
            with mock.patch.object(pathlib.Path, 'read_text', slow_read), mock.patch('scoring.score', return_value=scores):
                with ThreadPoolExecutor(max_workers=4) as pool:
                    futures = [pool.submit(trial, i) for i in range(3)]
                    try:
                        self.assertTrue(all(event.wait(2) for event in ready), 'three validators must enter cache reads')
                        watchdog = pool.submit(search_campaign.watchdog_once, self.store, name, mock.Mock())
                        self.assertFalse(watchdog.result(timeout=2)['stopped'])
                        # A second claimer cannot steal any reader's fresh entry.
                        with self.store.transaction() as db:
                            keys = [row[0] for row in db.execute("SELECT key FROM eval_control.leases WHERE campaign=%s AND key LIKE 'embedding/%%' AND payload IS NULL", (name,)).fetchall()]
                        self.assertEqual(len(keys), 3)
                        self.assertTrue(all(c['status'] == 'leased' for c in self.store.claim_many(name, keys).values()))
                    finally:
                        release.set()
                    self.assertEqual([f.result(timeout=5)['cost']['cache_hits'] for f in futures], [1, 1, 1])
            self.assertTrue(all(c['status'] == 'done' for c in self.store.claim_many(name, keys).values()))

    def test_watchdog_and_supervisor_retry_sql_contention_without_losing_ownership(self):
        # Real lock/statement timeouts must not become an unavailable-store
        # crash. Release the blocker at the retry wait, never by sleeping.
        import psycopg
        import search_campaign
        name = self.value['name']
        owner = self.store.acquire(name)
        done = self.store.claim(name, 'evidence')
        self.store.publish(name, 'evidence', done['owner'], {'canonical': True})
        connect = psycopg.connect
        for operation, timeout in (('watchdog', 'lock_timeout'), ('renew', 'statement_timeout'),
                                   ('claims', 'lock_timeout'), ('evidence', 'statement_timeout')):
            with self.subTest(operation=operation), connect(self.dsn) as blocker:
                blocker.execute('SELECT name FROM eval_control.campaigns WHERE name=%s FOR UPDATE', (name,))
                def bounded(*args, **kwargs):
                    kwargs['options'] = '-c statement_timeout=10000 -c ' + timeout + '=25'
                    return connect(*args, **kwargs)
                waits = []
                def backoff(delay):
                    waits.append(delay)
                    if len(waits) == 2:
                        blocker.rollback()
                with mock.patch('psycopg.connect', side_effect=bounded), mock.patch('time.sleep', side_effect=backoff):
                    if operation == 'watchdog':
                        status = search_campaign.watchdog_once(self.store, name, mock.Mock())
                        self.assertFalse(status['paused'])
                    elif operation == 'renew':
                        self.store.renew_owner(name, owner)
                    elif operation == 'claims':
                        claims = self.store.claim_many(name, iter(['first', 'second']))
                        self.assertEqual(set(claims), {'first', 'second'})
                        self.assertTrue(all(c['status'] == 'claimed' for c in claims.values()))
                    else:
                        self.assertEqual(self.store.evidence(name, iter(['evidence'])), {'evidence': {'canonical': True}})
                self.assertEqual(waits, [1, 2])
                state = self.store.snapshot(name)
                self.assertEqual(state['owner'], owner)
                self.assertTrue(state['live'])

    def test_signal_stops_compute_before_blocked_measurement_futures_drain(self):
        import signal
        for kind in (signal.SIGTERM, signal.SIGINT):
            with self.subTest(signal=kind):
                self.assert_signal_cleanup(kind)

    def test_persistent_contention_exhausts_the_sql_retry_window(self):
        import psycopg
        name = self.value['name']
        connect = psycopg.connect
        clock, waits = [0], []
        with connect(self.dsn) as blocker:
            blocker.execute('SELECT name FROM eval_control.campaigns WHERE name=%s FOR UPDATE', (name,))
            def bounded(*args, **kwargs):
                kwargs['options'] = '-c statement_timeout=10000 -c lock_timeout=25'
                return connect(*args, **kwargs)
            def backoff(delay):
                waits.append(delay)
                clock[0] += 31
                if len(waits) > 1:
                    self.fail('SQL retry window never returned control to the caller')
            with mock.patch('psycopg.connect', side_effect=bounded), mock.patch('time.monotonic', side_effect=lambda: clock[0]), \
                 mock.patch('time.sleep', side_effect=backoff):
                with self.assertRaises(control_store.Contention):
                    self.store.availability(name)
            self.assertEqual(waits, [1])
            clock[0], waits[:] = 0, []
            import search_campaign
            with mock.patch('psycopg.connect', side_effect=bounded), mock.patch('time.monotonic', side_effect=lambda: clock[0]), \
                 mock.patch('time.sleep', side_effect=backoff):
                status = search_campaign.supervise(self.store, name, None, '.', once=True)
            self.assertEqual(status['status'], 'retrying')
            self.assertEqual(waits, [1])
            for command, expected in ((['stop', name, '--allow-paid'], 2), (['status', name], 2),
                                      (['digest', name], 2), (['watchdog', name, '--allow-paid', '--once'], 0)):
                clock[0], waits[:] = 0, []
                with self.subTest(command=command), mock.patch.dict(os.environ, EVAL_CONTROL_DATABASE_URL=self.dsn, CI='false', GITHUB_ACTIONS='false'), \
                     mock.patch('psycopg.connect', side_effect=bounded), mock.patch('time.monotonic', side_effect=lambda: clock[0]), \
                     mock.patch('time.sleep', side_effect=backoff), mock.patch('sys.stdout', new_callable=io.StringIO) as output:
                    self.assertEqual(search_campaign.main(command), expected)
                    self.assertEqual(json.loads(output.getvalue())['status'], 'retrying')
                self.assertEqual(waits, [1])
            blocker.rollback()
            self.assertIsNone(self.store.availability(name)['stopped'])
            blocker.execute('SELECT name FROM eval_control.campaigns WHERE name=%s FOR UPDATE', (name,))
            clock[0], waits[:] = 0, []
            foreign_owner = []
            def takeover(delay):
                waits.append(delay)
                if delay == 7:
                    blocker.rollback()
                    if not foreign_owner:
                        foreign_owner.append(self.store.acquire(name))
                else:
                    clock[0] += 31
                if len(waits) > 2:
                    self.fail('supervisor silently retried another live owner')
            with mock.patch('psycopg.connect', side_effect=bounded), mock.patch('time.monotonic', side_effect=lambda: clock[0]), \
                 mock.patch('time.sleep', side_effect=takeover):
                with self.assertRaises(control_store.LeaseBusy):
                    search_campaign.supervise(self.store, name, None, '.', poll_seconds=7)
            self.assertEqual(waits, [1, 7])
            self.assertEqual(self.store.snapshot(name)['owner'], foreign_owner[0])
            self.store.release_owner(name, foreign_owner[0])
            self.store.stop(name)
            blocker.execute('SELECT name FROM eval_control.campaigns WHERE name=%s FOR UPDATE', (name,))
            clock[0], waits[:] = 0, []
            def recover(delay):
                waits.append(delay)
                if delay == 7:
                    blocker.rollback()
                else:
                    clock[0] += 31
                if len(waits) > 2:
                    self.fail('supervisor did not recover after contention cleared')
            with mock.patch('psycopg.connect', side_effect=bounded), mock.patch('time.monotonic', side_effect=lambda: clock[0]), \
                 mock.patch('time.sleep', side_effect=recover), mock.patch('campaign_reporting.Notifications.send'):
                recovered = search_campaign.supervise(self.store, name, None, '.', poll_seconds=7)
            self.assertEqual(recovered['stopped'], 'operator stop')
            self.assertEqual(waits, [1, 7])

    def assert_signal_cleanup(self, requested_signal):
        import threading
        import uuid
        import signal
        import psycopg
        import search_campaign
        with psycopg.connect(self.dsn) as db:
            db.execute((pathlib.Path(__file__).parents[2] / 'deploy/mlflow/optuna.sql').read_text())
        value = copy.deepcopy(self.value)
        value['name'] = uuid.uuid4().hex
        self.store.register(value, 'a' * 40, 'sha256:fixture')
        started, terminated = threading.Event(), threading.Event()
        handlers, canceled_before_drain = {}, []
        calls = 0
        lock = threading.Lock()
        class Compute:
            def find(self, label):
                return ['ap-fixture']
            def stop(self, app_id):
                terminated.set()
            def running(self, app_id):
                return not terminated.is_set()
        class Measurement:
            def __init__(self, store, name, owner, *_):
                self.store, self.name, self.owner = store, name, owner
            def __call__(self, config, **_):
                nonlocal calls
                with lock:
                    calls += 1
                    number = calls
                if number == 1:
                    # Fake external measurement has a known running app. The
                    # test owns signal/drain ordering, not app registration.
                    intent = self.store.intent(self.name, self.owner)
                    self.store.bind(self.name, self.owner, intent['id'], 'ap-fixture')
                    started.set()
                    canceled_before_drain.append(terminated.wait(2))
                    return {'status': 'failed'}
                if not started.wait(2):
                    raise AssertionError('blocking measurement did not start')
                # Propagates KeyboardInterrupt through the real executor, like
                # main-thread signal delivery, with another future still blocked.
                handlers.get(requested_signal, signal.default_int_handler)(requested_signal, None)
        with mock.patch.dict(os.environ, EVAL_CONTROL_DATABASE_URL=self.dsn,
                             EVAL_STUDY_DATABASE_URL=self.dsn, CI='false', GITHUB_ACTIONS='false'), \
             mock.patch('campaign_compute.ModalCompute', return_value=Compute()), \
             mock.patch('campaign_compute.Measurement', Measurement), \
             mock.patch('search_campaign.lineage', return_value=('a' * 40, 'sha256:fixture')), \
             mock.patch('signal.signal', side_effect=lambda kind, handler: handlers.update({kind: handler})), \
             mock.patch('sys.stdout', new_callable=io.StringIO) as output:
            self.assertEqual(search_campaign.main(['resume', value['name'], '--allow-paid', '--once']), 0)
        self.assertEqual(canceled_before_drain, [True], 'signal cleanup must precede executor drain')
        self.assertTrue(json.loads(output.getvalue())['stopped'])

    def test_takeover_requires_acknowledged_resource_cleanup_and_fences_old_owner(self):
        import campaign_store
        import control_store
        import psycopg
        owner = self.store.acquire(self.value['name'])
        intent = self.store.intent(self.value['name'], owner)
        with psycopg.connect(self.dsn) as db:
            db.execute("UPDATE eval_control.campaign_runs SET expires_at=clock_timestamp()-interval '3601 seconds' WHERE campaign=%s", (self.value['name'],))
        with self.assertRaises(campaign_store.CleanupPending):
            self.store.acquire(self.value['name'])
        # External Modal app exists, even though app-ID registration crashed.
        class Compute:
            fail = True
            visible = False
            def find(self, label):
                return ['ap-fixture'] if self.visible and label == intent['label'] else []
            def stop(self, app_id):
                if self.fail:
                    raise OSError('network unavailable')
            def running(self, app_id):
                return self.fail
        compute = Compute()
        # A missing creation acknowledgement cannot be resolved by waiting: a
        # timed-out creation RPC may still materialize an app after that window.
        with psycopg.connect(self.dsn) as db:
            db.execute("UPDATE eval_control.campaign_runs SET state=jsonb_set(state,%s,to_jsonb((clock_timestamp()-interval '1 second')::text)) WHERE campaign=%s",
                       (['resources', intent['id'], 'creation_deadline'], self.value['name']))
        with self.assertRaises(campaign_store.CleanupPending):
            campaign_store.cleanup(self.store, self.value['name'], compute)
        self.assertEqual(self.store.snapshot(self.value['name'])['resources'][intent['id']]['status'], 'pending')
        compute.visible = True
        with self.assertRaises(campaign_store.CleanupPending):
            campaign_store.cleanup(self.store, self.value['name'], compute)
        self.assertEqual(self.store.snapshot(self.value['name'])['resources'][intent['id']]['status'], 'pending')
        compute.fail = False
        campaign_store.cleanup(self.store, self.value['name'], compute)
        replacement = self.store.acquire(self.value['name'])
        self.assertNotEqual(owner, replacement)
        with self.assertRaises(control_store.LeaseLost):
            self.store.intent(self.value['name'], owner)
        with self.assertRaises(control_store.LeaseLost):
            self.store.trial(self.value['name'], owner, 0, {'status': 'complete'})
        self.store.stop(self.value['name'])
        with self.assertRaises(control_store.LeaseLost):
            self.store.intent(self.value['name'], replacement)

    def test_restart_replays_published_trial_before_tell_and_retains_pareto_points(self):
        import optuna
        import search_campaign
        optuna.logging.set_verbosity(optuna.logging.WARNING)
        study = optuna.create_study(directions=['maximize', 'minimize', 'minimize'])
        name = self.value['name']
        owner = self.store.acquire(name)
        calls = []
        scores = [(0.7, .001, 20), (0.8, .002, 30), (0.6, .003, 40)]
        def measurement(config, **_):
            i = len(calls)
            calls.append(config)
            q, c, l = scores[i]
            return {'status': 'rejected', 'verdict': 'rejected', 'gates': {},
                    'aggregate_sets': {s: {'candidate': {'ndcg_at_10': q,
                        'cost_per_search_usd': c, 'latency_p95_ms': l},
                        'baseline': {'ndcg_at_10': .65}} for s in self.value['policy']['sets']},
                    'samples': {'private-query-id': 'must never export'},
                    'work': {'set/candidate': {'receipt': {'result_key': 'a'*64, 'status':'synced', 'run_id':'opaque'}}}}
        loop = search_campaign.Loop(self.store, name, owner, study, measurement)
        # Simulate process failure after durable aggregate publication, before tell.
        with mock.patch.object(study, 'tell', side_effect=RuntimeError('interrupted')):
            with self.assertRaisesRegex(RuntimeError, 'interrupted'):
                loop.tick(limit=1)
        self.assertEqual(len(calls), 1)
        exported = self.store.snapshot(name)['trials']['0']['report']
        self.assertNotIn('private-query-id', json.dumps(exported))
        self.store.release_owner(name, owner)
        replacement = self.store.acquire(name)
        recovered = search_campaign.Loop(self.store, name, replacement, study, measurement)
        recovered.tick(limit=1)
        self.assertEqual(len(calls), 1)
        for actual, expected in zip(study.trials[0].values, [.7, .001, 20]):
            self.assertAlmostEqual(actual, expected)
        recovered.tick(limit=1)
        recovered.tick(limit=1)
        self.assertEqual(len(calls), 3)
        self.assertEqual([t.number for t in study.best_trials], [0, 1])
        # A stale loop cannot ask, persist or measure after takeover.
        with self.assertRaises(control_store.LeaseLost):
            loop.tick(limit=1)
        self.assertEqual(len(calls), 3)

    def test_persistent_study_reloads_and_watchdog_pauses_then_cleans_up(self):
        import psycopg
        import search_campaign
        import campaign_store
        with psycopg.connect(self.dsn) as db:
            db.execute((pathlib.Path(__file__).parents[2] / 'deploy/mlflow/optuna.sql').read_text())
        name = self.value['name']
        with search_campaign.open_study(name, self.dsn) as study:
            trial = study.ask()
            trial.suggest_float('dense_weight', .2, .8)
            study.tell(trial, [.8, .001, 20])
        with search_campaign.open_study(name, self.dsn) as study:
            self.assertEqual(study.trials[0].values, [.8, .001, 20])
            self.assertEqual(len(study.best_trials), 1)
        owner = self.store.acquire(name)
        intent = self.store.intent(name, owner)
        self.store.bind(name, owner, intent['id'], 'ap-fixture')
        class Compute:
            terminated = False
            def find(self, label):
                return ['ap-fixture']
            def stop(self, identity):
                self.terminated = True
            def running(self, identity):
                return not self.terminated
        compute = Compute()
        self.store.reserve(name, 'provider', .9)
        with self.assertRaises(embeddings.BudgetExceeded):
            self.store.reserve(name, 'provider', .2)
        status = search_campaign.watchdog_once(self.store, name, compute)
        self.assertTrue(status['paused'])
        self.assertTrue(compute.terminated)
        self.assertEqual(self.store.summary(name)['provider']['unknown_usd'], .9)
        # Advance the ledger day without sleeping. Next day is admissible but
        # prior uncertain charges continue to count against the total cap.
        with psycopg.connect(self.dsn) as db:
            db.execute('UPDATE eval_control.days SET day=day-1 WHERE campaign=%s', (name,))
            db.execute('UPDATE eval_control.reservations SET day=day-1 WHERE campaign=%s', (name,))
        self.assertFalse(self.store.availability(name)['paused'])
        replacement = self.store.acquire(name)
        self.assertNotEqual(owner, replacement)
        self.assertTrue(self.store.reserve(name, 'provider', .1))
