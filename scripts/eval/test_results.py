"""Owner contracts for results persistence, privacy and MLflow transport (no network)."""
import copy
import importlib.util
import json
import os
import pathlib
import tempfile
import unittest
import io
import urllib.error
from unittest import mock

import results


def measurement(machine='laptop', quality=.7):
    return {'schema_version': 1, 'experiment': 'public/demo', 'git_sha': 'a' * 40,
            'plugin_digest': 'sha256:fixture', 'config': {'model': 'example', 'dimensions': 2},
            'dataset': {'name': 'example', 'version': '1', 'split': 'dev', 'fingerprint': 'fixture-v1',
                        'private': False}, 'tier': 'direct', 'machine': machine, 'duration_seconds': 2,
            'cost': {'example': {'tokens': 20, 'usd': .02, 'modal_seconds': 0}},
            'metrics': {'ndcg@10': quality, 'cost_per_search_usd': .01, 'latency_p95_ms': 5},
            'per_query': {'ndcg@10': {'q1': .5, 'q2': .9}}}


class Offline(unittest.TestCase):
    def setUp(self):
        names = ['MLFLOW_TRACKING_URI', 'MLFLOW_TRACKING_USERNAME', 'MLFLOW_TRACKING_PASSWORD',
                 'MLFLOW_PRIVATE_TRACKING_URI', 'MLFLOW_PRIVATE_TRACKING_USERNAME', 'MLFLOW_PRIVATE_TRACKING_PASSWORD']
        patch = mock.patch.dict(os.environ, {name: '' for name in names})
        patch.start()
        self.addCleanup(patch.stop)


class Results(Offline):
    def test_offline_results_survive_restart_and_repeated_logging(self):
        # No existing eval test owns the durable cross-process results contract.
        with tempfile.TemporaryDirectory() as temp:
            store = results.Results(directory=pathlib.Path(temp))
            receipt = store.log(measurement())
            self.assertEqual(receipt['status'], 'pending')
            again = results.Results(directory=pathlib.Path(temp))
            self.assertEqual(again.log(measurement())['result_key'], receipt['result_key'])
            listed = again.list('public/demo')
            self.assertEqual(len(listed), 1)
            self.assertEqual(listed[0]['metrics']['ndcg_at_10'], .7)
            self.assertEqual(listed[0]['config']['dimensions'], 2)
            self.assertEqual(listed[0]['machine'], 'laptop')
            self.assertNotIn('per_query', listed[0])
            self.assertEqual(again.get(receipt['result_key'], queries=True)['per_query']['ndcg_at_10']['q2'], .9)
            self.assertEqual(next(pathlib.Path(temp).rglob('*.json')).stat().st_mode & 0o777, 0o600)

    @unittest.skipUnless(importlib.util.find_spec('scipy'), 'paired comparison needs scipy')
    def test_comparison_pairs_exact_queries_and_pareto_keeps_real_tradeoffs(self):
        with tempfile.TemporaryDirectory() as temp:
            store = results.Results(directory=temp)
            base = measurement()
            candidate = measurement('rented', .8)
            candidate['config']['model'] = 'candidate'
            candidate['metrics']['latency_p95_ms'] = 8
            candidate['per_query']['ndcg@10'] = {'q1': .7, 'q2': .9}
            weak = measurement('agent', .6)
            weak['config']['model'] = 'weak'
            weak['metrics']['latency_p95_ms'] = 6
            base_key = store.log(base)['result_key']
            candidate_key = store.log(candidate, baseline=base_key)['result_key']
            keys = [base_key, candidate_key, store.log(weak)['result_key']]
            self.assertAlmostEqual(store.get(candidate_key)['comparison']['paired']['ndcg_at_10']['delta'], .1)
            comparison = store.compare(keys[1], keys[0])
            self.assertAlmostEqual(comparison['paired']['ndcg_at_10']['delta'], .1)
            self.assertEqual(comparison['paired']['ndcg_at_10']['queries'], 2)
            self.assertEqual({v['machine'] for v in store.leaderboard('public/demo', 'quality,cost,latency')},
                             {'laptop', 'rented'})
            changed_set = copy.deepcopy(candidate)
            changed_set['dataset']['fingerprint'] = 'different'
            other = store.log(changed_set)['result_key']
            with self.assertRaisesRegex(ValueError, 'dataset'):
                store.compare(other, keys[0])
            candidate['per_query']['ndcg@10'].pop('q2')
            candidate['config']['model'] = 'missing-query'
            missing = store.log(candidate)['result_key']
            with self.assertRaisesRegex(ValueError, 'query IDs'):
                store.compare(missing, keys[0])


