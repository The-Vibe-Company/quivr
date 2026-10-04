"""Owner tests for live adapter transport, spend and prepared retrieval contracts."""
import contextlib
import concurrent.futures
import threading
import errno
import http.client
import importlib.util
import io
import json
import os
import random
import socket
import ssl
import pathlib
import subprocess
import sys
import tempfile
import types
import unittest
import urllib.error
from unittest import mock

import news_set as news
import news_providers as live


def config():
    return dict(model='chat-model', family='family-a', endpoint_env='NEWS_ENDPOINT', key_env='NEWS_KEY',
                max_input_tokens=1000000, max_output_tokens=100000, max_usd=10,
                input_usd_per_million=1, output_usd_per_million=2,
                request_input_tokens=10000, request_output_tokens=1000, max_retries=2)


def response(content, prompt=100, completion=20, finish='stop'):
    return io.BytesIO(json.dumps({'choices': [{'finish_reason': finish, 'message': {'content': content}}],
                                  'usage': {'prompt_tokens': prompt, 'completion_tokens': completion}}).encode())


class ChatAdapters(unittest.TestCase):
    def setUp(self):
        self.env = mock.patch.dict(os.environ, NEWS_ENDPOINT='https://example.invalid/openai/v1', NEWS_KEY='private-key')
        self.env.start()
        self.addCleanup(self.env.stop)
        self.opener = mock.Mock()
        self.transport = mock.patch('urllib.request.build_opener', return_value=self.opener)
        self.transport.start()
        self.addCleanup(self.transport.stop)

    def test_request_refusals_split_once_and_configuration_errors_stop(self):
        q = news.Question('private-query', 'event', '2026-01-02', ('a',))
        articles = [news.Article(k, '', 'private-text-' + k, q.date) for k in 'abcd']
        for status, code in [(400, None), (404, None), (413, 'context_length_exceeded'), (422, 'invalid_value')]:
            with self.subTest(status=status, code=code):
                self.opener.open.reset_mock()
                def refuse(request, **kwargs):
                    data = json.loads(json.loads(request.data)['messages'][1]['content'])
                    if [a['text'] for a in data['articles']] == ['private-text-c', 'private-text-d']:
                        return response('{"grades":{"a1":2,"a2":0}}')
                    raise urllib.error.HTTPError('private-url', status, 'private-body', {},
                        io.BytesIO(json.dumps({'error': {'code': code, 'message': 'private-key'}}).encode()))
                self.opener.open.side_effect = refuse
                judge = live.ChatJudge(config())
                self.assertEqual(judge.grade(q, articles), {'a': None, 'b': None, 'c': 2, 'd': 0})
                self.assertEqual(self.opener.open.call_count, 3)
                self.assertEqual(judge.summary()['refusals'], {str(status) + ':' + (code or 'unknown'): 2})
                self.assertNotIn('private', json.dumps(judge.summary()))
                counts = __import__('collections').Counter()
                rows, ranks = news.judge_pool([q], articles, {'bm25': news.FakeRetriever()},
                    [judge, news.FakeJudge('b'), news.FakeJudge('c')], .5, counts)
                self.assertEqual(rows, [])
                self.assertEqual(ranks, {})
                self.assertEqual(counts['filtered_candidates'], 2)
                self.assertEqual(counts['dropped_questions'], 1)

        for status, code in [(401, None), (403, 'content_filter'), (404, 'DeploymentNotFound'),
                             (400, 'model_not_found'), (400, 'invalid_api_key'),
                             (402, None), (405, None), (407, None), (415, None),
                             (400, 'insufficient_quota'), (429, 'insufficient_quota')]:
            with self.subTest(status=status, code=code):
                self.opener.open.reset_mock()
                self.opener.open.side_effect = urllib.error.HTTPError('private-url', status, 'private-body', {},
                    io.BytesIO(json.dumps({'error': {'code': code}}).encode()))
                with self.assertRaises(live.AdapterError):
                    live.ChatJudge(config()).grade(q, articles)
                self.assertEqual(self.opener.open.call_count, 1)

    def test_answerability_conflicts_are_replaced_and_counted_with_shares(self):
        from test_news_set import articles, unavailable_scorer
        for kind, reason, persistent in [
                ('entity', 'answer_expected_none_found', False),
                ('no_answer', 'no_answer_but_relevant_found', False),
                ('entity', 'answer_expected_none_found', True)]:
            with self.subTest(kind=kind, persistent=persistent):
                state = {'question': None, 'bad': None}
                by_text = {}
                def retrieve(question, corpus, limit):
                    state['question'] = question
                    by_text.update({a.text: a.id for a in corpus})
                    if question.kind == kind and state['bad'] is None:
                        state['bad'] = question.text
                    return [a.id for a in corpus]
                def answer(request, **kwargs):
                    data = json.loads(json.loads(request.data)['messages'][1]['content'])
                    conflict = data['query'] == state['bad'] or (persistent and state['question'].kind == kind)
                    return response(json.dumps({'grades': {
                        a['id']: (0 if kind == 'entity' else 3) if conflict else
                        3 if by_text[a['text']] in state['question'].sources else 0 for a in data['articles']}}))
                self.opener.open.side_effect = answer
                providers = news.fake_providers()
                providers.retrievers = {'bm25': mock.Mock(search=retrieve)}
                providers.baseline = 'bm25'
                providers.judges[:2] = [live.ChatJudge({**config(), 'family': f}) for f in ('a', 'b')]
                with mock.patch('scoring.score', side_effect=unavailable_scorer):
                    if persistent:
                        with self.assertRaises(news.BuildError) as caught:
                            news.build(articles(), providers, 6, 42, salt=b'x' * 32)
                        diagnostic = caught.exception.diagnostic
                        self.assertEqual(diagnostic['reason'], 'min_questions_not_met')
                        self.assertEqual((diagnostic['accepted'], diagnostic['min_questions']), (5, 6))
                        self.assertEqual(diagnostic['question_targets']['entity'],
                                         {'target': 1, 'accepted': 0, 'shortfall': 1, 'attempts': 10, 'attempt_budget': 10})
                        self.assertEqual(diagnostic['rejected'][reason], 10)
                        continue
                    result = news.build(articles(), providers, 6, 42, salt=b'x' * 32)
                self.assertEqual(result.report['question_types'], dict.fromkeys(news.KINDS, 1))
                self.assertEqual(result.report['rejected'][reason], 1)
                quality = result.report['question_quality']
                self.assertEqual((quality['questions'], quality['dropped_questions']), (7, 1))
                self.assertEqual(quality['shares'][reason], 1 / 7)
                self.assertEqual(result.report['content_filter']['dropped_questions'], 0)
                self.assertNotIn(state['bad'], {**result.working['queries'], **result.held_out['queries']}.values())
                news.validate_report(result.report)

    def test_response_cache_survives_interruption_and_accounts_reuse(self):
        with tempfile.TemporaryDirectory() as directory:
            cache = live.ResponseCache(directory)
            client = live.Chat(config())
            client.response_cache = cache
            self.opener.open.side_effect = [response('{"answer":42}'), TimeoutError('private-key')]
            self.assertEqual(client.complete('instruction', {'text': 'private-text'}), {'answer': 42})
            with mock.patch('news_providers.time.sleep'), self.assertRaises(live.AdapterError):
                client.complete('instruction', {'text': 'other'})
            self.opener.open.side_effect = urllib.error.HTTPError('private-url', 400, 'private-body', {}, io.BytesIO(b'{}'))
            with self.assertRaises(news.InvalidBatch):
                client.complete('instruction', {'text': 'refused'})
            self.assertEqual(client.summary()['refusals'], {'400:unknown': 1})
            cache.close()
            restarted = live.Chat(config())
            restarted.response_cache = live.ResponseCache(directory)
            self.addCleanup(restarted.response_cache.close)
            self.opener.open.side_effect = lambda *args, **kwargs: response('{"answer":43}')
            calls = self.opener.open.call_count
            self.assertEqual(restarted.complete('instruction', {'text': 'private-text'}), {'answer': 42})
            self.assertEqual(self.opener.open.call_count, calls)
            self.assertEqual(restarted.summary()['attempts'], 0)
            self.assertEqual(restarted.summary()['confirmed_cost_usd'], 0)
            self.assertEqual(restarted.summary()['cached_calls'], 1)
            self.assertEqual(restarted.summary()['cached_cost_usd'], .00014)
            with self.assertRaises(news.InvalidBatch):
                restarted.complete('instruction', {'text': 'refused'})
            self.assertEqual(self.opener.open.call_count, calls)
            self.assertEqual(restarted.summary()['refusals'], {})
            self.assertEqual(restarted.summary()['cached_refusals'], {'400:unknown': 1})
            self.assertEqual(restarted.complete('instruction', {'text': 'changed'}), {'answer': 43})
            self.assertEqual(self.opener.open.call_count, calls + 1)
            for path in pathlib.Path(directory).rglob('*'):
                if path.is_file():
                    self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                    self.assertNotIn(b'private-key', path.read_bytes())
            self.assertEqual(pathlib.Path(directory).stat().st_mode & 0o777, 0o700)

    def test_concurrent_reservations_keep_hard_caps(self):
        client = live.Chat({**config(), 'max_output_tokens': 2000})
        barrier = threading.Barrier(2)
        def answer(*args, **kwargs):
            barrier.wait(timeout=2)
            # Unknown usage retains the full reservation.
            return io.BytesIO(b'{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}')
        self.opener.open.side_effect = answer
        def call(i):
            try:
                return client.complete('instruction', {'i': i})
            except live.AdapterError:
                return None
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
            results = list(executor.map(call, range(8)))
        self.assertEqual(results.count({}), 2)
        self.assertEqual(self.opener.open.call_count, 2)
        self.assertEqual(client.summary()['budgeted_output_tokens'], 2000)
        self.assertTrue(client.summary()['stopped'])

    def test_parallel_build_resumes_paid_calls_and_keeps_seeded_output(self):
        from jev_rerank.client import Result
        from test_news_set import articles, unavailable_scorer
        lock = threading.Lock()
        calls, active, peak = [], 0, 0
        generation_barrier, judging_barrier = threading.Barrier(2), threading.Barrier(8)
        gated = {'generation': 0, 'judging': 0}
        def enter(stage):
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
                gated[stage] += 1
                gate = gated[stage] <= (2 if stage == 'generation' else 8)
            if gate:
                (generation_barrier if stage == 'generation' else judging_barrier).wait(timeout=3)
        def leave():
            nonlocal active
            with lock:
                active -= 1
        by_text = {a.text: a.id for a in articles()}
        def sources(query):
            value = query.split(' preuves ', 1)[1]
            return set(value.split(',')) if value != 'aucune' else set()
        fail = {'enabled': True, 'malformed_nonce': None}
        conflicts = set()
        def answer(request, **kwargs):
            body = json.loads(request.data)
            data = json.loads(body['messages'][1]['content'])
            stage = 'generation' if 'kind' in data else 'judging'
            enter(stage)
            try:
                with lock:
                    calls.append(request.data)
                if stage == 'generation':
                    with lock:
                        if fail['malformed_nonce'] is None:
                            fail['malformed_nonce'] = data['nonce']
                    if data['nonce'] == fail['malformed_nonce']:
                        return response('{"questions":[]}')
                    ids = [] if data['kind'] == 'no_answer' else [a['id'] for a in data['articles']]
                    if data['kind'] in ('entity', 'no_answer') and data['count'] == 25:
                        evidence = ','.join(by_text[a['text']] for a in data['articles']) if ids else 'aucune'
                        conflicts.add(f"Dossier {data['kind']} {data['nonce']} 0 preuves {evidence}")
                    return response(json.dumps({'questions': [
                        {'text': f"Dossier {data['kind']} {data['nonce']} {i} preuves {','.join(by_text[a['text']] for a in data['articles']) if ids else 'aucune'}",
                         'sources': ids} for i in range(data['count'])]}))
                if fail['enabled'] and 'Dossier event ' in data['query'] and body['model'] == 'judge-a':
                    raise urllib.error.HTTPError('private-url', 401, 'private-key', {}, io.BytesIO(b'private-body'))
                # One inconsistent question per full generation batch; top-ups
                # have smaller batches and produce consistent replacement rows.
                conflict = data['query'] in conflicts
                return response(json.dumps({'grades': {a['id']: (3 if 'Dossier no_answer ' in data['query'] else 0) if conflict else
                        3 if by_text[a['text']] in sources(data['query']) else 0 for a in data['articles']}}))
            finally:
                leave()
        class JevTransport:
            def judge(self, query, passages, deadline, cost_limit):
                enter('judging')
                try:
                    return Result(scores={k: float(any(text in passage and marker in sources(query)
                                                   for text, marker in by_text.items()))
                                          for k, passage in passages.items()},
                                  paid_calls=1, input_tokens=100)
                finally:
                    leave()
        def factory(concurrency):
            retrievers = {s: news.FakeRetriever() for s in news.SYSTEMS}
            hosted = types.SimpleNamespace(reuse=live.Reuse())
            for retriever in retrievers.values():
                retriever.retrieval = types.SimpleNamespace(hosted=hosted)
            cfg = {**config(), 'max_input_tokens': 10000000, 'max_output_tokens': 1000000}
            jev_budget = live.embeddings.Budget(10000000, 1)
            return live.LiveProviders(live.ChatGenerator(cfg), retrievers,
                [live.ChatJudge({**cfg, 'model': 'judge-a', 'family': 'a'}),
                 live.ChatJudge({**cfg, 'model': 'judge-b', 'family': 'b'}),
                 news.JevJudge(live.CappedJev(JevTransport(), jev_budget))], 'hybrid',
                synthetic=True, concurrency=concurrency,
                embedding_budget=live.embeddings.Budget(100000, 1), jev_budget=jev_budget,
                resume_config={'test-model': 'v1'})
        self.opener.open.side_effect = answer
        with tempfile.TemporaryDirectory() as directory, mock.patch('scoring.score', side_effect=unavailable_scorer):
            first = factory(8)
            first.enable_cache(directory)
            salt = first.prepare_resume(articles(), 300, 42)
            with self.assertRaises(news.BuildError) as caught:
                news.build(articles(), first, 300, 42)
            self.assertEqual(caught.exception.diagnostic['http_status'], 401)
            self.assertEqual(peak, 8)
            paid = set(calls)
            first.response_cache.close()
            fail['enabled'] = False
            calls.clear()
            resumed = factory(8)
            resumed.enable_cache(directory)
            result = news.build(articles(), resumed, 300, 42)
            # Failed authentication requests are intentionally never cached.
            cached_successes = paid - {raw for raw in paid if b'Dossier event ' in raw and b'judge-a' in raw}
            self.assertFalse(cached_successes & set(calls))
            usage = resumed.usage()
            news.validate_usage(usage)
            self.assertGreater(usage['generator']['cached_calls'], 0)
            self.assertEqual(result.report['rejected']['invalid_count'], 1)
            self.assertEqual(result.report['rejected']['answer_expected_none_found'], 2)
            self.assertEqual(result.report['rejected']['no_answer_but_relevant_found'], 2)
            self.assertGreater(usage['judge_1']['cached_calls'], 0)
            self.assertGreater(usage['jev']['cached_calls'], 0)
            self.assertGreater(usage['totals']['cached_cost_usd'], 0)
            resumed.response_cache.close()
            serial = news.build(articles(), factory(1), 300, 42, salt=salt)
            self.assertEqual(result.version, serial.version)
            calls.clear()
            reused = factory(8)
            reused.enable_cache(directory)
            again = news.build(articles(), reused, 300, 42)
            self.assertEqual(again.version, result.version)
            self.assertEqual(calls, [])
            self.assertEqual(reused.usage()['totals']['confirmed_cost_usd'], 0)
            self.assertEqual(reused.usage()['totals']['cost_upper_bound_usd'], 0)
            reused.response_cache.close()

    def test_shortfall_replays_identical_accepted_questions_without_paid_calls(self):
        from test_news_set import articles, unavailable_scorer
        # Real generator/cache boundary: only HTTP is faked. Most no-answer
        # queries have relevant candidates, so the builder must retain one.
        from jev_rerank.client import Result
        calls = []
        by_text = {a.text: a.id for a in articles()}
        def relevant(query, text):
            if query.startswith('Dossier no_answer '):
                return not query.split(' preuves ', 1)[0].endswith(' 0')
            return by_text[text] in query.split(' preuves ', 1)[1].split(',')
        def answer(request, **kwargs):
            calls.append(request.data)
            data = json.loads(json.loads(request.data)['messages'][1]['content'])
            if 'kind' not in data:
                return response(json.dumps({'grades': {a['id']: 3 if relevant(data['query'], a['text']) else 0
                                                        for a in data['articles']}}))
            ids = [] if data['kind'] == 'no_answer' else [a['id'] for a in data['articles']]
            evidence = ','.join(by_text[a['text']] for a in data['articles']) if ids else 'aucune'
            return response(json.dumps({'questions': [
                {'text': f"Dossier {data['kind']} {data['nonce']} {i} preuves {evidence}", 'sources': ids}
                for i in range(data['count'])]}))
        class JevTransport:
            def judge(self, query, passages, deadline, cost_limit):
                calls.append(('jev', query))
                return Result(scores={key: float(any(text in passage and relevant(query, text)
                                                    for text in by_text)) for key, passage in passages.items()},
                              paid_calls=1, input_tokens=100)
        def factory():
            retrievers = {s: news.FakeRetriever() for s in news.SYSTEMS}
            hosted = types.SimpleNamespace(reuse=live.Reuse())
            for retriever in retrievers.values():
                retriever.retrieval = types.SimpleNamespace(hosted=hosted)
            budget = live.embeddings.Budget(1000000, 1)
            providers = live.LiveProviders(live.ChatGenerator(config()), retrievers,
                [live.ChatJudge({**config(), 'family': family}) for family in ('a', 'b')] +
                [news.JevJudge(live.CappedJev(JevTransport(), budget))], 'hybrid', synthetic=True,
                question_targets={'entity': 3, 'no_answer': 3}, attempt_budgets={'no_answer': 1}, min_questions=12,
                embedding_budget=live.embeddings.Budget(1000000, 1), jev_budget=budget,
                resume_config={'model': 'fake-v1'})
            providers.enable_cache(directory)
            return providers
        self.opener.open.side_effect = answer
        with tempfile.TemporaryDirectory() as directory, mock.patch('scoring.score', side_effect=unavailable_scorer):
            first = factory()
            try:
                result = news.build(articles(), first, 12, 42)
            finally:
                first.response_cache.close()
            self.assertEqual(result.report['questions'], 12)
            self.assertEqual(result.report['question_types']['entity'], 3)
            self.assertEqual(result.report['question_targets']['no_answer'],
                             {'target': 3, 'accepted': 1, 'shortfall': 2, 'attempts': 1, 'attempt_budget': 1})
            self.assertEqual(result.report['split'], {'working': 7, 'held_out': 5})
            calls.clear()
            replay = factory()
            try:
                again = news.build(articles(), replay, 12, 42)
                self.assertEqual(again.version, result.version)
                self.assertEqual(again.working, result.working)
                self.assertEqual(again.held_out, result.held_out)
                self.assertEqual(calls, [])
                self.assertEqual(replay.usage()['totals']['confirmed_cost_usd'], 0)
                replay.min_questions = 13
                with self.assertRaises(news.BuildError) as caught:
                    news.build(articles(), replay, 12, 42)
                self.assertEqual(caught.exception.diagnostic['reason'], 'min_questions_not_met')
                self.assertEqual(caught.exception.diagnostic['accepted'], 12)
                self.assertEqual(caught.exception.diagnostic['min_questions'], 13)
                self.assertEqual(calls, [])
            finally:
                replay.response_cache.close()

    def test_cache_keeps_embedding_batches_and_jev_refusals_private(self):
        from jev_rerank.client import MAX_TOKENS, Result
        with tempfile.TemporaryDirectory() as directory:
            cache = live.ResponseCache(directory)
            self.addCleanup(cache.close)
            embedding_budget = live.embeddings.Budget(10000, 1)
            hosted = live.CachedHosted('https://example.invalid', 'private-key', embedding_budget,
                                      'news', prices={'Cohere-test': .12})
            hosted.response_cache = cache
            self.opener.open.side_effect = lambda *args, **kwargs: io.BytesIO(json.dumps({
                'embeddings': {'float': [[1, 0]]}, 'meta': {'billed_units': {'input_tokens': 10}}}).encode())
            self.assertEqual(hosted.embed('Cohere-test', ['private-text'], 'document', dimensions=2), [[1, 0]])
            spend = embedding_budget.summary()['confirmed_cost_usd']
            self.assertGreater(spend, 0)
            self.assertEqual(hosted.embed('Cohere-test', ['private-text'], 'document', dimensions=2), [[1, 0]])
            self.assertEqual(self.opener.open.call_count, 1)
            self.assertEqual(embedding_budget.summary()['confirmed_cost_usd'], spend)
            self.assertEqual(hosted.reuse.summary()['cached_calls'], 1)
            transport = mock.Mock(spec=['judge'])
            transport.judge.return_value = Result(reason='HTTP 422', input_tokens=MAX_TOKENS,
                                                  estimated_tokens=MAX_TOKENS)
            client = live.CappedJev(transport, live.embeddings.Budget(10000000, 1))
            client.response_cache = cache
            judge = news.JevJudge(client)
            q = news.Question('private-query', 'event', '2026-01-02', ('a',))
            candidates = [news.Article(k, '', 'private-text-' + k, q.date) for k in 'abcd']
            self.assertEqual(judge.grade(q, candidates), dict.fromkeys('abcd'))
            self.assertEqual(transport.judge.call_count, 3)
            self.assertEqual(client.refusals, {'422:unknown': 3})
            self.assertEqual(judge.grade(q, candidates), dict.fromkeys('abcd'))
            self.assertEqual(transport.judge.call_count, 3)
            self.assertEqual(client.reuse.summary()['cached_calls'], 3)
            self.assertEqual(client.refusals, {'422:unknown': 3})
            self.assertEqual(client.reuse.summary()['cached_refusals'], {'422:unknown': 3})
            for reason in ('HTTP 401', 'HTTP 402', 'HTTP 403', 'HTTP 405', 'HTTP 407', 'HTTP 415', 'provider refused (payment)'):
                transport.judge.return_value = Result(reason=reason)
                with self.assertRaises(news.BuildError):
                    judge.grade(news.Question(reason, q.kind, q.date, q.sources), candidates)

    def test_cache_identity_permissions_and_exclusive_local_access(self):
        with tempfile.TemporaryDirectory() as directory:
            cache = live.ResponseCache(directory)
            original = cache.salt({'seed': 42, 'config': 'v1', 'article': 'a'})
            self.assertEqual(cache.salt({'seed': 42, 'config': 'v1', 'article': 'a'}), original)
            for field, value in [('seed', 43), ('config', 'v2'), ('article', 'b')]:
                identity = {'seed': 42, 'config': 'v1', 'article': 'a', field: value}
                self.assertNotEqual(cache.salt(identity), original)
            with self.assertRaises(ValueError):
                live.ResponseCache(directory)
            key = live.digest('entry')
            cache.put(key, {'value': 1})
            path = pathlib.Path(directory) / (key + '.json')
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                cache.get(key)
            path.unlink()
            path.symlink_to(pathlib.Path(directory) / 'lock')
            with self.assertRaises(OSError):
                cache.get(key)
            cache.close()
        with self.assertRaises(ValueError):
            live.ResponseCache(pathlib.Path(news.__file__).resolve().parents[2] / '.context/cache-test')

    def test_generator_and_judge_use_json_contract_and_keep_evidence_out_of_judging(self):
        article = news.Article('abcdef0123456789' * 4, 'Un port', 'Le ferry ouvre lundi.', '2026-01-02',
                               updated_at='2026-01-02T12:00:00Z', source='private-source', credit='private-credit',
                               story_id='private-story', previous_versions=(
                                   {'id': 'earlier', 'text': 'Ouverture prévue', 'updated_at': '2026-01-02T09:00:00Z'},))
        self.opener.open.side_effect = [response(json.dumps({'questions': [
            {'text': 'Quand part le navire ?', 'sources': ['a1']}]})), response('{"grades":{"a1":3}}')]
        generator = live.ChatGenerator(config())
        q = generator.generate([article], 'event', 1, random.Random(1))[0]
        self.assertEqual(q, news.Question('Quand part le navire ?', 'event', '2026-01-02', (article.id,)))
        self.assertEqual(live.ChatJudge({**config(), 'auth_header': 'bearer'}).grade(q, [article]), {article.id: 3})
        requests = [c.args[0] for c in self.opener.open.call_args_list]
        self.assertEqual(requests[0].full_url, 'https://example.invalid/openai/v1/chat/completions')
        self.assertEqual(requests[0].get_header('Api-key'), 'private-key')
        self.assertEqual(requests[1].get_header('Authorization'), 'Bearer private-key')
        self.assertIsNone(requests[1].get_header('Api-key'))
        for request in requests:
            self.assertNotIn(article.id.encode(), request.data)
            body = json.loads(request.data)
            self.assertEqual(body['response_format'], {'type': 'json_object'})
            self.assertRegex(body['messages'][0]['content'].lower(), r'return[^.]*\bjson\b')
        generator_input = json.loads(json.loads(requests[0].data)['messages'][1]['content'])
        self.assertEqual(generator_input['articles'][0]['previous_versions'][0]['text'], 'Ouverture prévue')
        self.assertNotIn('id', generator_input['articles'][0]['previous_versions'][0])
        for field in ('source', 'credit', 'story_id'):
            self.assertNotIn(field, generator_input['articles'][0])
        judge_input = json.loads(json.loads(requests[1].data)['messages'][1]['content'])
        self.assertNotIn('sources', judge_input)
        self.assertNotIn('kind', judge_input)
        self.assertNotIn('previous_versions', judge_input['articles'][0])
        self.assertEqual(judge_input['articles'][0]['updated_at'], '2026-01-02T12:00:00Z')
        self.assertEqual(generator.summary()['confirmed_cost_usd'], .00014)

    def test_judge_recovers_mistyped_long_ids_and_splits_invalid_batches_once(self):
        articles = [news.Article(char * 64, '', char, '2026-01-02') for char in 'abcd']
        question = news.Question('Query', 'event', '2026-01-02', (articles[2].id,))
        batches = []
        def answer(request, **kwargs):
            batch = json.loads(json.loads(request.data)['messages'][1]['content'])['articles']
            batches.append([a['text'] for a in batch])
            # Reproduce the observed failure when prompted with long ids;
            # the last half remains malformed even with aliases.
            if len(batch) > 2 or batch[0]['text'] == 'c' or len(batch[0]['id']) == 64:
                return response(json.dumps({'grades': {a['id'][:-1]: 3 for a in batch}}))
            return response('{"grades":{"a1":3,"a2":1}}')
        self.opener.open.side_effect = answer
        judge = live.ChatJudge({**config(), 'max_retries': 1})
        self.assertEqual(judge.grade(question, articles),
                         {articles[0].id: 3, articles[1].id: 1, articles[2].id: None, articles[3].id: None})
        self.assertEqual(batches, [list('abcd'), list('abcd'), list('ab'), list('cd'), list('cd')])
        self.assertEqual(judge.summary()['rejected'], {'missing_ids': 4, 'unknown_ids': 4})
        import jsonschema
        schema = json.loads(pathlib.Path(news.__file__).with_name('news-report.schema.json').read_text())
        jsonschema.Draft202012Validator(schema['$defs']['chat_usage']).validate(judge.summary())

    def test_alias_cache_ignores_long_id_requests_and_remaps_on_reuse(self):
        articles = [news.Article('f' * 64, 'title', 'text', '2026-01-02')]
        question = news.Question('Query', 'event', articles[0].date, (articles[0].id,))
        with tempfile.TemporaryDirectory() as directory:
            cache = live.ResponseCache(directory)
            self.addCleanup(cache.close)
            for cls in (live.ChatJudge, live.ChatGenerator):
                with self.subTest(adapter=cls.__name__):
                    self.opener.open.reset_mock()
                    client = cls(config())
                    client.response_cache = cache
                    def call(sample):
                        if cls is live.ChatJudge:
                            return client.grade(question, sample)
                        return client.generate(sample, 'event', 1, random.Random(1))
                    expected_value = ({'grades': {'a1': 2}} if cls is live.ChatJudge else
                                      {'questions': [{'text': 'Question', 'sources': ['a1']}]})
                    def answer(request, **kwargs):
                        # Seed a legacy full-id response under the same prompt:
                        # even without a prompt change its body has another key.
                        body = json.loads(request.data)
                        data = json.loads(body['messages'][1]['content'])
                        data['articles'][0]['id'] = articles[0].id
                        legacy_raw = client.request_body(body['messages'][0]['content'], data)
                        key = live.digest(['chat-v1', client.url, client.cfg, legacy_raw.decode()])
                        legacy = ({'grades': {articles[0].id: 0}} if cls is live.ChatJudge else
                                  {'questions': [{'text': 'Stale', 'sources': [articles[0].id]}]})
                        cache.put(key, {'value': legacy})
                        return response(json.dumps(expected_value))
                    self.opener.open.side_effect = answer
                    first = call(articles)
                    raw = self.opener.open.call_args.args[0].data.decode()
                    alias_key = live.digest(['chat-v1', client.url, client.cfg, raw])
                    (pathlib.Path(directory) / (alias_key + '.json')).unlink()
                    # Only the legacy entry remains: it must miss the cache.
                    self.assertEqual(call(articles), first)
                    # Same content, different real id: cached aliases must be
                    # interpreted through the current request's local mapping.
                    remapped = [news.Article('e' * 64, 'title', 'text', '2026-01-02')]
                    second = call(remapped)
                    if cls is live.ChatJudge:
                        self.assertEqual(first, {articles[0].id: 2})
                        self.assertEqual(second, {remapped[0].id: 2})
                    else:
                        self.assertEqual(first[0].sources, (articles[0].id,))
                        self.assertEqual(second[0].sources, (remapped[0].id,))
                        self.assertEqual(second[0].text, 'Question')
                    self.assertEqual(self.opener.open.call_count, 2)
                    self.assertEqual(client.summary()['cached_calls'], 1)

    def test_cli_reports_only_safe_failure_fields_at_the_failing_phase(self):
        cases = [
            ('generation', 400, b'{"error":{"code":"invalid_request_error","message":"private-body private-key"}}',
             'HTTPError', 'invalid_request_error'),
            ('judging', 503, b'{"error":{"code":"service_unavailable","message":"private-body"}}',
             'HTTPError', 'service_unavailable'),
            ('generation', 429, b'{"error":{"code":"rate_limit_exceeded"}}', 'HTTPError', 'rate_limit_exceeded'),
            ('generation', 400, b'private-body', 'HTTPError', None),
            ('generation', 400, b'{"error":[]}', 'HTTPError', None),
            ('generation', 400, b'{"error":{"code":"private-key\\nprivate-text"}}', 'HTTPError', None),
            ('generation', 400, b'{"error":{"code":"https://private-url/private-key"}}', 'HTTPError', None),
            ('generation', 400, b'{"error":{"code":123}}', 'HTTPError', None),
            ('generation', 400, b'{"error":{"code":"private-key"}}', 'HTTPError', None),
            ('generation', 400, b'{"error":{"code":"invalid_request_error"},"padding":"' + b'x' * 65536 + b'"}',
             'HTTPError', None),
            ('generation', None, None, 'TimeoutError', None),
            ('retrieval', None, None, 'TimeoutError', None),
            ('retrieval', None, b'invalid-candidates', 'BuildError', None),
        ]
        with tempfile.TemporaryDirectory() as directory:
            for phase, status, payload, exception, code in cases:
                with self.subTest(phase=phase, status=status, payload=payload[:80] if payload else None):
                    self.opener.open.reset_mock()
                    client = (live.ChatGenerator if phase == 'generation' else live.ChatJudge)(
                        {**config(), 'max_retries': 0})
                    error = (urllib.error.HTTPError('https://private-url', status, 'private-body', {}, io.BytesIO(payload))
                             if status else TimeoutError('private-key private-text private-query'))
                    self.opener.open.side_effect = error
                    providers = news.fake_providers()
                    if phase == 'generation':
                        providers.generator = client
                    elif phase == 'judging':
                        providers.judges[0] = client
                    else:
                        providers.retrievers['bm25'] = mock.Mock()
                        if exception == 'BuildError':
                            providers.retrievers['bm25'].search.return_value = ['unknown-article']
                        else:
                            providers.retrievers['bm25'].search.side_effect = error
                    output, stderr = io.StringIO(), io.StringIO()
                    with mock.patch('news_set.fake_providers', return_value=providers), \
                         mock.patch.dict(os.environ, QUIVR_NEWS_WORKING_RECIPIENTS='working',
                                         QUIVR_NEWS_HOLDOUT_RECIPIENTS='holdout', QUIVR_NEWS_REVIEW_RECIPIENTS='review'), \
                         contextlib.redirect_stdout(output), contextlib.redirect_stderr(stderr):
                        result = news.main(['--fake', '--questions', '6', '--storage-dir', directory,
                                            '--report', str(pathlib.Path(directory) / 'report.json')])
                    self.assertEqual(result, 2)
                    expected = {'exception': exception, 'phase': phase}
                    if status in (429, 503):
                        expected['reason'] = 'chat provider refused the request'
                    elif phase == 'generation' and status is None:
                        expected['reason'] = 'chat transport failed'
                    elif phase == 'retrieval':
                        expected['reason'] = ('retriever returned invalid candidate ids' if exception == 'BuildError'
                                              else 'news-set phase failed')
                    if status:
                        expected['http_status'] = status
                    if code:
                        expected['provider_code'] = code
                    if status == 400:
                        expected = {'phase': phase, 'reason': 'min_questions_not_met', 'accepted': 0, 'min_questions': 6,
                                    'question_targets': {kind: {'target': 1, 'accepted': 0, 'shortfall': 1,
                                        'attempts': 10, 'attempt_budget': 10} for kind in news.KINDS},
                                    'rejected': {'request_refused': 60}}
                    self.assertEqual(json.loads(output.getvalue().split('diagnostic: ', 1)[1]), expected)
                    for private in ('private-body', 'private-key', 'private-text', 'private-query', 'private-url'):
                        self.assertNotIn(private, output.getvalue() + stderr.getvalue())
                    self.assertEqual(self.opener.open.call_count, 0 if phase == 'retrieval' else 60 if status == 400 else 1)

    def test_retries_charge_unknown_attempts_and_caps_block_before_http(self):
        client = live.ChatJudge(config())
        self.opener.open.side_effect = [urllib.error.HTTPError('private-url', 429, 'private-body',
                                       {'Retry-After': '999999'}, io.BytesIO(b'private-body')), response('{"grades":{"a1":2}}')]
        q, articles = news.Question('Question ?', 'event', '2026-01-02', ('a',)), [news.Article('a', '', 'texte', '2026-01-02')]
        with mock.patch('news_providers.time.sleep') as sleep:
            self.assertEqual(client.grade(q, articles), {'a': 2})
        self.assertLessEqual(sleep.call_args.args[0], 10)
        self.assertEqual(client.summary()['attempts'], 2)
        self.assertEqual(client.summary()['retries'], 1)
        self.assertEqual(client.summary()['timeouts'], 0)
        self.assertGreater(client.summary()['cost_upper_bound_usd'], client.summary()['confirmed_cost_usd'])
        capped = live.ChatJudge({**config(), 'max_usd': .00001})
        calls = self.opener.open.call_count
        with self.assertRaises(live.AdapterError):
            capped.grade(q, articles)
        self.assertEqual(self.opener.open.call_count, calls)

    def test_transient_transport_failures_retry_and_keep_unknown_reservations(self):
        failures = [TimeoutError('private-text'), socket.timeout('private-text'),
                    urllib.error.URLError(socket.timeout('private-text')),
                    urllib.error.URLError(OSError(errno.ETIMEDOUT, 'private-text')),
                    urllib.error.URLError(ConnectionResetError('private-text')),
                    urllib.error.URLError(ConnectionRefusedError('private-text')),
                    urllib.error.URLError(socket.gaierror(socket.EAI_AGAIN, 'private-text')),
                    ConnectionResetError('private-text'), http.client.RemoteDisconnected('private-text'),
                    http.client.IncompleteRead(b'private-text', 100)]
        for error in failures:
            for stage in ('open', 'read'):
                with self.subTest(error=type(error).__name__, stage=stage):
                    self.opener.open.reset_mock()
                    failed_response = mock.MagicMock()
                    failed_response.__enter__.return_value.read.side_effect = error
                    self.opener.open.side_effect = [error if stage == 'open' else failed_response,
                                                  response('{"answer":42}')]
                    client = live.Chat({**config(), 'timeout_seconds': 120})
                    with mock.patch('news_providers.time.sleep') as sleep:
                        self.assertEqual(client.complete('instruction', {'text': 'private-text'}), {'answer': 42})
                    self.assertEqual(self.opener.open.call_count, 2)
                    sleep.assert_called_once_with(1)
                    summary = client.summary()
                    self.assertEqual(summary['attempts'], 2)
                    self.assertEqual(summary['retries'], 1)
                    self.assertEqual(summary['timeouts'], int(error in failures[:4]))
                    request = self.opener.open.call_args.args[0]
                    self.assertEqual(self.opener.open.call_args.kwargs['timeout'], 120)
                    inputs = len(request.data) + 128
                    self.assertEqual(summary['budgeted_input_tokens'], inputs + 100)
                    self.assertEqual(summary['budgeted_output_tokens'], 1020)
                    self.assertAlmostEqual(summary['cost_upper_bound_usd'], (inputs + 100 + 2 * 1020) / 1000000)
                    self.assertEqual(summary['confirmed_input_tokens'], 100)
                    self.assertEqual(summary['confirmed_output_tokens'], 20)

    def test_timeouts_stop_at_retry_or_reservation_bounds_without_leaking_details(self):
        inputs = live.Chat(config()).input_bound('instruction', {})
        for limits, attempts in [({}, 3), ({'max_retries': 0}, 1),
                                 ({'max_input_tokens': inputs}, 1),
                                 ({'max_output_tokens': 1000}, 1),
                                 ({'max_usd': (inputs + 2000) / 1000000}, 1)]:
            with self.subTest(limits=limits):
                self.opener.open.reset_mock()
                self.opener.open.side_effect = TimeoutError('private-key private-url private-text')
                client = live.Chat({**config(), **limits})
                with mock.patch('news_providers.time.sleep') as sleep, self.assertRaises(live.AdapterError) as caught:
                    client.complete('instruction', {})
                self.assertEqual(self.opener.open.call_count, attempts)
                self.assertEqual(client.summary()['timeouts'], attempts)
                self.assertEqual(client.summary()['retries'], attempts - 1)
                self.assertEqual(client.summary()['budgeted_input_tokens'], inputs * attempts)
                self.assertEqual(client.summary()['budgeted_output_tokens'], 1000 * attempts)
                self.assertEqual(client.summary()['confirmed_cost_usd'], 0)
                self.assertEqual(client.summary()['stopped'], bool(set(limits) - {'max_retries'}))
                if attempts == 3:
                    self.assertEqual(sleep.call_args_list, [mock.call(1), mock.call(2)])
                    self.assertEqual(caught.exception.diagnostic, {'exception': 'TimeoutError'})
                for private in ('private-key', 'private-url', 'private-text'):
                    self.assertNotIn(private, str(caught.exception) + json.dumps(caught.exception.diagnostic))

    def test_permanent_url_failures_do_not_retry(self):
        reasons = [socket.gaierror(socket.EAI_NONAME, 'private-url'),
                   ssl.SSLCertVerificationError('private-key'), ValueError('private-url'),
                   'private-url', OSError(errno.EINVAL, 'private-text')]
        for reason in reasons:
            with self.subTest(reason=type(reason).__name__):
                self.opener.open.reset_mock()
                self.opener.open.side_effect = urllib.error.URLError(reason)
                client = live.Chat(config())
                with mock.patch('news_providers.time.sleep') as sleep, self.assertRaises(live.AdapterError) as caught:
                    client.complete('instruction', {})
                self.assertEqual(self.opener.open.call_count, 1)
                sleep.assert_not_called()
                self.assertEqual(client.summary()['timeouts'], 0)
                self.assertEqual(client.summary()['retries'], 0)
                self.assertEqual(caught.exception.diagnostic, {'exception': 'URLError'})

    def test_chat_timeout_configuration_accepts_reasoning_models_and_rejects_invalid_bounds(self):
        for timeout in (1, 120, 121, 600):
            with self.subTest(timeout=timeout):
                self.assertEqual(live.Chat.settings({**config(), 'timeout_seconds': timeout})['timeout_seconds'], timeout)
        for timeout in (0, 601, True, 60.0, '600'):
            with self.subTest(timeout=timeout), self.assertRaises(live.AdapterError):
                live.Chat.settings({**config(), 'timeout_seconds': timeout})
        self.opener.open.assert_not_called()

    def test_invalid_provider_output_is_strict_sanitised_and_never_a_negative_vote(self):
        cases = [('{"grades":{"a1":true}}', {'bad_values': 3}),
                 ('{"grades":{"a1":4}}', {'bad_values': 3}),
                 ('{"grades":{"a1":1.0}}', {'bad_values': 3}),
                 ('{"grades":{"a1":1,"a1":2}}', {'invalid_json': 3}),
                 ('{"grades":{"a1":NaN}}', {'invalid_json': 3}),
                 ('{"grades":{}}', {'missing_ids': 3}),
                 ('{"grades":{"a1":0,"other":0}}', {'unknown_ids': 3}),
                 ('{"grades":{"other":0}}', {'missing_ids': 3, 'unknown_ids': 3}),
                 ('```json\n{"grades":{"a1":3}}\n```', {'invalid_json': 3}),
                 ('{"grades":{"a1":0},"secret":"private-text"}', {'extra_fields': 3}),
                 ('{"grades":null}', {'bad_values': 3}),
                 ('{}', {'bad_values': 3}), ('[]', {'bad_values': 3})]
        for content, reasons in cases:
            with self.subTest(content=content):
                self.opener.open.reset_mock()
                self.opener.open.side_effect = lambda *args, **kwargs: response(content)
                judge = live.ChatJudge(config())
                self.assertEqual(judge.grade(news.Question('private-query', 'event', '2026-01-02', ('a',)),
                                             [news.Article('a', '', 'private-text', '2026-01-02')]), {'a': None})
                self.assertEqual(judge.summary()['rejected'], reasons)
                self.assertNotIn('private', json.dumps(judge.summary()))
                self.assertEqual(self.opener.open.call_count, 3)
        self.opener.open.side_effect = None
        self.opener.open.return_value = response('{"grades":{"a1":3}}', completion=1001)
        client = live.ChatJudge(config())
        with self.assertRaises(live.AdapterError):
            client.grade(news.Question('q', 'event', '2026-01-02', ('a',)), [news.Article('a', '', 't', '2026-01-02')])
        self.assertTrue(client.summary()['stopped'])

    def test_unknown_usage_keeps_a_reservation_covering_escaped_request_bytes(self):
        self.opener.open.return_value = io.BytesIO(json.dumps({'choices': [
            {'finish_reason': 'stop', 'message': {'content': '{"grades":{"a1":3}}'}}]}).encode())
        judge = live.ChatJudge(config())
        judge.grade(news.Question('"\\' * 500, 'event', '2026-01-02', ('a',)),
                    [news.Article('a', '', 'text', '2026-01-02')])
        request = self.opener.open.call_args.args[0]
        self.assertGreaterEqual(judge.summary()['budgeted_input_tokens'], len(request.data))
        self.assertEqual(judge.summary()['confirmed_input_tokens'], 0)

    def test_generation_recovers_whole_batches_and_reports_fixed_reasons(self):
        from test_news_set import articles, unavailable_scorer
        cases = [
            ('{"questions":[]}', 'invalid_count'),
            ('{"questions":[{"text":"discard me","sources":[]},'
             '{"text":"bad","sources":["unknown"]}]}', 'invalid_evidence'),
            ('{"questions":[{"text":"bad","sources":[],"extra":"private-text"},{}]}', 'invalid_questions'),
            ('{"questions":null}', 'invalid_questions'),
            ('{"questions":[],"extra":"private-text"}', 'invalid_questions'),
            ('private-text', 'invalid_json'),
            ('truncated', 'invalid_response'),
            ('http_content_filter', 'content_filter'),
            ('http_request_refused', 'request_refused'),
        ]
        for content, reason in cases:
            with self.subTest(reason=reason, content=content):
                self.opener.open.reset_mock()
                def answer(request, **kwargs):
                    data = json.loads(json.loads(request.data)['messages'][1]['content'])
                    if self.opener.open.call_count == 1:
                        if content == 'http_request_refused':
                            raise urllib.error.HTTPError('private-url', 400, 'private-body', {}, io.BytesIO(b'private-key'))
                        if content == 'http_content_filter':
                            raise urllib.error.HTTPError('private-url', 400, 'private-body', {},
                                io.BytesIO(b'{"error":{"code":"content_filter","message":"private-key"}}'))
                        if content == 'truncated':
                            return response('{}', finish='length')
                        if 'discard me' in content:
                            malformed = json.loads(content)
                            malformed['questions'][0]['sources'] = [data['articles'][0]['id']]
                            return response(json.dumps(malformed))
                        return response(content)
                    sources = [] if data['kind'] == 'no_answer' else [a['id'] for a in data['articles']]
                    return response(json.dumps({'questions': [
                        {'text': f"Quelle évolution maritime concerne le dossier {data['nonce']} {i} ?",
                         'sources': sources} for i in range(data['count'])]}))
                self.opener.open.side_effect = answer
                providers = news.fake_providers()
                providers.generator = live.ChatGenerator(config())
                with mock.patch('scoring.score', side_effect=unavailable_scorer):
                    result = news.build(articles(), providers, 12, 42, salt=b'x' * 32)
                self.assertEqual(result.report['questions'], 12)
                self.assertEqual(result.report['question_types'], dict.fromkeys(news.KINDS, 2))
                self.assertEqual(result.report['rejected'][reason], 1)
                self.assertNotIn('discard me', list(result.working['queries'].values()) +
                                 list(result.held_out['queries'].values()))
                self.assertEqual(providers.generator.summary()['attempts'], 7)
                self.assertEqual(providers.generator.summary()['rejected'], {reason: 1})
                self.assertEqual(providers.generator.summary()['confirmed_output_tokens'],
                                 120 if reason in ('content_filter', 'request_refused') else 140)
                if reason == 'content_filter':
                    samples = [json.loads(json.loads(call.args[0].data)['messages'][1]['content'])['articles']
                               for call in self.opener.open.call_args_list[:2]]
                    self.assertNotEqual(samples[0][0]['text'], samples[1][0]['text'])
                    self.assertEqual(result.report['content_filter']['filtered_generation_share'], 1 / 7)
                news.validate_report(result.report)

    def test_judge_filters_are_isolated_union_counted_and_resampled_at_threshold(self):
        from test_news_set import articles, unavailable_scorer
        # One union exclusion below the cap; two at the cap; equality with
        # one; and removal of the last positive below the cap.
        for filtered_count, cap, filter_source, dropped in [
                (1, .25, False, 0), (2, .25, False, 1),
                (1, .125, False, 1), (1, .25, True, 1)]:
            with self.subTest(filtered_count=filtered_count, cap=cap, filter_source=filter_source):
                self.opener.open.reset_mock()
                state = {'first': None, 'question': None, 'filtered': None}
                by_text = {}
                def retrieve(question, corpus, limit):
                    by_text.update({a.text: a.id for a in corpus})
                    state['question'] = question
                    if state['first'] is None:
                        state['first'] = question.text
                        ids = sorted(a.id for a in corpus if a.id not in question.sources)
                        state['filtered'] = (list(question.sources) if filter_source else ids[:filtered_count])
                    return [a.id for a in corpus]
                def answer(request, **kwargs):
                    body = json.loads(request.data)
                    data = json.loads(body['messages'][1]['content'])
                    aliases = {a['id']: by_text[a['text']] for a in data['articles']}
                    ids = list(aliases.values())
                    # Two judges filter different candidates in the two-filter
                    # case: the pool policy must use their union.
                    index = 0 if body['model'] == 'judge-a' else -1
                    blocked = state['filtered'][index]
                    if data['query'] == state['first'] and blocked in ids:
                        raise urllib.error.HTTPError('private-url', 400, 'private-body', {},
                            io.BytesIO(b'{"error":{"code":"content_filter","message":"private-key"}}'))
                    return response(json.dumps({'grades': {
                        alias: 3 if real_id in state['question'].sources else 0 for alias, real_id in aliases.items()}}))
                self.opener.open.side_effect = answer
                providers = news.fake_providers()
                providers.max_filtered_candidate_share = cap
                providers.retrievers = {'bm25': mock.Mock(search=retrieve)}
                providers.baseline = 'bm25'
                providers.judges[:2] = [live.ChatJudge({**config(), 'model': 'judge-a', 'family': 'a'}),
                                        live.ChatJudge({**config(), 'model': 'judge-b', 'family': 'b'})]
                with mock.patch('scoring.score', side_effect=unavailable_scorer):
                    result = news.build(articles(), providers, 6, 42, salt=b'x' * 32)
                self.assertEqual(result.report['questions'], 6)
                self.assertEqual(result.report['question_types'], dict.fromkeys(news.KINDS, 1))
                filters = result.report['content_filter']
                self.assertEqual(filters['judged_candidates'], 8 * (6 + dropped))
                self.assertEqual(filters['filtered_candidates'], filtered_count)
                self.assertEqual(filters['filtered_candidate_share'], filtered_count / (8 * (6 + dropped)))
                self.assertEqual(filters['dropped_questions'], dropped if not filter_source else 0)
                if filter_source:
                    self.assertEqual(result.report['rejected']['answer_expected_none_found'], dropped)
                self.assertEqual(filters['max_filtered_candidate_share'], cap)
                queries = {**result.working['queries'], **result.held_out['queries']}
                self.assertEqual(state['first'] in queries.values(), not dropped)
                judgments = result.working['judgments'] + result.held_out['judgments']
                if not dropped:
                    qid = next(qid for qid, text in queries.items() if text == state['first'])
                    self.assertEqual(len([r for r in judgments if r['query'] == qid]), 8 - filtered_count)
                    self.assertFalse(any(r['query'] == qid and r['doc'] in state['filtered'] for r in judgments))
                self.assertTrue(all(all(type(v) is int for v in r['votes']) for r in judgments))
                self.assertGreater(providers.judges[0].summary()['rejected']['content_filter'], 0)
                news.validate_report(result.report)

    def test_persistent_content_filters_stop_at_attempt_or_spend_bounds(self):
        from test_news_set import articles, unavailable_scorer
        for adapter, cap in [('generator', 1000000), ('judge', 1000000), ('judge', 1000)]:
            with self.subTest(adapter=adapter, cap=cap):
                self.opener.open.reset_mock()
                def refuse(*args, **kwargs):
                    raise urllib.error.HTTPError('private-url', 400, 'private-body', {},
                        io.BytesIO(b'{"error":{"code":"content_filter"}}'))
                self.opener.open.side_effect = refuse
                providers = news.fake_providers()
                client = (live.ChatGenerator if adapter == 'generator' else live.ChatJudge)(
                    {**config(), 'max_output_tokens': cap, 'max_input_tokens': 10000000})
                if adapter == 'generator':
                    providers.generator = client
                else:
                    providers.judges[0] = client
                with mock.patch('scoring.score', side_effect=unavailable_scorer), \
                     self.assertRaises(news.BuildError) as caught:
                    news.build(articles(), providers, 6, 42, salt=b'x' * 32)
                if cap == 1000:
                    self.assertEqual(caught.exception.diagnostic['reason'], 'chat_cap_exhausted')
                    self.assertEqual(self.opener.open.call_count, 1)
                else:
                    self.assertEqual(caught.exception.diagnostic['reason'], 'min_questions_not_met')
                    self.assertTrue(all(v['attempts'] == v['attempt_budget'] == 10
                                        for v in caught.exception.diagnostic['question_targets'].values()))
                    reason = 'content_filter' if adapter == 'generator' else 'judge_content_filter'
                    self.assertEqual(caught.exception.diagnostic['rejected'][reason], 60)
                    self.assertEqual(self.opener.open.call_count, 60 if adapter == 'generator' else 900)

    def test_generator_rejects_count_evidence_and_extra_fields_before_returning_any_row(self):
        cases = [('{"questions":[]}', 'event', 1, 'invalid_count'),
                 ('{"questions":[{"text":"q","sources":["a1"]},'
                  '{"text":"q2","sources":["other"]}]}', 'event', 2, 'invalid_evidence'),
                 ('{"questions":[{"text":"q","sources":["a1"]}]}', 'multi_article', 1, 'invalid_evidence'),
                 ('{"questions":[{"text":"q","sources":["a1"],"kind":"event"}]}', 'event', 1, 'invalid_questions'),
                 ('{"questions":[{"text":"q","sources":["a1"]},{"text":"q2","sources":["a1"]}]}',
                  'event', 1, 'invalid_count')]
        for content, kind, count, reason in cases:
            with self.subTest(content=content):
                self.opener.open.return_value = response(content)
                with self.assertRaises(news.InvalidBatch) as caught:
                    live.ChatGenerator(config()).generate([news.Article('a', 'title', 'text', '2026-01-02')],
                                                         kind, count, random.Random(1))
                self.assertEqual(caught.exception.reason, reason)

    def test_malformed_retries_stop_at_caps_before_next_http_call(self):
        from test_news_set import articles
        for adapter in ('generator', 'judge'):
            with self.subTest(adapter=adapter):
                self.opener.open.reset_mock()
                self.opener.open.side_effect = lambda *args, **kwargs: response('{}')
                cls = live.ChatGenerator if adapter == 'generator' else live.ChatJudge
                client = cls({**config(), 'max_output_tokens': 1000})
                with self.assertRaises(live.AdapterError) as caught:
                    if adapter == 'generator':
                        news.generate(articles(), client, 6, 42)
                    else:
                        client.grade(news.Question('q', 'event', '2026-01-02', ('a',)),
                                     [news.Article('a', '', 't', '2026-01-02')])
                self.assertEqual(self.opener.open.call_count, 1)
                self.assertTrue(client.summary()['stopped'])
                self.assertEqual(caught.exception.diagnostic['reason'], 'chat_cap_exhausted')

    def test_large_judgment_pools_batch_without_losing_any_vote(self):
        candidates = [news.Article('a', '', 'x' * 1200, '2026-01-02'),
                      news.Article('b', '', 'y' * 1200, '2026-01-02')]
        self.opener.open.side_effect = [response('{"grades":{"a1":3}}'),
                                        response('{"grades":{"a1":true}}'),
                                        response('{"grades":{"a1":1,"a1":2}}'),
                                        response('{"grades":{"a1":1}}')]
        judge = live.ChatJudge({**config(), 'request_input_tokens': 2400})
        self.assertEqual(judge.grade(news.Question('q', 'event', '2026-01-02', ('a',)), candidates), {'a': 3, 'b': 1})
        self.assertEqual(judge.summary()['attempts'], 4)
        self.assertEqual(judge.summary()['rejected'], {'bad_values': 1, 'invalid_json': 1})
        batches = [json.loads(json.loads(call.args[0].data)['messages'][1]['content'])['articles']
                   for call in self.opener.open.call_args_list]
        self.assertEqual([[a['id'] for a in batch] for batch in batches], [['a1'], ['a1'], ['a1'], ['a1']])

    def test_oversized_articles_are_unavailable_votes_and_generation_resamples(self):
        q = news.Question('q', 'event', '2026-01-02', ('small',))
        small = news.Article('small', '', 'short', q.date)
        large = news.Article('large', '', 'x' * 200000, q.date)
        self.opener.open.side_effect = lambda *args, **kwargs: response('{"grades":{"a1":3}}')
        self.assertEqual(live.ChatJudge(config()).grade(q, [large, small]), {'large': None, 'small': 3})
        self.assertEqual(self.opener.open.call_count, 1)
        with self.assertRaises(news.InvalidBatch) as caught:
            live.ChatGenerator(config()).generate([large], 'entity', 1, random.Random(1))
        self.assertEqual(caught.exception.reason, 'input_bound')
        self.assertEqual(self.opener.open.call_count, 1)
        from jev_rerank.client import Result
        transport = mock.Mock(judge=lambda query, passages, deadline, cost_limit:
                              Result(scores=dict.fromkeys(passages, 1.0)))
        self.assertEqual(news.JevJudge(transport).grade(q, [large, small]), {'large': None, 'small': 3})

    def test_failed_jev_attempts_consume_budget_and_block_next_call(self):
        from jev_rerank.client import MAX_TOKENS, Result
        budget = live.embeddings.Budget(3 * MAX_TOKENS, 1)
        transport = mock.Mock()
        transport.judge.return_value = Result(input_tokens=3 * MAX_TOKENS, estimated_tokens=3 * MAX_TOKENS,
                                              reason='transport failure')
        client = live.CappedJev(transport, budget)
        self.assertEqual(client.judge('q', {'a': 'text'}, 100, 1).reason, 'transport failure')
        with self.assertRaises(live.AdapterError):
            client.judge('q', {'a': 'text'}, 100, 1)
        self.assertEqual(transport.judge.call_count, 1)
        self.assertAlmostEqual(budget.summary()['cost_upper_bound_usd'], 3 * MAX_TOKENS * .042 / 1000000)

    def test_offline_config_rejects_unsafe_caps_and_unknown_fields_without_http(self):
        for change in [{'max_filtered_candidate_share': v} for v in (0, 1.1, float('nan'), True, '0.1')] + [
                       *({'min_questions': v} for v in (0, 1, True, None, '1000')),
                       *({name: value} for name in ('question_targets', 'attempt_budgets')
                         for value in ([], {'unknown': 1}, {'entity': -1}, {'entity': True}, {'entity': 1.5})),
                       {'build_max_usd': 1}, {'generator': {**config(), 'max_retries': 99}},
                       {'articles': {'filter': {'field': 'credit', 'values': []}}},
                       {'retrieval': {**live.example_config()['retrieval'], 'key_env': 'literal-secret'}},
                       {'jev': {**live.example_config()['jev'], 'key_env': 123}},
                       {'retrieval': {**live.example_config()['retrieval'], 'dense_weight': float('nan')}}]:
            with self.subTest(change=change), self.assertRaises(live.AdapterError):
                live.validate_config({**live.example_config(), **change})
        cfg = live.validate_config({**live.example_config(), 'question_targets': {'no_answer': 0},
                                    'attempt_budgets': {'no_answer': 0, 'entity': 2}, 'min_questions': 1000})
        estimated = live.estimate(cfg, 1200)
        self.assertEqual(estimated['questions'], 1000)
        self.assertEqual(estimated['generation_max_attempts'], 4002)
        self.assertEqual(estimated['min_questions'], 1000)
        self.assertEqual(live.estimate(live.example_config(), 1500)['min_questions'], 1000)
        with self.assertRaises(news.BuildError):
            live.estimate(cfg, 1194)
        self.opener.open.assert_not_called()

    def test_cli_signals_preserve_paid_fake_transport_usage(self):
        import signal
        for stopped_by in (signal.SIGTERM, signal.SIGINT):
            with self.subTest(signal=stopped_by), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                (root / 'adapters.py').write_text('''import io,json,os,signal,urllib.request
import news_set as news
import news_providers as live
class FakeHTTP:
    calls=0
    def open(self,*args,**kwargs):
        self.calls+=1
        if self.calls==2:
            os.kill(os.getpid(),int(os.environ['TEST_STOP_SIGNAL']))
        return io.BytesIO(json.dumps({'usage':{'prompt_tokens':100,'completion_tokens':20},
          'choices':[{'finish_reason':'stop','message':{'content':'{"questions":[]}'}}]}).encode())
def providers():
    from test_news_providers import config
    result=news.Providers(news.FakeGenerator(),{s:news.FakeRetriever() for s in news.SYSTEMS},
                          [news.FakeJudge(str(i)) for i in range(3)],'hybrid',synthetic=True)
    urllib.request.build_opener=lambda *args:FakeHTTP()
    result.generator=live.ChatGenerator(config())
    def usage():
        chat=result.generator.summary()
        return {'generator':chat,'judge_1':live.Chat(config()).summary(),'judge_2':live.Chat(config()).summary(),
                'jev':live.embeddings.Budget(10000,1).summary(),'retrieval':live.embeddings.Budget(10000,1).summary(),
                'totals':{'confirmed_cost_usd':chat['confirmed_cost_usd'],
                          'cost_upper_bound_usd':chat['cost_upper_bound_usd'],
                          'generation_judging_max_usd':30,'retrieval_max_usd':1}}
    result.usage=usage
    return result
''')
                environment = {**os.environ, 'NEWS_ENDPOINT': 'https://example.invalid', 'NEWS_KEY': 'fake-key',
                    'QUIVR_NEWS_WORKING_RECIPIENTS': 'working', 'QUIVR_NEWS_HOLDOUT_RECIPIENTS': 'holdout',
                    'QUIVR_NEWS_REVIEW_RECIPIENTS': 'review', 'TEST_STOP_SIGNAL': str(int(stopped_by))}
                # Load the operator factory through the real CLI; --fake only supplies articles.
                command = [sys.executable, '-c',
                    "import sys,news_set as n;n.fake_providers=lambda:n.load_providers(sys.argv[1]);"
                    "raise SystemExit(n.main(sys.argv[2:]))", str(root / 'adapters.py'),
                    '--fake', '--questions', '6', '--storage-dir', str(root / 'storage'),
                    '--report', str(root / 'quality.json'), '--usage-report', str(root / 'usage.json')]
                completed = subprocess.run(command, cwd=pathlib.Path(news.__file__).parent, env=environment,
                                           capture_output=True, text=True, timeout=10)
                self.assertEqual(completed.returncode, 128 + stopped_by, completed.stdout + completed.stderr)
                usage = json.loads((root / 'usage.json').read_text())
                news.validate_usage(usage)
                self.assertGreaterEqual(usage['generator']['confirmed_cost_usd'], .00014)
                self.assertGreaterEqual(usage['generator']['confirmed_input_tokens'], 100)
                self.assertFalse((root / 'quality.json').exists())
                self.assertNotIn('fake-key', completed.stdout + completed.stderr)

    def test_cli_failure_retains_spend_from_a_configured_factory(self):
        # Owner regression: loading config and articles in one assignment lost the
        # initialized factory, so --usage-report omitted paid failed attempts.
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / 'input').mkdir()
            (root / 'input/articles.json').write_text(json.dumps([
                {'id': 'a', 'title': 'Port', 'text': 'A ferry opens Monday.', 'published_at': '2026-01-02'},
                {'id': 'b', 'title': 'Mountain', 'text': 'The road closes Tuesday.', 'published_at': '2026-01-02'}]))
            (root / 'config.json').write_text(json.dumps({**live.example_config(), 'attempt_budgets': dict.fromkeys(news.KINDS, 2)}))
            (root / 'adapters.py').write_text('''import io,json,urllib.request
import news_providers as live
class FakeHTTP:
    def open(self,*args,**kwargs):
        return io.BytesIO(json.dumps({'usage':{'prompt_tokens':100,'completion_tokens':20},
          'choices':[{'finish_reason':'stop','message':{'content':'{"questions":[]}'}}]}).encode())
def providers():
    urllib.request.build_opener=lambda *args:FakeHTTP()
    return live.providers()
''')
            environment = {**os.environ, 'QUIVR_NEWS_CONFIG': str(root / 'config.json'),
                           'NEWS_ENDPOINT': 'https://example.invalid/openai/v1', 'NEWS_KEY': 'fake-key',
                           'AZURE_FOUNDRY_ENDPOINT': 'https://example.invalid', 'AZURE_FOUNDRY_KEY': 'fake-key',
                           'TYPESAFE_API_KEY': 'fake-key', 'QUIVR_NEWS_WORKING_RECIPIENTS': 'working',
                           'QUIVR_NEWS_HOLDOUT_RECIPIENTS': 'holdout', 'QUIVR_NEWS_REVIEW_RECIPIENTS': 'review',
                           'CI': '', 'GITHUB_ACTIONS': ''}
            completed = subprocess.run([sys.executable, str(pathlib.Path(news.__file__)),
                '--articles', str(root / 'input'), '--providers', str(root / 'adapters.py'),
                '--storage-dir', str(root / 'storage'), '--report', str(root / 'quality.json'),
                '--usage-report', str(root / 'usage.json')], env=environment, capture_output=True, text=True, timeout=10)
            self.assertEqual(completed.returncode, 2, completed.stdout + completed.stderr)
            usage = json.loads((root / 'usage.json').read_text())
            self.assertEqual(usage['generator']['attempts'], 12)
            self.assertEqual(usage['generator']['rejected'], {'invalid_count': 12})
            self.assertEqual(json.loads(completed.stdout.split('diagnostic: ', 1)[1]),
                             {'phase': 'generation', 'reason': 'min_questions_not_met', 'accepted': 0, 'min_questions': 1000,
                              'question_targets': {kind: {'target': 250, 'accepted': 0, 'shortfall': 250,
                                  'attempts': 2, 'attempt_budget': 2} for kind in news.KINDS},
                              'rejected': {'invalid_count': 12}})
            self.assertEqual(usage['generator']['confirmed_cost_usd'], .0024)
            self.assertFalse((root / 'quality.json').exists())
            self.assertNotIn('fake-key', completed.stdout + completed.stderr)
            (root / 'alias').symlink_to(root, target_is_directory=True)
            alias = subprocess.run([sys.executable, str(pathlib.Path(news.__file__)),
                '--articles', str(root / 'input'), '--providers', str(root / 'adapters.py'),
                '--storage-dir', str(root / 'storage'), '--report', str(root / 'quality.json'),
                '--usage-report', str(root / 'alias/quality.json')], env=environment,
                capture_output=True, text=True, timeout=10)
            self.assertEqual(alias.returncode, 2)
            self.assertIn('usage report must name a separate new file', alias.stderr)


