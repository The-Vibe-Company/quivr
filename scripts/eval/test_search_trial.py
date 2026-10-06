"""Runner contract: real cache/ranking/scoring/outbox; only provider I/O is fake.

No existing test covers configuration sweeps or canonical lease publication.
Changing fusion must change rankings without re-embedding cached documents;
successful fake network responses still exercise real shared admission.
"""
import hashlib
import collections
import math
import re
import importlib.util
import io
import json
import os
import pathlib
import tempfile
import threading
import unittest
import urllib.error
import uuid
from unittest import mock

import control_store
import direct_bakeoff
import embeddings
import results
import search_trial
import trec



# Frozen pre-THE-1004 scorer: independent oracle for ranking preservation.
def reference_bm25(docs, query):
    terms = [collections.Counter(re.findall(r'\w+', text.casefold())) for text in docs]
    lengths = [sum(t.values()) for t in terms]
    average = sum(lengths) / len(lengths) or 1
    scores = [0.] * len(docs)
    for word in set(re.findall(r'\w+', query.casefold())):
        df = sum(word in t for t in terms)
        idf = math.log(1 + (len(docs) - df + .5) / (df + .5))
        for i, term in enumerate(terms):
            tf = term[word]
            scores[i] += idf * tf * 2.2 / (tf + 1.2 * (.25 + .75 * lengths[i] / average))
    return scores


def reference_rank(docs, doc_ids, query, query_vector, piece_vectors, owners, cfg):
    """Best-piece exact cosine and weighted reciprocal-rank BM25 fusion."""
    import numpy as np
    order = lambda values: sorted(range(len(doc_ids)), key=lambda i: (-values[i], doc_ids[i]))
    alpha = cfg['dense_weight']
    if alpha == 0:
        return [doc_ids[i] for i in order(reference_bm25(docs, query))[:cfg['candidate_count']]]
    scores = direct_bakeoff.normalize([query_vector]) @ direct_bakeoff.normalize(piece_vectors).T
    dense = np.full(len(docs), -np.inf, dtype=np.float32)
    np.maximum.at(dense, owners, scores[0])
    dense_order = order(dense)
    if alpha == 1:
        selected = dense_order
    else:
        lexical_order = order(reference_bm25(docs, query))
        fused = [0.] * len(docs)
        for weight, ranking in ((alpha, dense_order), (1 - alpha, lexical_order)):
            for position, index in enumerate(ranking, 1):
                fused[index] += weight / (60 + position)
        selected = order(fused)
    return [doc_ids[i] for i in selected[:cfg['candidate_count']]]