class Upload(Offline):
    def test_outage_then_partial_upload_replay_finishes_one_run(self):
        # Fake only HTTP; independent MLflow wire responses fix the remote run id.
        with tempfile.TemporaryDirectory() as temp:
            store = results.Results(directory=pathlib.Path(temp) / 'outbox', tracking_uri='https://example.com')
            outage = urllib.error.URLError('sensitive transport detail')
            large = measurement()
            large['config']['large_setting'] = 'x' * 7000
            with mock.patch('urllib.request.OpenerDirector.open', side_effect=outage):
                receipt = store.log(large)
            self.assertEqual(receipt['status'], 'pending')
            run = {'info': {'run_id': 'remote-a', 'status': 'RUNNING',
                            'artifact_uri': 'mlflow-artifacts:/1/remote-a/artifacts'},
                   'data': {'tags': {}, 'params': {}, 'metrics': {}}}
            responses = [{'experiments': [{'experiment_id': '1'}]}, {'runs': []}, {'run': run}, {}, outage]
            sent = []
            def reply(request, timeout):
                sent.append((request.method, request.full_url, request.data))
                response = responses.pop(0)
                if isinstance(response, Exception):
                    raise response
                return io.BytesIO(json.dumps(response).encode())
            with mock.patch('urllib.request.OpenerDirector.open', side_effect=reply):
                self.assertEqual(store.sync()[0]['status'], 'pending')
            self.assertEqual(sum('/runs/create' in url for _, url, _ in sent), 1)
            responses[:] = [{'experiments': [{'experiment_id': '1'}]}, {'runs': [run]}, {}, {}, {}, {}]
            sent.clear()
            with mock.patch('urllib.request.OpenerDirector.open', side_effect=reply):
                finished = store.sync()[0]
            self.assertEqual(finished['status'], 'synced')
            self.assertEqual(finished['run_id'], 'remote-a')
            self.assertFalse(any('/runs/create' in url for _, url, _ in sent))
            batch = json.loads(next(body for _, url, body in sent if '/runs/log-batch' in url))
            metrics = {v['key']: v['value'] for v in batch['metrics']}
            self.assertEqual(metrics['ndcg_at_10'], .7)
            self.assertEqual(metrics['config.dimensions'], 2)
            self.assertTrue(all(len(p['value']) <= 6000 for p in batch['params']))
            artifact = json.loads(next(body for method, url, body in sent
                                       if method == 'PUT' and url.endswith('/record.json')))
            self.assertEqual(artifact['config']['large_setting'], 'x' * 7000)
            self.assertEqual(json.loads(next(body for method, url, body in sent
                                            if method == 'PUT' and url.endswith('/per_query.json'))),
                             {'ndcg_at_10': {'q1': .5, 'q2': .9}})

    def test_permission_denial_is_not_an_offline_success(self):
        with tempfile.TemporaryDirectory() as temp:
            store = results.Results(directory=temp, tracking_uri='https://example.com')
            for code in (401, 403):
                error = urllib.error.HTTPError('https://example.com', code, 'secret response', {}, io.BytesIO(b'sensitive'))
                with self.subTest(code=code), mock.patch('urllib.request.OpenerDirector.open', side_effect=error):
                    with self.assertRaisesRegex(PermissionError, 'MLflow authorization failed'):
                        store.log(measurement())

    def test_remote_run_response_and_query_artifact_are_read_and_cached(self):
        with tempfile.TemporaryDirectory() as temp:
            value = measurement()
            value.pop('per_query')
            value['result_key'] = 'b' * 64
            value['per_query_status'] = 'available'
            run = {'info': {'run_id': 'remote-id', 'status': 'FINISHED',
                            'artifact_uri': 'mlflow-artifacts:/1/remote-id/artifacts'},
                   'data': {'params': [{'key': k, 'value': json.dumps(v)} for k, v in value.items() if k != 'metrics'],
                            'metrics': [{'key': 'ndcg_at_10', 'value': .7}],
                            'tags': [{'key': 'quivr.result_key', 'value': 'b' * 64}]}}
            def reply(request, timeout):
                if '/experiments/search' in request.full_url:
                    body = {'experiments': [{'experiment_id': '1', 'name': 'public/demo'}]}
                elif '/runs/search' in request.full_url:
                    body = {'runs': [run]}
                elif request.full_url.endswith('/record.json'):
                    body = value
                else:
                    body = {'ndcg_at_10': {'q1': .5, 'q2': .9}}
                return io.BytesIO(json.dumps(body).encode())
            store = results.Results(directory=pathlib.Path(temp) / 'outbox', tracking_uri='https://example.com')
            with mock.patch('urllib.request.OpenerDirector.open', side_effect=reply):
                self.assertEqual(store.list('public/demo')[0]['run_id'], 'remote-id')
                self.assertEqual(store.get('remote-id', queries=True)['per_query']['ndcg_at_10']['q1'], .5)
            offline = results.Results(directory=store.directory)
            self.assertEqual(offline.list('public/demo')[0]['metrics']['ndcg_at_10'], .7)
            # Full artifacts remain readable even when legacy params were truncated.
            run['data']['params'].append({'key': 'config', 'value': '{"setting":"' + 'x' * 6000})
            with mock.patch('urllib.request.OpenerDirector.open', side_effect=reply):
                self.assertEqual(store.list()[0]['config']['dimensions'], 2)
                value['result_key'] = '../../escaped'
                with self.assertRaisesRegex(ValueError, 'result key'):
                    store.list()
            self.assertFalse((store.directory.parent / 'escaped.json').exists())