@unittest.skipUnless(importlib.util.find_spec('numpy'), 'requires numpy for local retrieval')
class Retrieval(unittest.TestCase):
    def test_factory_retrieves_and_reuses_indexes_and_query_embeddings(self):
        cfg = live.example_config()
        cfg['generator'] = config()
        cfg['judges'] = [config(), {**config(), 'family': 'family-b'}]
        corpus = [news.Article('a', 'Port', 'ferry navire', '2026-01-02'),
                  news.Article('b', 'Montagne', 'neige sommet', '2026-01-02')]
        e5, hosted = mock.Mock(), mock.Mock()
        hosted.reuse = live.Reuse()
        e5.embed.side_effect = lambda texts, mode: [[1., 0.] if 'ferry' in t else [0., 1.] for t in texts]
        hosted.embed.side_effect = lambda model, texts, mode, **kwargs: [[1., 0.] if 'ferry' in t else [0., 1.] for t in texts]
        with mock.patch.dict(os.environ, NEWS_ENDPOINT='https://example.invalid', NEWS_KEY='key',
                             TYPESAFE_API_KEY='key', AZURE_FOUNDRY_ENDPOINT='https://example.invalid', AZURE_FOUNDRY_KEY='key'), \
             mock.patch('direct_bakeoff.E5', return_value=e5), mock.patch('news_providers.CachedHosted', return_value=hosted), \
             mock.patch('search_trial.BM25', wraps=__import__('search_trial').BM25) as bm25:
            providers = live.providers(cfg)
            q = news.Question('ferry', 'event', '2026-01-02', ('a',))
            for _ in range(2):
                for retriever in providers.retrievers.values():
                    self.assertEqual(retriever.search(q, corpus, 10)[0], 'a')
            self.assertEqual(bm25.call_count, 1)
            self.assertEqual(e5.embed.call_count, 2)  # documents + one cached query
            self.assertEqual(hosted.embed.call_count, 2)
            with self.assertRaises(live.AdapterError):
                providers.retrievers['bm25'].search(q, list(reversed(corpus)), 10)
        self.assertFalse(providers.synthetic)
        news.validate_usage(providers.usage())
        with self.assertRaises(news.BuildError):
            news.validate_usage({**providers.usage(), 'article_text': 'private-text'})

    def test_pool_never_judges_more_than_ten_per_system(self):
        corpus = [news.Article(str(i), '', '', '2026-01-02') for i in range(50)]
        qs = [news.Question('q', 'event', '2026-01-02', ('0',))]
        rows, ranks = news.judge_pool(qs, corpus, {s: news.FakeRetriever() for s in news.SYSTEMS},
                                     [news.FakeJudge(str(i)) for i in range(3)])
        self.assertEqual(len(rows), 10)
        self.assertTrue(all(len(ids) == 10 for ids in ranks[0].values()))


if __name__ == '__main__':
    unittest.main()
