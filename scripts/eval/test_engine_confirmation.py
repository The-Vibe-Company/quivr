"""Confirmation owners: effective mapping, protected admission and exports.

Mapping regression: passing character values as token limits or replacing an
unsupported reranker must refuse before any measurement. No existing dev or
smoke owner observes the cross-tier mapping. Exercise the adapter's real mapper.
"""
import unittest
import asyncio
import copy
import io
import os
import uuid
import urllib.parse
import hashlib
import importlib.util
import json
import pathlib
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock

import embeddings
import run

import engine_confirmation as confirmation
import search_trial
import engine_measurement as measurement
import trec
import control_store
import modal_search
import results


class Mapping(unittest.TestCase):
    def test_trusted_lineage_binds_ci_admission_source_bytes(self):
        path = 'scripts/eval/ci_guard.py'
        hashes = confirmation.lineage()['engine_source_hashes']
        self.assertEqual(hashes[path], hashlib.sha256((confirmation.ROOT / path).read_bytes()).hexdigest())

    def test_maps_real_settings_and_refuses_changed_character_windows(self):
        baseline = search_trial.configuration({'model': 'Cohere-Embed-V5-Pro',
            'revision': '2026-10-03', 'dimensions': 1024})
        production = {'ingestion': {'kind': 'hosted', 'max_tokens_per_segment': 512,
                                   'overlap': 48, 'batch_size': 16, 'max_batch_tokens': 8192,
                                   'request_timeout_ms': 4000, 'call_budget_ms': 30000,
                                   'max_concurrent_requests': 4, 'max_retries': 2}, 'hybrid_fusion': 'relative_score'}
        candidate = {**baseline, 'dense_weight': .3, 'candidate_count': 100,
                     'model': 'Cohere-Embed-V5-Fast'}
        mapped = confirmation.mapping(candidate, baseline, production, candidate=True)
        self.assertEqual(mapped['retrieve'], {'dense_weight': .3, 'candidate_count': 100,
                                             'hybrid_fusion': 'ranked'})
        self.assertEqual(mapped['ingestion']['max_tokens_per_segment'], 512)
        self.assertEqual(mapped['ingestion']['model'], 'Cohere-Embed-V5-Fast')
        for field in confirmation.HOSTED_EXECUTION:
            self.assertEqual(mapped['ingestion'][field], production['ingestion'][field])
        self.assertEqual(mapped['mode'], 'hybrid')
        self.assertEqual(confirmation.mapping(baseline, baseline, production)['retrieve']['hybrid_fusion'], 'relative_score')
        for change, reason in [({'window_chars': 2400}, 'character_segmentation'),
                               ({'overlap_chars': 100}, 'character_segmentation'),
                               ({'candidate_count': 101}, 'candidate_depth'),
                               ({'reranker': 'jev'}, 'reranker'),
                               ({'revision': '.bad'}, 'model_revision'),
                               ({'revision': '-bad'}, 'model_revision')]:
            with self.subTest(change=change), self.assertRaisesRegex(confirmation.Unmappable, reason):
                confirmation.mapping({**candidate, **change}, baseline, production, candidate=True)

    def test_command_previews_without_keys(self):
        with mock.patch.dict(os.environ, {}, clear=True), mock.patch('sys.stdout', new_callable=io.StringIO) as output:
            self.assertEqual(confirmation.main(['--dry-run']), 0)
        self.assertFalse(json.loads(output.getvalue())['confirmation_available'])


class ProtectedInputs(unittest.TestCase):
    def test_checks_full_family_content_and_disjoint_queries_before_measurement(self):
        # Frozen split membership has no owner in the dev-only loader. Check
        # real file bytes, including duplicate text with a different query ID.
        with tempfile.TemporaryDirectory() as temp:
            dev, held = pathlib.Path(temp) / 'dev', pathlib.Path(temp) / 'held'
            corpus = {'d': {'text': 'neutral record'}}
            trec.write(dev, corpus, {'dev': 'working question'}, {'dev': {'d': 1}})
            trec.write(held, corpus, {'test': 'unseen question'}, {'test': {'d': 1}})
            req = {'heldout_family': {'set': {'private': False, 'digest': trec.fingerprint(held), 'fingerprint': trec.fingerprint(held)}},
                   'datasets': {'set': {'fingerprint': trec.fingerprint(dev), 'split_fingerprint': measurement.split_identity(dev)[0]}}}
            refs = {'set': {'heldout': str(held), 'dev': str(dev)}}
            self.assertEqual(set(measurement.inputs(req, refs, temp)['set']['queries']), {'test'})
            req['heldout_family']['set']['private'] = True
            with self.assertRaisesRegex(PermissionError, 'key and provider consent'):
                measurement.inputs(req, refs, temp)
            req['heldout_family']['set']['private'] = False
            trec.write(held, corpus, {'test': 'working question'}, {'test': {'d': 1}})
            with self.assertRaisesRegex(ValueError, 'checksum'):
                measurement.inputs(req, refs, temp)
            req['heldout_family']['set'].update(digest=trec.fingerprint(held), fingerprint=trec.fingerprint(held))
            with self.assertRaisesRegex(ValueError, 'overlap'):
                measurement.inputs(req, refs, temp)


