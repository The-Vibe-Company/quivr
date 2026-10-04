"""Runner contract: real cache/ranking/scoring/outbox; only provider I/O is fake.

No existing test covers configuration sweeps or canonical lease publication.
Changing fusion must change rankings without re-embedding cached documents;
successful fake network responses still exercise real shared admission.
"""
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import tempfile
import unittest
import uuid
from unittest import mock

import control_store
import direct_bakeoff
import embeddings
import results
import search_trial
import trec


class Reranker(unittest.TestCase):
    def test_admission_precedes_transport_and_only_valid_usage_and_scores_are_accepted(self):
        budget = embeddings.Budget(100000, 1)
        response = {'model': 'jev-1.13.0', 'usage': {'input_tokens': 20},
                    'answers': {'a': {'noul': .1}, 'b': {'noul': .9}}}
        opener = mock.Mock()
        opener.open.side_effect = lambda *a, **kw: io.BytesIO(json.dumps(response).encode())
        with mock.patch.object(search_trial.urllib.request, 'build_opener', return_value=opener):
            ranked = search_trial.rerank('question', {'a': 'first', 'b': 'second'}, budget, 'fixture-key', .042)
            self.assertEqual(ranked, ['b', 'a'])
            self.assertEqual(budget.summary()['confirmed_input_tokens'], 20)
            with self.assertRaises(embeddings.BudgetExceeded):
                search_trial.rerank('question', {'a': 'first'}, embeddings.Budget(100, 1), 'fixture-key', .042)
            self.assertEqual(opener.open.call_count, 1)
            response['answers']['b']['noul'] = float('nan')
            with self.assertRaisesRegex(RuntimeError, '^reranker attempt failed'):
                search_trial.rerank('question', {'a': 'first', 'b': 'second'}, budget, 'fixture-key', .042)


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN') and importlib.util.find_spec('ranx'), 'needs eval dependencies and disposable PostgreSQL')
class Trial(unittest.TestCase):
    def test_cache_chunks_commit_before_publication_and_recover_expired_fills(self):
        # Own bounded cache persistence/recovery, beyond the scoring sweep's
        # tiny fixture. Only hosted HTTP is fake; leases and files are real.
        import psycopg
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        campaign = uuid.uuid4().hex
        store.campaign(campaign, {'provider_daily_usd': 1, 'modal_daily_usd': 1})
        lease = store.claim(campaign, 'trial')
        budget = control_store.Budget(store, campaign, ('trial', lease['owner']))
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1', 'dimensions': 2})
        prices = {'Cohere-Embed-V5-Fast': .08}
        client = direct_bakeoff.Hosted('https://example.com', 'fixture-key', budget, 'tiny', prices)
        data = {'corpus': {str(i): {'text': 'passage ' + str(i)} for i in range(129)},
                'queries': {'q': 'question'}, 'qrels': {'q': {'0': 1}}}
        dataset = {'name': 'tiny', 'version': '1', 'split': 'dev', 'private': False}
        def respond(request, timeout):
            count = len(json.loads(request.data)['texts'])
            return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                'meta': {'billed_units': {'input_tokens': count}}}).encode())
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(client.opener, 'open', side_effect=respond) as network:
            def run(flush):
                return search_trial.measure(cfg, data, dataset, temp, budget, client, prices, .001,
                                            fresh_latency=False, flush=flush)
            identity = {k: cfg[k] for k in ('model', 'revision', 'dimensions', 'window_chars', 'overlap_chars')}
            busy_key = 'embedding/' + search_trial.digest({'config': identity, 'mode': 'document',
                'text_hash': hashlib.sha256('passage 1'.encode()).hexdigest()})
            store.claim(campaign, busy_key, ttl=86400)
            with self.assertRaisesRegex(RuntimeError, 'embedding cache fill already leased'):
                run(mock.Mock())
            network.assert_not_called()
            with psycopg.connect(store.dsn) as db:
                self.assertEqual(db.execute("SELECT count(*) FROM eval_control.leases WHERE campaign=%s AND key LIKE 'embedding/%%'", (campaign,)).fetchone()[0], 1)
                db.execute("UPDATE eval_control.leases SET expires_at=clock_timestamp()-interval '1 second' WHERE campaign=%s AND key=%s", (campaign, busy_key))
            with self.assertRaises(OSError):
                run(mock.Mock(side_effect=OSError('volume commit failed')))
            with psycopg.connect(store.dsn) as db:
                self.assertEqual(db.execute('SELECT count(*) FROM eval_control.leases WHERE campaign=%s AND payload IS NOT NULL', (campaign,)).fetchone()[0], 0)
                db.execute("UPDATE eval_control.leases SET expires_at=clock_timestamp()-interval '1 second' WHERE campaign=%s AND key LIKE 'embedding/%%'", (campaign,))
            snapshots = []
            def flush():
                with psycopg.connect(store.dsn) as db:
                    snapshots.append(db.execute('SELECT count(*) FROM eval_control.leases WHERE campaign=%s AND payload IS NOT NULL', (campaign,)).fetchone()[0])
            before = network.call_count
            with self.assertLogs(search_trial.LOG, level='INFO') as progress:
                measured = run(flush)
            self.assertEqual(snapshots, [0, 128, 129])
            self.assertEqual(network.call_count - before, 4)
            self.assertEqual(measured['cost']['cache_hits'], 0)
            self.assertNotIn('passage ', '\n'.join(progress.output))
            self.assertNotIn('question', '\n'.join(progress.output))
            before = network.call_count
            commit = mock.Mock()
            replay = run(commit)
            self.assertEqual(network.call_count, before)
            commit.assert_not_called()
            self.assertEqual(replay['cost']['cache_hits'], 130)
            with psycopg.connect(store.dsn) as db:
                payload = db.execute('SELECT payload FROM eval_control.leases WHERE campaign=%s AND key=%s', (campaign, busy_key)).fetchone()[0]
            path = pathlib.Path(temp, payload['filename'])
            original = path.read_bytes()
            data['corpus']['000'] = {'text': 'uncached passage'}
            for corrupt in (False, True):
                with self.subTest(corrupt=corrupt):
                    if corrupt:
                        path.write_text('{}')
                    else:
                        path.unlink()
                    with self.assertRaisesRegex(RuntimeError, 'cache (unavailable|digest mismatch)'):
                        run(commit)
                    self.assertEqual(network.call_count, before)
                    with psycopg.connect(store.dsn) as db:
                        self.assertEqual(db.execute("SELECT count(*) FROM eval_control.leases WHERE campaign=%s AND key LIKE 'embedding/%%'", (campaign,)).fetchone()[0], 130)
                    path.write_bytes(original)
            recovered = run(commit)
            self.assertEqual(network.call_count, before + 1)
            self.assertEqual(commit.call_count, 1)
            self.assertEqual(recovered['cost']['cache_hits'], 130)

    def test_cached_sweep_reprices_usage_and_logs_real_scores_without_duplicate_provider_calls(self):
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        campaign = uuid.uuid4().hex
        store.campaign(campaign, {'provider_daily_usd': 1, 'modal_daily_usd': 1})
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1', 'dimensions': 2,
                                          'dense_weight': 1, 'window_chars': 20, 'overlap_chars': 0})
        prices = {'Cohere-Embed-V5-Fast': .08, 'jev-1.13.0': .042}
        requests = []
        def respond(request, timeout):
            body = json.loads(request.data)
            requests.append(body)
            vectors = [[1, 0] if text == 'apple' else [0, 1] for text in body['texts']]
            return io.BytesIO(json.dumps({'embeddings': {'float': vectors},
                                         'meta': {'billed_units': {'input_tokens': len(vectors)}}}).encode())
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            trec.write(root / 'data', {'a': {'text': 'apple'}, 'b': {'text': 'pear'}}, {'q': 'pear'}, {'q': {'a': 1}})
            data = trec.load(root / 'data')
            outbox = results.Results(directory=root / 'results')
            def run(config, key, fresh=False, commits=0):
                lease = store.claim(campaign, key)
                budget = control_store.Budget(store, campaign, (key, lease['owner']))
                client = direct_bakeoff.Hosted('https://example.com', 'fixture-key', budget, 'tiny', prices)
                with mock.patch.object(client.opener, 'open', side_effect=respond):
                    # Each mode is one tiny chunk; commit once for documents, once for queries.
                    commit = mock.Mock()
                    measured = search_trial.measure(config, data, {'name': 'tiny', 'version': '1', 'split': 'dev',
                        'fingerprint': trec.fingerprint(root / 'data'), 'private': False},
                        root / 'cache', budget, client, prices, .001, fresh_latency=fresh, flush=commit)
                    self.assertEqual(commit.call_count, commits)
                    return measured
            dense = run(cfg, 'dense', commits=2)
            calls = len(requests)
            lexical = run(dict(cfg, dense_weight=.5), 'hybrid')
            self.assertEqual(len(requests), calls)
            self.assertAlmostEqual(dense['metrics']['ndcg@10'], .6309297535714575)
            self.assertAlmostEqual(lexical['metrics']['ndcg@10'], .6309297535714575)
            self.assertGreater(lexical['metrics']['cost_per_1000_documents_usd'], 0)
            self.assertIsNone(lexical['metrics']['latency_p95_ms'])
            self.assertEqual(lexical['cost']['provider']['confirmed_input_tokens'], 0)
            self.assertEqual(search_trial.rank(['apple', 'pear'], ['a', 'b'], 'apple', [0, 1], [[1, 0], [0, 1]], [0, 1], cfg), ['b', 'a'])
            self.assertEqual(search_trial.rank(['apple', 'pear'], ['a', 'b'], 'apple', [0, 1], [[1, 0], [0, 1]], [0, 1], dict(cfg, dense_weight=0)), ['a', 'b'])
            before = len(requests)
            fresh = run(cfg, 'fresh', fresh=True)
            self.assertEqual(len(requests) - before, 2)  # One warmup, one scored query.
            self.assertEqual(fresh['cost']['provider']['confirmed_input_tokens'], 2)
            self.assertEqual(len(fresh['per_query']['ndcg@10']), 1)
            self.assertIsNotNone(fresh['metrics']['latency_p95_ms'])
            self.assertIn('one fixed first-query warmup', fresh['cost']['latency_method'])
            lease = store.claim(campaign, 'publish')
            canonical = search_trial.record(lexical, cfg, 'public/trial', 'a' * 40, 'sha256:fixture')
            store.publish(campaign, 'publish', lease['owner'], canonical)
            receipt = outbox.log(store.claim(campaign, 'publish')['payload'])
            self.assertAlmostEqual(outbox.get(receipt['result_key'], queries=True)['per_query']['ndcg_at_10']['q'], .6309297535714575)


if __name__ == '__main__':
    unittest.main()