@unittest.skipUnless(importlib.util.find_spec('numpy'), 'requires numpy')
class PreparedSearch(unittest.TestCase):
    def test_prepared_serial_and_batched_rankings_match_previous_scorer(self):
        # Own scoring preservation and corpus reuse at the cheapest boundary.
        # Re-tokenisation after preparation is forbidden without timing sleeps.
        docs = ['Apple apple pear', '', 'CAFÉ café pear', 'pear apple', 'unrelated']
        ids = ['z', 'a', 'c', 'b', 'd']
        pieces = [[2, 0, 0], [0, 3, 0], [1, 1, 0], [0, 0, 4], [2, 0, 0], [-1, 0, 0]]
        owners = [0, 0, 1, 2, 3, 4]
        queries = ['APPLE apple missing', 'café pear', '', 'unseen']
        vectors = [[3, 0, 0], [0, 1, 1], [1, 1, 0], [-1, 0, 0]]
        class Document(str):
            readable = True
            def casefold(self):
                if not self.readable:
                    raise AssertionError('scoring re-read a corpus document')
                return super().casefold()
        for weight in (0, .3, .5, 1):
            with self.subTest(weight=weight):
                cfg = search_trial.configuration({'dense_weight': weight})
                expected = [reference_rank(docs, ids, q, v, pieces, owners, cfg)
                            for q, v in zip(queries, vectors)]
                guarded = [Document(d) for d in docs]
                index = search_trial.SearchIndex(guarded, ids, pieces, owners, cfg)
                for d in guarded:
                    d.readable = False
                batch = index.dense_scores(vectors) if weight else [None] * len(queries)
                for q, v, scores, ranking in zip(queries, vectors, batch, expected):
                    self.assertEqual(index.rank(q, v), ranking)
                    self.assertEqual(index.rank(q, dense=scores), ranking)

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
    def trial(self, prices=None, provider_cap=1, store=None, campaign=None, key='trial'):
        store = store or control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        if campaign is None:
            campaign = uuid.uuid4().hex
            store.campaign(campaign, {'provider_daily_usd': provider_cap, 'modal_daily_usd': 1})
        lease = store.claim(campaign, key)
        budget = control_store.Budget(store, campaign, (key, lease['owner']))
        client = (direct_bakeoff.Hosted('https://example.com', 'fixture-key', budget, 'tiny', prices)
                  if prices is not None else None)
        return store, campaign, budget, client

    def test_quality_overlap_preserves_rankings_prices_and_serial_latency(self):
        # Own concurrent quality ordering and per-search prices at measure():
        # real SQL admission/scoring, only HTTP is fake. Existing embedding
        # concurrency tests cannot detect cross-query rerank cost attribution.
        data = {'corpus': {'a': {'text': 'apple'}, 'b': {'text': 'pear'}},
                'queries': {f'q{i:02}': f'question {i}' for i in range(10)},
                'qrels': {f'q{i:02}': {'a': 1} for i in range(10)}}
        cfg = search_trial.configuration({'dense_weight': 0, 'reranker': 'jev'})
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        for limit in (1, 3, 8):
            for fresh in (False, True):
                with self.subTest(limit=limit, fresh=fresh), tempfile.TemporaryDirectory() as temp:
                    store, campaign, budget, _ = self.trial(store=store)
                    finished = [threading.Event() for _ in range(10)]
                    arrivals = {start: threading.Barrier(min(limit, 10 - start)) for start in range(0, 10, limit)}
                    lock, completed, visits = threading.Lock(), [], collections.Counter()
                    active, peak = 0, 0
                    latency_calls = []
                    def provider(request, timeout):
                        nonlocal active, peak
                        payload = json.loads(request.data)
                        query = int(payload['state']['query'].split()[-1])
                        with lock:
                            active += 1
                            peak = max(peak, active)
                            quality = visits[query] == 0
                            visits[query] += 1
                            if not quality:
                                self.assertTrue(all(event.is_set() for event in finished))
                                latency_calls.append(query)
                                self.assertEqual(active, 1)
                        if quality:
                            arrivals[query // limit * limit].wait(5)
                        if quality and query < min((query // limit + 1) * limit, 10) - 1:
                            self.assertTrue(finished[query + 1].wait(5), 'quality calls did not overlap')
                        with lock:
                            if quality:
                                completed.append(query)
                                finished[query].set()
                            active -= 1
                        scores = {'a': .9 if query % 2 == 0 else .1, 'b': .1 if query % 2 == 0 else .9}
                        return io.BytesIO(json.dumps({'model': 'jev-1.13.0',
                            'usage': {'input_tokens': (query + 1) * 5},
                            'answers': {doc: {'noul': value} for doc, value in scores.items()}}).encode())
                    original_score = search_trial.scoring.score
                    rankings = {}
                    def score(qrels, ranking):
                        rankings.update(ranking)
                        return original_score(qrels, ranking)
                    with mock.patch('urllib.request.OpenerDirector.open', side_effect=provider), \
                            mock.patch.object(search_trial.scoring, 'score', side_effect=score):
                        measured = search_trial.measure(cfg, data, {'split': 'dev', 'private': False}, temp,
                            budget, None, {'jev-1.13.0': .042}, 0, fresh_latency=fresh,
                            rerank_key='fixture-key', quality_concurrency=limit)
                    expected_order = [i for start in range(0, 10, limit)
                                      for i in reversed(range(start, min(start + limit, 10)))]
                    self.assertEqual(completed, expected_order)
                    self.assertEqual(peak, limit)
                    self.assertEqual(list(rankings), sorted(data['queries']))
                    for i in range(10):
                        self.assertEqual(rankings[f'q{i:02}'], ['a', 'b'] if i % 2 == 0 else ['b', 'a'])
                        self.assertAlmostEqual(measured['per_query']['ndcg@10'][f'q{i:02}'],
                                               1 if i % 2 == 0 else 1 / math.log2(3))
                    self.assertAlmostEqual(measured['cost']['search_provider_usd'], 27.5 * .042 / 1_000_000)
                    self.assertEqual(measured['cost']['provider']['confirmed_input_tokens'], 555 if fresh else 275)
                    self.assertEqual(measured['cost']['provider']['reserved_input_tokens'], 0)
                    expected_sample = sorted(range(10), key=lambda i: (hashlib.sha256(f'q{i:02}'.encode()).hexdigest(), i))
                    self.assertEqual(latency_calls, [0] + expected_sample if fresh else [])
                    self.assertEqual(measured['metrics']['latency_p95_ms'] is not None, fresh)
                    for phase, usage in measured['cost']['phase_usage'].items():
                        self.assertGreaterEqual(usage['cpu_seconds'], 0, phase)
                        self.assertGreaterEqual(usage['elapsed_seconds'], 0, phase)

    def test_rerank_wave_drains_admitted_calls_after_cap_or_transport_failure(self):
        # Own failure drain and retained charges with simultaneous reranks.
        # A shared PostgreSQL cap refuses before HTTP, including concurrent work.
        for capped in (False, True):
            with self.subTest(capped=capped), tempfile.TemporaryDirectory() as temp:
                store, campaign, budget, _ = self.trial(provider_cap=.006 if capped else 1)
                refused, barrier = threading.Event(), threading.Barrier(3)
                reserve = store.reserve
                def admit(*args, **kwargs):
                    try:
                        return reserve(*args, **kwargs)
                    except embeddings.BudgetExceeded:
                        refused.set()
                        raise
                def provider(request, timeout):
                    if capped:
                        self.assertTrue(refused.wait(5), 'cap did not refuse overlapping reservation')
                    else:
                        barrier.wait(5)
                        if json.loads(request.data)['state']['query'] == 'question 0':
                            raise OSError('transport failed')
                    return io.BytesIO(b'{"model":"jev-1.13.0","usage":{"input_tokens":20},"answers":{"a":{"noul":1}}}')
                cfg = search_trial.configuration({'dense_weight': 0, 'reranker': 'jev'})
                data = {'corpus': {'a': {'text': 'apple'}},
                        'queries': {str(i): f'question {i}' for i in range(3)},
                        'qrels': {str(i): {'a': 1} for i in range(3)}}
                with mock.patch.object(store, 'reserve', side_effect=admit), \
                        mock.patch('urllib.request.OpenerDirector.open', side_effect=provider) as network:
                    with self.assertRaises(embeddings.BudgetExceeded if capped else RuntimeError):
                        search_trial.measure(cfg, data, {'split': 'dev', 'private': False}, temp,
                            budget, None, {'jev-1.13.0': .042}, 0, fresh_latency=False,
                            rerank_key='fixture-key', quality_concurrency=3)
                self.assertEqual(network.call_count, 2 if capped else 3)
                self.assertEqual(budget.summary()['confirmed_input_tokens'], 40)
                self.assertEqual(budget.summary()['reserved_input_tokens'], 0 if capped else 65536)
                self.assertAlmostEqual(store.summary(campaign)['provider']['charged_usd'],
                                       (40 + (0 if capped else 65536)) * .042 / 1_000_000)

    def test_serving_compute_excludes_hosted_wait_and_cached_usage_reprices(self):
        # Own unit price decomposition: fake time advances only at provider I/O
        # and ranking. SQL admission, cache and scorer remain real.
        data = {'corpus': {'a': {'text': 'apple'}}, 'queries': {'q': 'apple'}, 'qrels': {'q': {'a': 1}}}
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        original_rank = search_trial.SearchIndex.rank
        cases = (
            ('hosted', 'none', .00002008, 10020, .00000008, 20, 10000, .00004008, .00008),
            ('hosted', 'jev', .00002029, 20020, .00000029, 20, 20000, .00004029, .00008),
            ('local', 'none', .00005, 50, 0, 50, 0, .0001, .06),
        )
        for private in (False, True):
            for model, reranker, price, latency, provider_usd, local_ms, provider_ms, replay_price, index_price in cases:
                with self.subTest(private=private, model=model, reranker=reranker), tempfile.TemporaryDirectory() as temp:
                    cfg = search_trial.configuration({'reranker': reranker, **({
                        'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1', 'dimensions': 2} if model == 'hosted' else {})})
                    prices = {'Cohere-Embed-V5-Fast': .08, 'jev-1.13.0': .042}
                    store, campaign, budget, client = self.trial(prices, store=store)
                    clock = [0.]
                    attempts = [0]
                    reserve, settle = budget.reserve, budget.settle
                    def ledger(operation, *args, **kwargs):
                        clock[0] += 3
                        return operation(*args, **kwargs)
                    def backoff(seconds):
                        clock[0] += seconds
                    def provider(request, timeout):
                        clock[0] += 10
                        if 'texts' in json.loads(request.data):
                            attempts[0] += 1
                            # After cache fill and warmup, throttle the timed sample.
                            if attempts[0] == 4:
                                raise urllib.error.HTTPError(request.full_url, 429, 'throttle',
                                    {'Retry-After': '7'}, io.BytesIO(b''))
                        if 'texts' not in json.loads(request.data):
                            return io.BytesIO(b'{"model":"jev-1.13.0","usage":{"input_tokens":5},"answers":{"a":{"noul":1}}}')
                        count = len(json.loads(request.data)['texts'])
                        return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                            'meta': {'billed_units': {'input_tokens': count}}}).encode())
                    def rank(index, *args, **kwargs):
                        clock[0] += .02
                        return original_rank(index, *args, **kwargs)
                    def local_embed(encoder, texts, mode):
                        clock[0] += .03
                        return [[1] + [0] * 383 for _ in texts]
                    vectors = {}
                    with mock.patch.object(search_trial.time, 'monotonic', side_effect=lambda: clock[0]), \
                            mock.patch('urllib.request.OpenerDirector.open', side_effect=provider) as network, \
                            mock.patch.object(search_trial.SearchIndex, 'rank', rank), \
                            mock.patch.object(direct_bakeoff.E5, 'embed', local_embed), \
                            mock.patch.object(budget, 'reserve', side_effect=lambda *a, **k: ledger(reserve, *a, **k)), \
                            mock.patch.object(budget, 'settle', side_effect=lambda *a, **k: ledger(settle, *a, **k)), \
                            mock.patch.object(direct_bakeoff.time, 'sleep', side_effect=backoff), \
                            mock.patch.object(direct_bakeoff.random, 'uniform', return_value=0):
                        measured = search_trial.measure(cfg, data, {'split': 'dev', 'private': private}, temp,
                            budget, client, prices, .001, rerank_key='fixture-key', private_vectors=vectors)
                        self.assertAlmostEqual(measured['metrics']['cost_per_search_usd'], price, delta=1e-12)
                        self.assertAlmostEqual(measured['metrics']['latency_p95_ms'], latency)
                        self.assertAlmostEqual(measured['cost']['search_provider_usd'], provider_usd, delta=1e-12)
                        self.assertAlmostEqual(measured['cost']['search_compute_usd'], price - provider_usd, delta=1e-12)
                        self.assertAlmostEqual(measured['cost']['search_timing_ms']['provider_p95'], provider_ms)
                        self.assertAlmostEqual(measured['cost']['search_timing_ms']['local_p95'], local_ms)
                        timing = measured['cost']['search_timing_ms']
                        if model == 'hosted':
                            self.assertEqual(timing['retried_samples'], 1)
                            self.assertAlmostEqual(timing['retry_http_p95'], 10000)
                            self.assertAlmostEqual(timing['backoff_p95'], 7000)
                            self.assertAlmostEqual(timing['ledger_p95'], 12000 + (6000 if reranker == 'jev' else 0))
                        else:
                            self.assertEqual(timing['retried_samples'], 0)
                        self.assertAlmostEqual(timing['retrieval_p95'], 20)
                        self.assertGreaterEqual(timing['wall_p95'] + 1e-9, latency)
                        before = network.call_count
                        replay = search_trial.measure(cfg, data, {'split': 'dev', 'private': private}, temp,
                            budget, client, prices, .002, fresh_latency=False, rerank_key='fixture-key', private_vectors=vectors)
                        self.assertEqual(network.call_count, before + (reranker == 'jev'))
                        self.assertAlmostEqual(replay['metrics']['cost_per_search_usd'], replay_price, delta=1e-12)
                        self.assertAlmostEqual(replay['metrics']['cost_per_1000_documents_usd'], index_price, delta=1e-12)


    def test_concurrent_retries_complete_with_uncertain_spend(self):
        # Own retry acceptance at the measurement/ledger boundary. Each of
        # four overlapping tasks fails its first HTTP attempt, then succeeds.
        # Public cache publication and private ephemeral fills use this path.
        for private in (False, True):
            with self.subTest(private=private):
                cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1', 'dimensions': 2})
                prices = {'Cohere-Embed-V5-Fast': .08}
                store, campaign, budget, client = self.trial(prices)
                data = {'corpus': {str(i): {'text': f'passage{i:03}'} for i in range(512)},
                        'queries': {'q': 'question'}, 'qrels': {'q': {'0': 1}}}
                barrier, lock = threading.Barrier(4), threading.Lock()
                attempted, query_calls = set(), 0
                def respond(request, timeout):
                    nonlocal query_calls
                    body = json.loads(request.data)
                    first = body['texts'][0]
                    with lock:
                        # Fail each document task, the quality query, and
                        # the timed query (after one successful warmup).
                        if body['input_type'] == 'search_query':
                            query_calls += 1
                            retry = query_calls in (1, 4)
                        else:
                            retry = first not in attempted and len(attempted) < 4
                        attempted.add(first)
                    if retry:
                        if body['input_type'] == 'search_document':
                            barrier.wait(timeout=5)
                        code = 429 if first in ('passage000', 'question') else 503
                        raise urllib.error.HTTPError(request.full_url, code, 'private error',
                                                     {'Retry-After': '0'}, io.BytesIO(b'private body'))
                    count = len(body['texts'])
                    return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                        'meta': {'billed_units': {'input_tokens': count}}}).encode())
                with tempfile.TemporaryDirectory() as temp, mock.patch.object(client.opener, 'open', side_effect=respond), \
                        mock.patch.object(direct_bakeoff.time, 'sleep'):
                    commit = mock.Mock()
                    private_vectors = {}
                    measured = search_trial.measure(cfg, data, {'split': 'dev', 'private': private}, temp,
                                                   budget, client, prices, 0, flush=commit, private_vectors=private_vectors)
                    self.assertEqual(measured['metrics']['ndcg@10'], 1)
                    self.assertEqual(measured['cost']['provider']['confirmed_input_tokens'], 515)
                    self.assertEqual(measured['cost']['provider']['reserved_input_tokens'], 3456)
                    self.assertAlmostEqual(store.summary(campaign)['provider']['unknown_usd'], .00027648)
                    # 512 confirmed + 3 * 1152 uncertain 503 document tokens;
                    # document/query 429 attempts settle zero.
                    self.assertAlmostEqual(measured['metrics']['cost_per_1000_documents_usd'], .00062)
                    self.assertAlmostEqual(measured['metrics']['cost_per_search_usd'], .00000008)
                    replay = search_trial.measure(cfg, data, {'split': 'dev', 'private': private}, temp,
                        budget, client, prices, 0, fresh_latency=False, flush=commit, private_vectors=private_vectors)
                    self.assertAlmostEqual(replay['metrics']['cost_per_1000_documents_usd'], .00062)
                    self.assertAlmostEqual(replay['metrics']['cost_per_search_usd'], .00000008)
                    self.assertEqual(query_calls, 5)
                    store.publish(campaign, 'trial', budget.lease[1], {'metrics': measured['metrics'], 'cost': measured['cost']})
                    self.assertEqual(store.claim(campaign, 'trial')['status'], 'done')
                    if private:
                        commit.assert_not_called()
                        self.assertEqual(list(pathlib.Path(temp).iterdir()), [])
                    else:
                        self.assertGreater(commit.call_count, 0)

    def test_cache_chunks_commit_before_publication_and_recover_expired_fills(self):
        # Own bounded cache persistence/recovery, beyond the scoring sweep's
        # tiny fixture. Only hosted HTTP is fake; leases and files are real.
        import psycopg
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1', 'dimensions': 2})
        prices = {'Cohere-Embed-V5-Fast': .08}
        store, campaign, budget, client = self.trial(prices)
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
            identity['timing'] = 'local-compute-v2'
            busy_key = 'embedding/' + search_trial.digest({'config': identity, 'mode': 'document',
                'text_hash': hashlib.sha256('passage 1'.encode()).hexdigest()})
            store.claim(campaign, busy_key, ttl=86400)
            with mock.patch('time.sleep', side_effect=RuntimeError('cache wait interrupted')) as wait, \
                    self.assertRaisesRegex(RuntimeError, 'cache wait interrupted'):
                run(mock.Mock())
            wait.assert_called_once_with(1)
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
            # A later corrupt chunk must release earlier validated claims in
            # the same wave, before any provider admission.
            data['corpus'].update({'000' + str(i).zfill(3): {'text': 'uncached passage ' + str(i)} for i in range(129)})
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
            self.assertEqual(network.call_count, before + 3)
            self.assertEqual(commit.call_count, 2)
            self.assertEqual(recovered['cost']['cache_hits'], 130)

    def test_parallel_fills_and_full_quality_use_a_fixed_fresh_latency_sample(self):
        # Own the performance contract at the real runner/SQL boundary. A
        # barrier proves overlap without timing assertions; transport counts
        # catch serial quality embeddings and unsampled latency regressions.
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1', 'dimensions': 2,
                                          'dense_weight': .5})
        prices = {'Cohere-Embed-V5-Fast': .08}
        store, campaign, budget, client = self.trial(prices)
        data = {'corpus': {str(i): {'text': 'passage ' + str(i)} for i in range(513)},
                'queries': {f'q{i:02}': f'question {i}' for i in range(61)},
                'qrels': {f'q{i:02}': {'0': 1} for i in range(61)}}
        dataset = {'name': 'tiny', 'version': '1', 'split': 'dev', 'private': False}
        barrier, lock = threading.Barrier(4), threading.Lock()
        calls, active, peak = [], 0, 0
        def respond(request, timeout):
            nonlocal active, peak
            body = json.loads(request.data)
            with lock:
                calls.append(body)
                active += 1
                peak = max(peak, active)
                document_call = sum(c['input_type'] == 'search_document' for c in calls)
            # First attempt of each document chunk meets all four workers.
            if body['input_type'] == 'search_document' and document_call <= 4:
                barrier.wait(timeout=5)
            with lock:
                active -= 1
            count = len(body['texts'])
            return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                'meta': {'billed_units': {'input_tokens': 7 if body['input_type'] == 'search_query' and count == 1 else count}}}).encode())
        import psycopg
        connect = psycopg.connect
        connections = 0
        def open_connection(*args, **kwargs):
            nonlocal connections
            with lock:
                connections += 1
            return connect(*args, **kwargs)
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(client.opener, 'open', side_effect=respond), \
                mock.patch.object(psycopg, 'connect', side_effect=open_connection):
            measured = search_trial.measure(cfg, data, dataset, temp, budget, client, prices, 0)
            # Actual control-plane I/O stays bounded across all quality queries.
            # THE-1025's per-query SQL renewal exceeds this fixture's budget.
            self.assertLessEqual(connections, 300)
            self.assertEqual(peak, 4)
            self.assertAlmostEqual(measured['cost']['index_tokens_attributed'], 513)
            self.assertEqual(measured['cost']['provider']['reserved_input_tokens'], 0)
            self.assertEqual(measured['cost']['provider']['confirmed_input_tokens'], 931)
            self.assertEqual(len(measured['per_query']['ndcg@10']), 61)
            query_calls = [c['texts'] for c in calls if c['input_type'] == 'search_query']
            expected_ids = sorted(data['qrels'], key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q))[:50]
            self.assertEqual(query_calls[0], [data['queries'][q] for q in sorted(data['qrels'])])
            self.assertEqual(query_calls[1:], [[data['queries']['q00']]] + [[data['queries'][q]] for q in expected_ids])
            self.assertEqual(measured['cost']['latency_sample']['query_ids'], expected_ids)
            self.assertEqual(measured['cost']['latency_sample']['warmup_query_ids'], ['q00'])
            self.assertIsNotNone(measured['metrics']['latency_p95_ms'])
            self.assertAlmostEqual(measured['metrics']['cost_per_search_usd'], 7 * .08 / 1_000_000)

    def test_index_price_includes_preparation_but_excludes_quality_query_fill(self):
        # Own indexing phase boundaries, which call-count/usage tests cannot
        # see. Dependency work advances a fake clock; no wall-clock wait.
        clock = [0.]
        split_documents = direct_bakeoff.split_documents
        def prepare_documents(*args, **kwargs):
            clock[0] += 5
            return split_documents(*args, **kwargs)
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1', 'dimensions': 2})
        prices = {'Cohere-Embed-V5-Fast': .08}
        store, campaign, budget, client = self.trial(prices)
        data = {'corpus': {'a': {'title': 'a', 'text': 'apple'}, 'b': {'title': 'b', 'text': 'pear'}},
                'queries': {'q': 'question'}, 'qrels': {'q': {'a': 1}}}
        def respond(request, timeout):
            body = json.loads(request.data)
            clock[0] += 7 if body['input_type'] == 'search_document' else 1000
            count = len(body['texts'])
            return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                'meta': {'billed_units': {'input_tokens': count}}}).encode())
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(client.opener, 'open', side_effect=respond), \
                mock.patch.object(search_trial.time, 'monotonic', side_effect=lambda: clock[0]), \
                mock.patch.object(direct_bakeoff, 'split_documents', side_effect=prepare_documents):
            measured = search_trial.measure(cfg, data, {'split': 'dev', 'private': False}, temp,
                                           budget, client, prices, .001, fresh_latency=False)
        # Ten seconds preparing documents, two billed tokens; seven seconds of
        # hosted document HTTP and quality-query preparation are excluded.
        self.assertAlmostEqual(measured['metrics']['cost_per_1000_documents_usd'], 5.00008)

    def test_parallel_missing_usage_drains_accounting_without_publishing(self):
        # A sibling in-flight reservation must not reject a valid response,
        # but one response without usage rejects the whole unpublished wave.
        import psycopg
        cfg = search_trial.configuration({'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1', 'dimensions': 2})
        prices = {'Cohere-Embed-V5-Fast': .08}
        store, campaign, budget, client = self.trial(prices)
        data = {'corpus': {str(i): {'text': 'passage ' + str(i)} for i in range(512)},
                'queries': {'q': 'question'}, 'qrels': {'q': {'0': 1}}}
        barrier = threading.Barrier(4)
        first_calls, lock = set(), threading.Lock()
        def respond(request, timeout):
            body = json.loads(request.data)
            first = body['texts'][0]
            with lock:
                first_attempt = threading.get_ident() not in first_calls
                first_calls.add(threading.get_ident())
            if first_attempt:
                barrier.wait(timeout=5)
            count = len(body['texts'])
            return io.BytesIO(json.dumps({'embeddings': {'float': [[1, 0]] * count},
                'meta': {'billed_units': {} if first == 'passage 0' else {'input_tokens': count}}}).encode())
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(client.opener, 'open', side_effect=respond):
            commit = mock.Mock()
            with self.assertRaisesRegex(RuntimeError, 'omitted confirmed usage'):
                search_trial.measure(cfg, data, {'split': 'dev', 'private': False}, temp, budget, client,
                                     prices, .001, flush=commit)
            commit.assert_not_called()
            # The task missing usage stops before its second provider batch;
            # the other three tasks drain both of their successful batches.
            self.assertEqual(budget.summary()['admitted_calls'], 7)
            self.assertEqual(budget.summary()['confirmed_input_tokens'], 384)
            self.assertGreater(budget.summary()['reserved_input_tokens'], 0)
            with psycopg.connect(store.dsn) as db:
                self.assertEqual(db.execute('SELECT count(*) FROM eval_control.leases WHERE campaign=%s AND payload IS NOT NULL', (campaign,)).fetchone()[0], 0)
            self.assertGreater(store.summary(campaign)['provider']['unknown_usd'], 0)

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
                _, _, budget, client = self.trial(prices, store=store, campaign=campaign, key=key)
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
