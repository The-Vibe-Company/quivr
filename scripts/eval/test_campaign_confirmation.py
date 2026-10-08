"""Native campaign bridge owner: real SQL/Git/Optuna; fake remote transports.

The existing confirmation owner covers protected loading and gate mathematics.
This owner catches missing native translation, forged native receipts, and
campaign restart/promotion mistakes that neither isolated side can observe.
"""
import copy
import functools
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
import urllib.parse
import uuid
from unittest import mock

import campaign_promotion
import campaign_reporting
import campaign_store
import engine_confirmation
import results
import search_campaign
import search_trial


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN') and importlib.util.find_spec('optuna')
                     and importlib.util.find_spec('scipy'), 'requires PostgreSQL, Optuna and scipy')
class NativeCampaign(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Promotion only reads the repository and runs its configure binary;
        # build and clone them once instead of per fixture.
        directory = tempfile.TemporaryDirectory()
        cls.addClassCleanup(directory.cleanup)
        cls.repository = pathlib.Path(directory.name) / 'repository'
        subprocess.run(['git', 'clone', '--quiet', '--local', str(search_campaign.ROOT), str(cls.repository)], check=True)
        cls.sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=cls.repository, text=True).strip()
        cls.binary = pathlib.Path(directory.name) / 'hosted-embed'
        subprocess.run(['go', 'build', '-o', str(cls.binary), '.'], cwd=cls.repository / 'plugins/hosted-embed', check=True)

    def setUp(self):
        import psycopg
        import yaml
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = pathlib.Path(self.directory.name)
        spec = yaml.safe_load((search_campaign.ROOT / 'scripts/eval/examples/search-campaign.yaml').read_text())
        self.name = spec['name'] = uuid.uuid4().hex
        spec['max_trials'] = 1
        spec['policy']['sets'] = {'scifact': {'split': 'dev'}}
        spec['goal']['weights'] = {'scifact': 1}
        self.dsn = os.environ['EVAL_CONTROL_TEST_DSN']
        with psycopg.connect(self.dsn) as db:
            db.execute((search_campaign.ROOT / 'deploy/mlflow/eval-control.sql').read_text())
            db.execute((search_campaign.ROOT / 'deploy/mlflow/optuna.sql').read_text())
        scorer = 'sha256:' + search_trial.digest({name: (self.repository / 'scripts/eval' / name).read_text()
            for name in ('scoring.py', 'gates.py', 'search_trial.py', 'embeddings.py', 'direct_bakeoff.py')})
        self.store = campaign_store.CampaignStore(self.dsn)
        self.store.register(spec, self.sha, scorer)
        self.owner = self.store.acquire(self.name)
        self.study = self.enterContext(search_campaign.open_study(self.name, self.dsn))
        self.study.ask()
        self.study.tell(0, [.6, .0001, 10])
        self.config = {**self.store.policy(self.name)['baseline'], 'dense_weight': .7}
        self.configuration = {
            'engine_runner_git_sha': self.sha,
            'heldout_family': {'sets': {'scifact': {'version': '1', 'split': 'heldout',
                'digest': 'd' * 64, 'fingerprint': 'e' * 64, 'private': False}}},
            'datasets': {'scifact': {'fingerprint': 'c' * 64, 'split_fingerprint': 'f' * 64}},
            'mapping_policy': {'production': {'ingestion': {'kind': 'hosted',
                'max_tokens_per_segment': 6144, 'overlap': 192, 'batch_size': 32,
                'max_batch_tokens': 196608, 'request_timeout_ms': 4000, 'call_budget_ms': 30000,
                'max_concurrent_requests': 16, 'max_retries': 2}, 'hybrid_fusion': 'relative_score'},
                'resources': {'experiment': spec['policy']['experiment'], 'modal_daily_usd': 10,
                              'modal_usd_per_second': .001}}}
        self.remote_runs, self.artifacts = {}, {}
        patch = mock.patch.dict(os.environ, MLFLOW_TRACKING_URI='https://results.example.invalid')
        patch.start()
        self.addCleanup(patch.stop)
        patch = mock.patch('urllib.request.OpenerDirector.open', side_effect=self.http)
        patch.start()
        self.addCleanup(patch.stop)
        self.rows, self.keys = {}, {}
        receipts = []
        policy = self.store.policy(self.name)
        scores = {'query-' + str(i): .3 + i / 100 for i in range(20)}
        sample = {'policy': 'sha256-query-id-v1; max=50',
                  'query_ids': sorted(scores, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q)),
                  'warmup_query_ids': [sorted(scores)[0]]}
        for side, config in (('baseline', policy['baseline']), ('candidate', self.config)):
            values = {q: v + (.1 + i / 10000 if side == 'candidate' else 0)
                      for i, (q, v) in enumerate(scores.items())}
            row = search_trial.record({'machine': 'same', 'duration_seconds': 1,
                'dataset': {'name': 'scifact', 'version': '1', 'split': 'dev', 'fingerprint': 'c' * 64, 'private': False},
                'per_query': {'ndcg@10': values}, 'metrics': {'ndcg@10': sum(values.values()) / 20,
                    'latency_p95_ms': 10, 'cost_per_search_usd': .0001, 'cost_per_1000_documents_usd': 1},
                'cost': {'resource_class': 'same', 'latency_method': 'fresh', 'latency_sample': sample}},
                {**config, 'campaign': self.name, 'campaign_policy_hash': search_trial.digest(policy),
                 'profile': 'default', 'fresh_latency': True}, policy['experiment'], self.sha, scorer)
            key = 'dev/' + side
            self.store.publish(self.name, key, self.store.claim(self.name, key)['owner'], row)
            self.rows[side], self.keys[side] = row, key
            receipts.append(results.Results(directory=self.path / 'outbox').log(row))
        report = {'status': 'exploration_finalist', 'gates': {g: {'passed': True} for g in campaign_promotion.GATES},
                  'aggregate_sets': {'scifact': {side: {'ndcg_at_10': .6, 'latency_p95_ms': 10,
                     'cost_per_search_usd': .0001} for side in self.rows}}, 'evidence': receipts}
        self.store.trial(self.name, self.owner, 0, {'config': self.config, 'report': report})
        self.apps, self.invocations = set(), []
        self.compute = mock.Mock()
        self.compute.stop.side_effect = self.apps.discard
        self.compute.running.side_effect = lambda app: app in self.apps
        parent = self
        class GitHub:
            def __init__(self):
                self.prs, self.calls = {}, 0
            def current_main(self, _):
                return parent.sha
            def find(self, branch):
                return self.prs.get(branch)
            def create(self, checkout, branch, title, body):
                changed = subprocess.check_output(['git', 'diff', '--name-only'], cwd=checkout, text=True).splitlines()
                parent.assertEqual(changed, [campaign_promotion.TARGET])
                parent.assertEqual(campaign_promotion.effective_settings(checkout)['retrieve']['dense_weight'], .7)
                self.calls += 1
                self.prs[branch] = {'url': 'https://github.com/example/engine/pull/1', 'body': body}
                return self.prs[branch]
        self.github = GitHub()
        self.builder = campaign_promotion.HostedManifestBuilder(binary=self.binary)
        # The engine source tree is unchanged during a test, and test_engine_confirmation
        # owns its lineage bytes; hash it once instead of on every native request.
        patch = mock.patch('engine_confirmation.lineage', functools.cache(engine_confirmation.lineage))
        patch.start()
        self.addCleanup(patch.stop)
        patch = mock.patch('campaign_promotion.HostedManifestBuilder', return_value=self.builder)
        patch.start()
        self.addCleanup(patch.stop)

    def http(self, request, **_):
        path = urllib.parse.urlsplit(request.full_url).path
        body = json.loads(request.data) if request.data else {}
        response = {}
        if path.endswith('experiments/search'):
            response = {'experiments': [{'experiment_id': '1', 'name': self.store.policy(self.name)['experiment']}]}
        elif path.endswith('runs/search'):
            selected = list(self.remote_runs.values())
            if " = '" in body.get('filter', ''):
                key = body['filter'].split(" = '")[1].rstrip("'")
                selected = [r for r in selected if r['data']['tags'][0]['value'] == key]
            response = {'runs': selected}
        elif path.endswith('runs/create'):
            rid = uuid.uuid4().hex
            self.remote_runs[rid] = {'info': {'run_id': rid, 'status': 'RUNNING', 'artifact_uri': 'mlflow-artifacts:/1/' + rid},
                                    'data': {'tags': body['tags'], 'params': [], 'metrics': []}}
            response = {'run': self.remote_runs[rid]}
        elif path.endswith('runs/log-batch'):
            self.remote_runs[body['run_id']]['data'].update(params=body['params'], metrics=body['metrics'])
        elif path.endswith('runs/update'):
            self.remote_runs[body['run_id']]['info']['status'] = body['status']
        elif '/mlflow-artifacts/' in path:
            if request.method == 'PUT':
                self.artifacts[path] = body
            else:
                response = self.artifacts[path]
        else:
            self.fail('unexpected MLflow route: ' + path)
        return io.BytesIO(json.dumps(response).encode())

    def transport(self, request, resource):
        def invoke(remote, renew):
            resource.app_name
            resource.on_app('ap-fixture')
            self.apps.add('ap-fixture')
            resource.check()
            renew()
            self.invocations.append(remote)
            ordinal = self.store.confirmation(self.name, (remote['lease_key'], remote['owner']))
            pairs = copy.deepcopy(self.rows)
            for row in pairs.values():
                row.update(tier='engine', dataset={'fingerprint': 'e' * 64})
            if getattr(self, 'reject', False):
                pairs['candidate'] = copy.deepcopy(pairs['baseline'])
            measured = engine_confirmation.aggregate({'scifact': pairs}, request, self.store.policy(self.name))
            return {**measured, 'read_ordinal': ordinal, 'remote_cleanup_verified': True,
                    'sandbox_terminated': True, 'compute_ids': {'app_id': 'ap-fixture', 'sandbox_id': 'sb-fixture'}}
        result = engine_confirmation.confirm(self.store, request, invoke, self.path / 'outbox')
        if getattr(self, 'tamper', None):
            self.tamper(result)
        return result

    def adapter(self):
        # Like supervise, one adapter serves every confirmation of a process.
        if not hasattr(self, 'native'):
            self.native = search_campaign.NativeConfirmation(self.store, self.name, self.configuration,
                                                             self.path / 'outbox', transport=self.transport)
        return self.native

    def advance(self):
        outcomes = search_campaign.advance_confirmations(self.store, self.name, self.owner, self.study,
            self.adapter(), repository=self.repository, compute=self.compute, github=self.github, builder=self.builder)
        return sum(r['status'] == 'opened' for r in outcomes)

    def test_native_receipt_replay_and_state_recovery_enable_only_confirmed_promotion(self):
        # A dominated finalist does not spend a protected read. Rejected fast
        # measurements can stay on the public leaderboard without displacing
        # eligible finalists from confirmation selection.
        report = copy.deepcopy(self.store.snapshot(self.name)['trials']['0']['report'])
        report['aggregate_sets']['scifact']['candidate']['ndcg_at_10'] = .4
        self.store.trial(self.name, self.owner, 1, {'config': self.config, 'report': report})
        report = copy.deepcopy(report)
        report['status'] = 'rejected'
        report['aggregate_sets']['scifact']['candidate']['ndcg_at_10'] = .9
        self.store.trial(self.name, self.owner, 2, {'config': self.config, 'report': report})
        self.assertEqual(self.advance(), 1)
        self.assertEqual(self.github.calls, 1)
        self.assertIn('quality=pass', next(iter(self.github.prs.values()))['body'])
        self.assertNotIn('1', self.store.snapshot(self.name)['confirmations'])
        receipt = self.store.snapshot(self.name)['confirmations']['0']['receipt']
        self.assertEqual(receipt['compute_ids'], ['ap-fixture'])
        self.assertEqual(receipt['bindings']['heldout_fingerprint'],
                         engine_confirmation.digest({'scifact': 'e' * 64}))
        self.assertEqual(self.invocations[0]['confirmation_request']['dev_evidence'], {'scifact': self.keys})
        self.assertEqual(self.invocations[0]['confirmation_request']['datasets'], self.configuration['datasets'])
        self.assertEqual(self.invocations[0]['confirmation_request']['candidate_request_limit'], 10)
        self.assertEqual(self.study.user_attrs['confirmations']['0']['status'], 'confirmed')
        self.assertEqual(search_campaign.public_status(self.store, self.name, self.study)['pareto'][0]['confirmation']['status'], 'confirmed')
        self.assertFalse(self.apps)
        # Losing campaign publication must replay canonical native SQL, including
        # registered closed app identity, without another admission or launch.
        with self.store.edit(self.name) as (_, state):
            state['confirmations'].clear()
        self.store = campaign_store.CampaignStore(self.dsn)
        self.store.release_owner(self.name, self.owner)
        # The default supervisor must recover configured finalists before its
        # exhausted study stops admission. Fake only remote/provider transports.
        with mock.patch('campaign_compute.ModalCompute', return_value=self.compute), \
             mock.patch('engine_confirmation.ModalAdapter', return_value=self.transport), \
             mock.patch('campaign_promotion.GitHub', return_value=self.github), \
             mock.patch('campaign_promotion.HostedManifestBuilder', return_value=self.builder), \
             mock.patch('campaign_reporting.Notifications.send', return_value='fake-ack'):
            status = search_campaign.supervise(self.store, self.name, self.study, self.path / 'outbox', once=True)
        self.assertEqual(status['confirmations'][0]['status'], 'confirmed')
        self.assertEqual(self.github.calls, 1)
        self.assertEqual(len(self.invocations), 1)
        self.assertEqual(self.store.availability(self.name)['confirmation_reads_left'], 9)
        with search_campaign.open_study(self.name, self.dsn) as restarted:
            self.assertEqual(restarted.user_attrs['confirmations']['0']['status'], 'confirmed')
        changed = copy.deepcopy(self.configuration)
        changed['datasets']['scifact']['split_fingerprint'] = '0' * 64
        with self.assertRaisesRegex(ValueError, 'immutable'):
            search_campaign.NativeConfirmation(self.store, self.name, changed, self.path / 'outbox')

    def test_nonwinning_native_outcomes_are_visible_and_never_promote(self):
        for outcome in ('rejected', 'unavailable'):
            with self.subTest(outcome=outcome):
                self.reject = outcome == 'rejected'
                if outcome == 'unavailable':
                    self.config['window_chars'] = 4000
                    self.store.trial(self.name, self.owner, 0, {**self.store.snapshot(self.name)['trials']['0'], 'config': self.config})
                    with self.store.edit(self.name) as (_, state):
                        state['confirmations'].clear()
                self.assertEqual(self.advance(), 0)
                state = self.store.snapshot(self.name)
                self.assertEqual(state['confirmations']['0']['status'], outcome)
                body = campaign_reporting.digest_body(state, self.store.availability(self.name), self.store.summary(self.name))
                self.assertIn('confirmation=' + outcome, body)
                self.assertEqual(self.study.user_attrs['confirmations']['0']['status'], outcome)
        self.assertEqual(len(self.invocations), 1)
        self.assertEqual(self.store.availability(self.name)['confirmation_reads_left'], 9)

    def test_native_binding_policy_cleanup_and_tracking_forgery_fail_closed(self):
        # The slowest native owner (about 10 s): each forgery is checked by a
        # different validator, so each row runs a full confirmation, and
        # production makes three full-tree Git checkouts per confirmation.
        mutations = (
            lambda r: r['bindings'].update(candidate_hash='0' * 64),
            lambda r: r.update(confirmation_policy_digest='0' * 64),
            lambda r: r.update(cleanup_verified=False),
            lambda r: r['compute_ids'].update(app_id='ap-unregistered'),
            lambda r: r['compute_ids'].update(sandbox_id=None),
            lambda r: r['receipts'][0].update(status='pending'),
            lambda r: r['receipts'][0].update(run_id='forged-run'),
        )
        patch = mock.patch('subprocess.run', wraps=subprocess.run)
        run = patch.start()
        self.addCleanup(patch.stop)
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                self.tamper = mutate
                with self.store.edit(self.name) as (_, state):
                    state.get('confirmations', {}).clear()
                self.assertEqual(self.advance(), 0)
                self.assertNotIn('receipt', self.store.snapshot(self.name)['confirmations']['0'])
                self.assertFalse(self.apps)
        self.assertEqual(len(self.invocations), 1)
        self.assertEqual(self.store.availability(self.name)['confirmation_reads_left'], 9)
        # Campaign, engine and production settings share one revision: one checkout per confirmation.
        clones = [c.args[0] for c in run.call_args_list if c.args[0][:2] == ['git', 'clone']]
        self.assertEqual(len(clones), len(mutations), clones)

    def test_production_settings_mismatch_is_unavailable_before_any_paid_dispatch(self):
        for field, value in (('batch_size', 8), ('max_tokens_per_segment', 512), ('hybrid_fusion', 'ranked')):
            with self.subTest(field=field):
                fixture = NativeCampaign()
                fixture.setUp()
                try:
                    production = fixture.configuration['mapping_policy']['production']
                    if field == 'hybrid_fusion':
                        production[field] = value
                    else:
                        production['ingestion'][field] = value
                    self.assertEqual(fixture.advance(), 0)
                    self.assertEqual(fixture.store.snapshot(fixture.name)['confirmations']['0']['status'], 'unavailable')
                    self.assertEqual(fixture.invocations, [])
                    self.assertEqual(fixture.store.snapshot(fixture.name)['resources'], {})
                    self.assertEqual(fixture.store.availability(fixture.name)['confirmation_reads_left'], 10)
                finally:
                    fixture.doCleanups()


if __name__ == '__main__':
    unittest.main()