class Privacy(Offline):
    def test_private_arrays_never_reach_public_store_or_reader(self):
        with tempfile.TemporaryDirectory() as temp:
            public, private = pathlib.Path(temp) / 'public', pathlib.Path(temp) / 'private'
            with self.assertRaisesRegex(ValueError, 'outside the repository'):
                results.Results(directory=public, private_directory=results.ROOT)
            value = measurement()
            value['dataset']['private'] = True
            value['per_query'] = {'ndcg@10': {'private-query-sentinel': .9}}
            store = results.Results(directory=public, private_directory=private)
            receipt = store.log(value)
            self.assertNotIn('private-query-sentinel', ''.join(p.read_text() for p in public.rglob('*.json')))
            self.assertNotIn('private-query-sentinel', json.dumps(store.list('public/demo')))
            with self.assertRaises(PermissionError):
                store.get(receipt['result_key'], queries=True)
            # Even a remote outage may not quietly send arrays using fleet credentials.
            remote = results.Results(directory=public, private_directory=private,
                                     tracking_uri='https://example.com')
            with mock.patch('urllib.request.OpenerDirector.open') as network:
                with self.assertRaisesRegex(PermissionError, 'private credentials'):
                    remote.sync()
            network.assert_not_called()
            self.assertTrue(any('private-query-sentinel' in p.read_text() for p in private.glob('*.json')))
            headers = {'MLFLOW_PRIVATE_TRACKING_URI': 'https://example.com',
                       'MLFLOW_PRIVATE_TRACKING_USERNAME': 'private-writer',
                       'MLFLOW_PRIVATE_TRACKING_PASSWORD': 'fixture-only-password'}
            sent, namespaces = [], {}
            def reply(request, timeout):
                body = json.loads(request.data) if request.data else {}
                private_request = 'Authorization' in request.headers
                sent.append((private_request, request.full_url, body))
                if '/experiments/search' in request.full_url:
                    eid = '2' if private_request else '1'
                    namespaces[eid] = body['filter']
                    response = {'experiments': [{'experiment_id': eid}]}
                elif '/runs/search' in request.full_url:
                    response = {'runs': []}
                elif '/runs/create' in request.full_url:
                    eid = body['experiment_id']
                    response = {'run': {'info': {'run_id': 'run-' + eid, 'status': 'RUNNING',
                                                 'artifact_uri': 'mlflow-artifacts:/' + eid + '/artifacts'}}}
                else:
                    response = {}
                return io.BytesIO(json.dumps(response).encode())
            with mock.patch.dict('os.environ', headers), mock.patch('urllib.request.OpenerDirector.open', side_effect=reply):
                authorized = results.Results(directory=public, private_directory=private,
                                             tracking_uri='https://example.com')
                self.assertEqual(authorized.sync()[0]['status'], 'synced')
            self.assertIn('private/public/demo', namespaces['2'])
            self.assertFalse(any('private-query-sentinel' in json.dumps(body)
                                 for is_private, _, body in sent if not is_private))
            self.assertTrue(any('private-query-sentinel' in json.dumps(body)
                                for is_private, _, body in sent if is_private))
            self.assertFalse(any('/experiments/create' in url for _, url, _ in sent))
            # ACL filtering can hide an unauthorized private experiment instead of 403.
            with mock.patch.dict('os.environ', headers):
                authorized = results.Results(directory=public, private_directory=private,
                                             tracking_uri='https://example.com')
            with mock.patch('urllib.request.OpenerDirector.open', side_effect=lambda *_a, **_k: io.BytesIO(b'{"experiments":[]}')):
                with self.assertRaisesRegex(PermissionError, 'private'):
                    authorized.get(receipt['result_key'], queries=True)
            with mock.patch('urllib.request.OpenerDirector.open', side_effect=urllib.error.URLError('offline')):
                self.assertEqual(authorized.get(receipt['result_key'], queries=True)['per_query'],
                                 {'ndcg_at_10': {'private-query-sentinel': .9}})



