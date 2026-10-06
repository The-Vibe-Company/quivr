"""Lead/reporting owner: durable SQL state; only notification HTTP is fake."""
import copy
import importlib.util
import os
import pathlib
import unittest


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN') and importlib.util.find_spec('yaml'),
                     'requires disposable PostgreSQL and PyYAML')
class Reporting(unittest.TestCase):
    def setUp(self):
        import uuid
        import yaml
        import psycopg
        import campaign_store
        self.spec = yaml.safe_load((pathlib.Path(__file__).parent / 'examples/search-campaign.yaml').read_text())
        self.spec['name'] = uuid.uuid4().hex
        self.spec['max_trials'] = 2
        self.dsn = os.environ['EVAL_CONTROL_TEST_DSN']
        with psycopg.connect(self.dsn) as db:
            db.execute((pathlib.Path(__file__).parents[2] / 'deploy/mlflow/eval-control.sql').read_text())
        self.store = campaign_store.CampaignStore(self.dsn)
        self.store.register(self.spec, 'a' * 40, 'sha256:fixture')
        self.name = self.spec['name']

    def test_exact_receipts_survive_restarts_replay_and_conflicting_identity(self):
        import campaign_reporting as reporting
        import campaign_store
        self.assertIsNone(reporting.usage(self.store.snapshot(self.name)))
        receipt = {'provider': 'runtime', 'session': 'lead-1', 'turn': 'turn-1',
                   'input_tokens': 123, 'output_tokens': 17, 'cached_input_tokens': 30}
        reporting.ingest_usage(self.store, self.name, receipt)
        restarted = campaign_store.CampaignStore(self.dsn)
        reporting.ingest_usage(restarted, self.name, receipt)
        self.assertEqual(reporting.usage(restarted.snapshot(self.name)),
                         {'input_tokens': 123, 'output_tokens': 17, 'cached_input_tokens': 30, 'receipts': 1})
        with self.assertRaises(ValueError):
            reporting.ingest_usage(restarted, self.name, {**receipt, 'output_tokens': 18})
        for invalid in ({**receipt, 'cached_input_tokens': 124}, {**receipt, 'input_tokens': True},
                        {**receipt, 'estimate': True}):
            with self.assertRaises(ValueError):
                reporting.ingest_usage(restarted, self.name, invalid)
        # Late exact accounting remains possible after cleanup/stop.
        self.store.stop(self.name)
        reporting.ingest_usage(restarted, self.name, {**receipt, 'turn': 'turn-2', 'input_tokens': 200})
        self.assertEqual(reporting.usage(restarted.snapshot(self.name))['input_tokens'], 323)

    def test_lead_proposals_are_bounded_durable_and_executed_once(self):
        import campaign_reporting as reporting
        import search_campaign
        import optuna
        for bounds in ({'low': 0, 'high': 1}, {'low': 1, 'high': 1}):
            integer_bounds = copy.deepcopy(self.spec)
            integer_bounds['name'] += '-bounds-' + str(bounds['low'])
            integer_bounds['space']['dense_weight'] = bounds
            self.store.register(integer_bounds, 'a' * 40, 'sha256:fixture')
            reporting.propose(self.store, integer_bounds['name'], {
                'id': 'fractional-weight',
                'space': {'dense_weight': {'low': 0, 'high': 1}},
                'config': {**self.spec['policy']['baseline'], 'dense_weight': .7}})
        proposal = {'id': 'proposal-1', 'config': {**self.spec['policy']['baseline'], 'dense_weight': .7},
                    'next_plan': 'Confirm promising points after the next exploration wave.'}
        reporting.propose(self.store, self.name, proposal)
        reporting.propose(self.store, self.name, proposal)
        bad = copy.deepcopy(proposal)
        bad['config']['dense_weight'] = .9
        with self.assertRaises(ValueError):
            reporting.propose(self.store, self.name, {**bad, 'id': 'outside'})
        extension = {'id': 'proposal-2', 'space': {'dense_weight': {'low': .1, 'high': .9, 'step': .1}},
                     'config': bad['config']}
        reporting.propose(self.store, self.name, extension)
        reporting.propose(self.store, self.name, {'id': 'idea-1', 'idea': 'Try a different chunker in a future campaign.'})
        with self.assertRaises(ValueError):
            reporting.propose(self.store, self.name, {'id': 'unsafe', 'space': {'provider_daily_usd': {'low': 1, 'high': 2}}})
        observed = []
        def measure(config, **_):
            observed.append(config['dense_weight'])
            return {'status': 'failed'}
        study = optuna.create_study(directions=['maximize', 'minimize', 'minimize'])
        owner = self.store.acquire(self.name)
        loop = search_campaign.Loop(self.store, self.name, owner, study, measure)
        loop.tick(limit=2)
        self.assertEqual(sorted(observed), [.7, .9])
        # A new lead session cannot queue the same proposals a second time.
        search_campaign.Loop(self.store, self.name, owner, study, measure).tick(limit=1)
        self.assertEqual(len([t for t in study.trials if t.user_attrs.get('proposal_id')]), 2)
        state = self.store.snapshot(self.name)
        self.assertEqual(len(state['space_revisions']), 1)
        self.assertEqual(state['spec']['policy']['baseline']['dense_weight'], .5)
        self.assertEqual(state['next_plan'], proposal['next_plan'])
        self.assertEqual(search_campaign.public_status(self.store, self.name)['space']['dense_weight']['high'], .9)

    def test_daily_digest_retries_only_unacknowledged_destination_and_exports_aggregates(self):
        import io
        import json
        from unittest import mock
        import campaign_reporting as reporting
        import campaign_store
        import psycopg
        connect = psycopg.connect
        blocker = connect(self.dsn)
        waits = []
        def timeout_connect(*args, **kwargs):
            kwargs['options'] = '-c statement_timeout=10000 -c lock_timeout=25'
            return connect(*args, **kwargs)
        def backoff(delay):
            waits.append(delay)
            blocker.rollback()
        owner = self.store.acquire(self.name)
        report = {'status': 'exploration_finalist', 'verdict': 'better',
                  'gates': {k: {'passed': True} for k in ('quality', 'no_loss', 'latency', 'price')},
                  'aggregate_sets': {name: {'candidate': {'ndcg_at_10': .8, 'cost_per_search_usd': .002, 'latency_p95_ms': 30},
                                           'baseline': {'ndcg_at_10': .7, 'cost_per_search_usd': .003, 'latency_p95_ms': 40}}
                                     for name in self.spec['goal']['weights']},
                  'evidence': [], 'per_query': {'private-query-should-never-export': .8}}
        self.store.trial(self.name, owner, 0, {'config': self.spec['policy']['baseline'], 'report': report})
        lease = self.store.claim(self.name, 'digest-cost')
        self.store.reserve(self.name, 'provider', .25, lease=('digest-cost', lease['owner']))
        requests = []
        fail_slack = True
        name = self.name
        class Response(io.BytesIO):
            def __enter__(self):
                return self
            def __exit__(self, *_):
                self.close()
        class HTTP:
            def open(self, request, timeout):
                nonlocal fail_slack
                body = json.loads(request.data)
                requests.append((request.full_url, body))
                if 'slack.com' in request.full_url:
                    if fail_slack:
                        fail_slack = False
                        raise TimeoutError('private-response-must-not-export')
                    return Response(b'{"ok":true,"ts":"123.4"}')
                if 'commentCreate' not in body['query']:
                    return Response(b'{"data":{"comment":null}}')
                # Contend only after the transport has acknowledged delivery.
                blocker.execute('SELECT name FROM eval_control.campaigns WHERE name=%s FOR UPDATE', (name,))
                return Response(json.dumps({'data': {'commentCreate': {'success': True, 'comment': {'id': body['variables']['input']['id']}}}}).encode())
        with blocker, mock.patch.dict(os.environ, EVAL_LINEAR_TOKEN='fixture-linear', EVAL_SLACK_BOT_TOKEN='fixture-slack', EVAL_SLACK_CHANNEL='Cfixture'), \
             mock.patch('urllib.request.build_opener', return_value=HTTP()), \
             mock.patch('psycopg.connect', side_effect=timeout_connect), \
             mock.patch('control_store.time.sleep', side_effect=backoff):
            first = reporting.digest(self.store, self.name)
            self.assertEqual(first['linear']['status'], 'delivered')
            self.assertEqual(first['slack']['status'], 'retry')
            restarted = campaign_store.CampaignStore(self.dsn)
            second = reporting.digest(restarted, self.name)
            self.assertEqual(second['slack']['status'], 'delivered')
            reporting.digest(restarted, self.name)
        self.assertEqual(len(requests), 4)
        self.assertEqual(waits, [1])
        rendered = requests[1][1]['variables']['input']['body']
        self.assertIn('unknown', rendered)
        self.assertIn('0.25', rendered)
        self.assertIn('10', rendered)
        self.assertIn('+0.1', rendered)
        self.assertNotIn('private-query-should-never-export', rendered)
        import search_campaign
        self.assertNotIn('private-query-should-never-export', json.dumps(search_campaign.public_status(self.store, self.name)))
        self.assertNotIn('private-response-must-not-export', str(second))
        self.assertEqual(requests[2][1]['client_msg_id'], requests[3][1]['client_msg_id'])