class SearchFailure(unittest.TestCase):
    def test_baseline_outage_invalidates_pair_instead_of_becoming_zero_quality(self):
        # Fake only engine HTTP, exercise real ingest/feed/search error handling.
        searches, indexed_key = [], 'd'
        class API(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def reply(self, code, body):
                self.send_response(code)
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())
            def do_GET(self):
                if self.path.startswith('/v0/changes'):
                    self.reply(200, {'items': [{'type': 'record.enrichment_available', 'resource': {'id': 'r'}}],
                                     'next_cursor': 'cursor', 'has_more': False})
                else:
                    self.reply(200, {'items': [{'record_id': 'r', 'source': {'record_key': indexed_key}}]})
            def do_POST(self):
                self.rfile.read(int(self.headers['Content-Length']))
                if self.path == '/v0/corpora':
                    self.reply(201, {'corpus_id': 'corpus'})
                elif self.path == '/v0/records/batch':
                    self.reply(200, {'items': [{'receipt': {'receipt_id': 'receipt'}}]})
                else:
                    searches.append(self.headers['Authorization'])
                    self.reply(503, {'error': 'sentinel-private-text'})
        server = ThreadingHTTPServer(('127.0.0.1', 0), API)
        thread = threading.Thread(target=server.serve_forever, kwargs={'poll_interval': .01})
        thread.start()
        try:
            clients = {side: run.Client(f'http://127.0.0.1:{server.server_port}', side) for side in ('candidate', 'baseline')}
            data = {'corpus': {'d': {'title': '', 'text': 'neutral'}}, 'queries': {'q': 'neutral'}, 'qrels': {'q': {'d': 1}}}
            configs = {side: {'mode': 'semantic', 'profile': 'default', 'request_limit': 10} for side in clients}
            budgets = {side: embeddings.Budget(10000, 1) for side in clients}
            for indexed_key, reason, expected_searches in (
                    ('outside-corpus', '^engine index is incomplete$', []),
                    ('d', '^engine search failed$', ['Bearer baseline'])):
                with self.subTest(indexed_key=indexed_key):
                    searches.clear()
                    with self.assertRaisesRegex(RuntimeError, reason):
                        measurement.paired(clients, data, configs, budgets, 'fixed', .001, 'a' * 40)
                    self.assertEqual(searches, expected_searches)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


