"""Campaign public CLI/loop owner: external measurement transports only are fake."""
import copy
import importlib.util
import io
import json
import os
import pathlib
import tempfile
import unittest
from unittest import mock

import control_store
import embeddings


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
            # No datastore/provider setup occurs when CI refuses live lifecycle.
            with mock.patch.dict(os.environ, CI='true'), mock.patch('sys.stderr', new_callable=io.StringIO):
                with self.assertRaises(SystemExit) as refused:
                    search_campaign.main(['start', str(path), '--allow-paid'])
            self.assertEqual(refused.exception.code, 2)
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

    def test_signal_stops_compute_before_blocked_measurement_futures_drain(self):
        import signal
        for kind in (signal.SIGTERM, signal.SIGINT):
            with self.subTest(signal=kind):
                self.assert_signal_cleanup(kind)

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
            def __call__(self, config):
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
            db.execute("UPDATE eval_control.campaign_runs SET expires_at=clock_timestamp()-interval '1 second' WHERE campaign=%s", (self.value['name'],))
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
        def measurement(config):
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