class Import(Offline):
    def test_engine_report_ingestion_preserves_scores_cost_and_private_default(self):
        # The engine report writer owns measurement; this owner covers conversion only.
        report = {'status': 'completed', 'run': {'source_revision': 'c' * 40, 'host': {'machine': 'x86_64'},
                  'duration_seconds': 12}, 'sets': {'tiny': {'status': 'completed', 'queries': 2,
                  'documents': 10, 'manifest': {'fingerprint': 'tiny-v1', 'sample': {'split': 'dev'}},
                  'systems': {'hybrid/default': {'profile_version': 'default-v1', 'scored_limit': 10,
                  'mean': {'ndcg@10': .7}, 'per_query': {'ndcg@10': {'q1': .5, 'q2': .9}},
                  'latency_ms': {'p50': 3, 'p95': 5}, 'reranker': None,
                  'accounting': {'scored': {'cost_cents_per_search': .2, 'actual_input_tokens': 40}}}}}}}
        lineage = {'machine': 'worker-a', 'plugin_digest': 'sha256:engine',
                   'config': {'embedding_model': 'example', 'dimensions': 2, 'chunking': {'size': 64}}}
        private = list(results.engine_records(report, 'public/engine', lineage))[0]
        self.assertTrue(private['dataset']['private'])
        with tempfile.TemporaryDirectory() as temp:
            path = pathlib.Path(temp) / 'report.json'
            path.write_text(json.dumps(report))
            metadata = pathlib.Path(temp) / 'lineage.json'
            metadata.write_text(json.dumps(lineage))
            outbox = pathlib.Path(temp) / 'outbox'
            with mock.patch('sys.stdout', new_callable=io.StringIO):
                self.assertEqual(results.main(['--directory', str(outbox), 'log-engine', str(path),
                    '--experiment', 'public/engine', '--lineage', str(metadata), '--public-set', 'tiny']), 0)
            store = results.Results(directory=outbox)
            row = store.list()[0]
            self.assertFalse(row['dataset']['private'])
            self.assertEqual(row['tier'], 'engine')
            self.assertEqual(row['config']['chunking'], {'size': 64})
            self.assertEqual(row['machine'], 'worker-a')
            self.assertEqual(row['dataset']['split'], 'dev')
            self.assertEqual(row['metrics']['cost_per_search_usd'], .002)
            self.assertEqual(row['metrics']['latency_p95_ms'], 5)
            self.assertEqual(store.get(row['result_key'], queries=True)['per_query']['ndcg_at_10']['q2'], .9)
        report['compare'] = {'source_revision': 'd' * 40, 'sets': copy.deepcopy(report['sets'])}
        report['compare']['sets']['tiny']['systems']['hybrid/default']['mean']['ndcg@10'] = .6
        lineage['baseline'] = {'plugin_digest': 'sha256:baseline', 'config': {'dimensions': 4}}
        both = list(results.engine_records(report, 'public/engine', lineage, ['tiny']))
        self.assertEqual(len(both), 2)
        self.assertEqual({r['git_sha'] for r in both}, {'c' * 40, 'd' * 40})
        self.assertEqual(both[1]['metrics']['ndcg@10'], .6)
        self.assertEqual(both[1]['config']['dimensions'], 4)
        self.assertEqual(both[1]['machine'], 'worker-a')
        self.assertFalse(both[1]['dataset']['private'])
        lineage.pop('baseline')
        unknown_base = list(results.engine_records(report, 'public/engine', lineage, ['tiny']))[1]
        self.assertIsNone(unknown_base['plugin_digest'])
        self.assertNotIn('dimensions', unknown_base['config'])
        report['status'] = 'failed'
        with self.assertRaisesRegex(ValueError, 'completed'):
            list(results.engine_records(report, 'public/engine', lineage))

    def test_historical_import_preserves_means_and_marks_missing_and_pooled_data(self):
        directory = pathlib.Path(__file__).resolve().parents[2] / 'docs/dated/evidence/2026-10-03-embedding-comparison'
        rows = list(results.import_evidence(directory, 'public/historical'))
        self.assertEqual(len(rows), 15)
        pro = next(r for r in rows if r['config']['model'] == 'Cohere-Embed-V5-Pro'
                   and r['provenance']['file'] == 'miracl-fr.json')
        self.assertEqual(pro['metrics']['ndcg@10'], .777668727242398)
        self.assertEqual(pro['cost']['provider']['usd'], .0745)
        self.assertEqual(pro['per_query'], {})
        self.assertIsNone(pro['git_sha'])
        self.assertIsNone(pro['dataset']['fingerprint'])
        reduced = next(r for r in rows if r['config']['model'] == 'Cohere-Embed-V5-Pro-1024')
        self.assertIsNone(reduced['cost']['provider']['usd'])
        self.assertEqual(reduced['provenance']['pooled_cost']['usd'], .149)
        self.assertIsNone(reduced['metrics']['cost_per_search_usd'])
        self.assertIsNone(reduced['metrics']['latency_p95_ms'])
        modern = json.loads((directory / 'miracl-fr.json').read_text())
        modern.update(sample={'version': 'release-1', 'split': 'dev', 'tier': 'restricted'}, promotion_eligible=False)
        modern_row = next(results.direct_records(modern, 'public/new-registry'))
        self.assertEqual(modern_row['dataset']['version'], 'release-1')
        self.assertFalse(modern_row['provenance']['promotion_eligible'])
        self.assertEqual(modern_row['provenance']['dataset_tier'], 'restricted')
        with tempfile.TemporaryDirectory() as temp:
            store = results.Results(directory=temp)
            before = {p.name: p.read_bytes() for p in directory.iterdir()}
            for _ in range(2):
                with mock.patch('sys.stdout', new_callable=io.StringIO):
                    self.assertEqual(results.main(['--directory', temp, 'import', str(directory),
                                                  '--experiment', 'public/historical']), 0)
            self.assertEqual({p.name: p.read_bytes() for p in directory.iterdir()}, before)
            self.assertEqual(len(store.list('public/historical')), 15)


if __name__ == '__main__':
    unittest.main()