@unittest.skipUnless(importlib.util.find_spec('scipy'), 'paired gates require scipy')
class Aggregates(unittest.TestCase):
    def test_gate_projection_retains_decisions_without_raw_query_ids_or_samples(self):
        # New held-out export owns field construction, not statistical formulae.
        # Regression: passing gates.evaluate directly exports its latency IDs.
        baseline = {'sentinel-query-' + str(i): .3 + i / 100 for i in range(20)}
        candidate = {q: value + .1 + i / 10000 for i, (q, value) in enumerate(baseline.items())}
        sample = {'policy': 'sha256-query-id-v1; max=50',
                  'query_ids': sorted(baseline, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q)),
                  'warmup_query_ids': [sorted(baseline)[0]]}
        def row(scores):
            return {'tier': 'engine', 'git_sha': 'a' * 40, 'dataset': {'fingerprint': 'frozen'},
                    'per_query': {'ndcg@10': scores}, 'text': 'sentinel-secret',
                    'metrics': {'ndcg@10': sum(scores.values()) / 20, 'latency_p95_ms': 10,
                                'cost_per_search_usd': .0001, 'cost_per_1000_documents_usd': 1},
                    'cost': {'resource_class': 'same', 'latency_method': 'fresh', 'latency_sample': sample}}
        req = {'heldout_fingerprint': 'a' * 64}
        policy = {'sets': {'set': {'diagnostic': False}}, 'profile': 'default'}
        exported = confirmation.aggregate({'set': {'candidate': row(candidate), 'baseline': row(baseline)}}, req, policy)
        self.assertEqual(exported['status'], 'confirmed')
        self.assertTrue(exported['heldout']['passed'])
        self.assertEqual(exported['aggregate_sets']['set']['paired']['queries'], 20)
        self.assertGreater(exported['aggregate_sets']['set']['paired']['delta'], .1)
        self.assertTrue(all(g['passed'] for g in exported['gates'].values()))
        self.assertNotIn('sentinel', json.dumps(exported))


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN') and importlib.util.find_spec('scipy'), 'requires disposable PostgreSQL and scipy')
class Replay(unittest.TestCase):
    def test_canonical_engine_result_replays_aggregates_without_reopening_heldout(self):
        # SQL and tracking are real owners. Fake only remote measurement
        # transport and the MLflow wire. Publication/replay/export stay real.
        import psycopg
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        with store.transaction() as db:
            db.execute((confirmation.ROOT / 'deploy/mlflow/eval-control.sql').read_text())
        name = uuid.uuid4().hex
        baseline = search_trial.configuration({'model': 'Cohere-Embed-V5-Pro', 'revision': '2026-10-03', 'dimensions': 1024})
        candidate = {**baseline, 'dense_weight': .3}
        policy = modal_search.frozen_policy(modal_search.policy({'experiment': 'public/confirmation-test',
            'baseline': baseline, 'sets': {'scifact': {'split': 'dev'}},
            'modal_usd_per_second': .001, 'price_revision': '2026-10-03'}), 'a' * 40, 'sha256:' + 'b' * 64)
        store.campaign(name, policy)
        scores = {'sentinel-query-' + str(i): .3 + i / 100 for i in range(20)}
        sample = {'policy': 'sha256-query-id-v1; max=50',
            'query_ids': sorted(scores, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q)), 'warmup_query_ids': [sorted(scores)[0]]}
        pairs, evidence = {}, {}
        for side, cfg in (('baseline', baseline), ('candidate', candidate)):
            values = {q: v + (.1 + i / 10000 if side == 'candidate' else 0) for i, (q, v) in enumerate(scores.items())}
            row = search_trial.record({'machine': 'same', 'duration_seconds': 1,
                'dataset': {'name': 'scifact', 'version': '1', 'split': 'dev', 'fingerprint': 'c' * 64, 'private': False},
                'per_query': {'ndcg@10': values}, 'metrics': {'ndcg@10': sum(values.values()) / 20,
                    'latency_p95_ms': 10, 'cost_per_search_usd': .0001, 'cost_per_1000_documents_usd': 1},
                'cost': {'resource_class': 'same', 'latency_method': 'fresh', 'latency_sample': sample}},
                {**cfg, 'campaign': name, 'campaign_policy_hash': search_trial.digest(policy), 'profile': 'default', 'fresh_latency': True},
                policy['experiment'], policy['git_sha'], policy['scorer_digest'])
            key = 'dev/' + side
            store.publish(name, key, store.claim(name, key)['owner'], row)
            evidence[side], pairs[side] = key, row
        remote_runs, artifacts, transported, invocations = {}, {}, [], []
        def http(request, **kwargs):
            path = urllib.parse.urlsplit(request.full_url).path
            body = json.loads(request.data) if request.data else {}
            transported.append(body)
            response = {}
            if path.endswith('experiments/search'):
                response = {'experiments': [{'experiment_id': '1', 'name': policy['experiment']}]}
            elif path.endswith('runs/search'):
                selected = list(remote_runs.values())
                if " = '" in body.get('filter', ''):
                    key = body['filter'].split(" = '")[1].rstrip("'")
                    selected = [r for r in selected if r['data']['tags'][0]['value'] == key]
                response = {'runs': selected}
            elif path.endswith('runs/create'):
                rid = uuid.uuid4().hex
                remote_runs[rid] = {'info': {'run_id': rid, 'status': 'RUNNING', 'artifact_uri': 'mlflow-artifacts:/1/' + rid},
                                    'data': {'tags': body['tags'], 'params': [], 'metrics': []}}
                response = {'run': remote_runs[rid]}
            elif path.endswith('runs/log-batch'):
                remote_runs[body['run_id']]['data'].update(params=body['params'], metrics=body['metrics'])
            elif path.endswith('runs/update'):
                remote_runs[body['run_id']]['info']['status'] = body['status']
            elif '/mlflow-artifacts/' in path:
                if request.method == 'PUT':
                    artifacts[path] = body
                else:
                    response = artifacts[path]
            else:
                self.fail('unexpected MLflow route')
            return io.BytesIO(json.dumps(response).encode())
        with tempfile.TemporaryDirectory() as temp, mock.patch.dict(os.environ, {'MLFLOW_TRACKING_URI': 'https://example.com'}), \
                mock.patch('urllib.request.OpenerDirector.open', side_effect=http):
            tracking = results.Results(directory=temp)
            for row in pairs.values():
                self.assertEqual(tracking.log(row)['status'], 'synced')
            req = confirmation.build_request(store, name, 1, candidate,
                {'name': 'frozen-family', 'version': 1, 'split': 'heldout', 'privacy': 'public',
                 'sets': {'scifact': {'version': '1', 'split': 'heldout', 'private': False,
                             'digest': 'd' * 64, 'fingerprint': 'e' * 64}}},
                {'scifact': {'fingerprint': 'c' * 64, 'split_fingerprint': 'f' * 64}}, {'scifact': evidence},
                {'production': {'ingestion': {'kind': 'hosted', 'max_tokens_per_segment': 512, 'overlap': 48, 'batch_size': 16, 'max_batch_tokens': 8192,
                                   'request_timeout_ms': 4000, 'call_budget_ms': 30000,
                                   'max_concurrent_requests': 4, 'max_retries': 2},
                                'hybrid_fusion': 'relative_score'}, 'resources': {'experiment': policy['experiment'],
                                'modal_daily_usd': policy['modal_daily_usd'], 'modal_usd_per_second': .001}}, 'a' * 40)
            transported.clear()
            def invoke(remote, renew):
                invocations.append(remote)
                renew()
                ordinal = store.confirmation(name, (remote['lease_key'], remote['owner']))
                held = copy.deepcopy(pairs)
                for row in held.values():
                    row.update(tier='engine', dataset={'fingerprint': 'e' * 64})
                return {**confirmation.aggregate({'scifact': held}, req, policy), 'read_ordinal': ordinal,
                    'remote_cleanup_verified': True, 'sandbox_terminated': True,
                    'compute_ids': {'app_id': 'ap-fixture', 'sandbox_id': 'sb-fixture'},
                    'query_text': 'sentinel-private-text', 'private_artifact_url': 'sentinel-private-url'}
            # Cancellation after admission must release the attempt lease,
            # retain its conservative charge/read, and allow a fenced retry.
            def cancelled(remote, renew):
                store.confirmation(name, (remote['lease_key'], remote['owner']))
                raise asyncio.CancelledError('sentinel-private-cancellation')
            failed = confirmation.confirm(store, req, cancelled, temp)
            self.assertEqual(failed['status'], 'failed')
            self.assertNotIn('sentinel', json.dumps(failed))
            with store.transaction() as db:
                self.assertEqual(db.execute('SELECT count(*) FROM eval_control.attempts WHERE campaign=%s AND status=%s', (name, 'failed')).fetchone()[0], 1)
                self.assertGreater(db.execute('SELECT sum(charged_usd) FROM eval_control.reservations WHERE campaign=%s', (name,)).fetchone()[0], 0)
            first = confirmation.confirm(store, req, invoke, temp)
            self.assertEqual(first['status'], 'confirmed', first)
            replay = confirmation.confirm(store, req, invoke, temp)
            self.assertEqual(replay, first)
            store.stop(name)
            self.assertEqual(confirmation.confirm(store, req, invoke, temp), first)
            self.assertEqual(len(invocations), 1)
            self.assertEqual(store.availability(name)['confirmation_reads_left'], 8)
            self.assertTrue(all(r['status'] == 'synced' for r in first['receipts']))
            canonical = store.claim(name, first['confirmation_key'])['payload']
            exported = json.dumps([first, canonical, transported])
            self.assertNotIn('sentinel', exported)
            held_files = [p for p in pathlib.Path(temp).glob('*.json') if json.loads(p.read_text())['tier'] == 'engine']
            self.assertEqual(len(held_files), 2)
            self.assertNotIn('sentinel', ''.join(p.read_text() for p in held_files))


if __name__ == '__main__':
    unittest.main()
