"""Runner contract: real cache/ranking/scoring/outbox; only provider I/O is fake.

No existing test covers configuration sweeps or canonical lease publication.
Changing fusion must change rankings without re-embedding cached documents;
successful fake network responses still exercise real shared admission.
"""
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
            def run(config, key, fresh=False):
                lease = store.claim(campaign, key)
                budget = control_store.Budget(store, campaign, (key, lease['owner']))
                client = direct_bakeoff.Hosted('https://example.com', 'fixture-key', budget, 'tiny', prices)
                with mock.patch.object(client.opener, 'open', side_effect=respond):
                    return search_trial.measure(config, data, {'name': 'tiny', 'version': '1', 'split': 'dev',
                        'fingerprint': trec.fingerprint(root / 'data'), 'private': False},
                        root / 'cache', budget, client, prices, .001, fresh_latency=fresh)
            dense = run(cfg, 'dense')
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
