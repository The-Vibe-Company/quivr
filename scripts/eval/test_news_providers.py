"""Owner tests for live adapter transport, spend and prepared retrieval contracts."""
import contextlib
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

    def test_generator_and_judge_use_json_contract_and_keep_evidence_out_of_judging(self):
        article = news.Article('a', 'Un port', 'Le ferry ouvre lundi.', '2026-01-02',
                               updated_at='2026-01-02T12:00:00Z', source='private-source', credit='private-credit',
                               story_id='private-story', previous_versions=(
                                   {'id': 'earlier', 'text': 'Ouverture prévue', 'updated_at': '2026-01-02T09:00:00Z'},))
        self.opener.open.side_effect = [response(json.dumps({'questions': [
            {'text': 'Quand part le navire ?', 'sources': ['a']}]})), response('{"grades":{"a":3}}')]
        generator = live.ChatGenerator(config())
        q = generator.generate([article], 'event', 1, random.Random(1))[0]
        self.assertEqual(q, news.Question('Quand part le navire ?', 'event', '2026-01-02', ('a',)))
        self.assertEqual(live.ChatJudge({**config(), 'auth_header': 'bearer'}).grade(q, [article]), {'a': 3})
        requests = [c.args[0] for c in self.opener.open.call_args_list]
        self.assertEqual(requests[0].full_url, 'https://example.invalid/openai/v1/chat/completions')
        self.assertEqual(requests[0].get_header('Api-key'), 'private-key')
        self.assertEqual(requests[1].get_header('Authorization'), 'Bearer private-key')
        self.assertIsNone(requests[1].get_header('Api-key'))
        for request in requests:
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
                    if status:
                        expected['http_status'] = status
                    if code:
                        expected['provider_code'] = code
                    self.assertEqual(json.loads(output.getvalue().split('diagnostic: ', 1)[1]), expected)
                    for private in ('private-body', 'private-key', 'private-text', 'private-query', 'private-url'):
                        self.assertNotIn(private, output.getvalue() + stderr.getvalue())
                    self.assertEqual(self.opener.open.call_count, 0 if phase == 'retrieval' else 1)

    def test_retries_charge_unknown_attempts_and_caps_block_before_http(self):
        client = live.ChatJudge(config())
        self.opener.open.side_effect = [urllib.error.HTTPError('private-url', 429, 'private-body',
                                       {'Retry-After': '999999'}, io.BytesIO(b'private-body')), response('{"grades":{"a":2}}')]
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
        cases = ['{"grades":{"a":true}}', '{"grades":{"a":1,"a":2}}',
                 '{"grades":{"a":NaN}}', '{"grades":{"other":0}}',
                 '```json\n{"grades":{"a":3}}\n```', '{"grades":{"a":0},"secret":"private-text"}']
        for content in cases:
            with self.subTest(content=content):
                self.opener.open.reset_mock()
                self.opener.open.side_effect = lambda *args, **kwargs: response(content)
                with self.assertRaises(live.AdapterError) as caught:
                    live.ChatJudge(config()).grade(news.Question('private-query', 'event', '2026-01-02', ('a',)),
                                                  [news.Article('a', '', 'private-text', '2026-01-02')])
                self.assertNotIn('private-query', str(caught.exception))
                self.assertNotIn('private-text', str(caught.exception))
                self.assertIsNone(caught.exception.__cause__)
                self.assertEqual(self.opener.open.call_count, 3)
                self.assertEqual(caught.exception.diagnostic['reason'], 'judge_attempts_exhausted')
        self.opener.open.side_effect = None
        self.opener.open.return_value = response('{"grades":{"a":3}}', completion=1001)
        client = live.ChatJudge(config())
        with self.assertRaises(live.AdapterError):
            client.grade(news.Question('q', 'event', '2026-01-02', ('a',)), [news.Article('a', '', 't', '2026-01-02')])
        self.assertTrue(client.summary()['stopped'])

    def test_unknown_usage_keeps_a_reservation_covering_escaped_request_bytes(self):
        self.opener.open.return_value = io.BytesIO(json.dumps({'choices': [
            {'finish_reason': 'stop', 'message': {'content': '{"grades":{"a":3}}'}}]}).encode())
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
        ]
        for content, reason in cases:
            with self.subTest(reason=reason, content=content):
                self.opener.open.reset_mock()
                def answer(request, **kwargs):
                    data = json.loads(json.loads(request.data)['messages'][1]['content'])
                    if self.opener.open.call_count == 1:
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
                                 120 if reason == 'content_filter' else 140)
                if reason == 'content_filter':
                    samples = [json.loads(json.loads(call.args[0].data)['messages'][1]['content'])['articles']
                               for call in self.opener.open.call_args_list[:2]]
                    self.assertNotEqual(samples[0][0]['id'], samples[1][0]['id'])
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
                def retrieve(question, corpus, limit):
                    state['question'] = question
                    if state['first'] is None:
                        state['first'] = question.text
                        ids = sorted(a.id for a in corpus if a.id not in question.sources)
                        state['filtered'] = (list(question.sources) if filter_source else ids[:filtered_count])
                    return [a.id for a in corpus]
                def answer(request, **kwargs):
                    body = json.loads(request.data)
                    data = json.loads(body['messages'][1]['content'])
                    ids = [a['id'] for a in data['articles']]
                    # Two judges filter different candidates in the two-filter
                    # case: the pool policy must use their union.
                    index = 0 if body['model'] == 'judge-a' else -1
                    blocked = state['filtered'][index]
                    if data['query'] == state['first'] and blocked in ids:
                        raise urllib.error.HTTPError('private-url', 400, 'private-body', {},
                            io.BytesIO(b'{"error":{"code":"content_filter","message":"private-key"}}'))
                    return response(json.dumps({'grades': {
                        d: 3 if d in state['question'].sources else 0 for d in ids}}))
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
                self.assertEqual(filters['dropped_questions'], dropped)
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
                    {**config(), 'max_output_tokens': cap})
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
                    self.assertEqual(caught.exception.diagnostic['reason'], 'generation_attempts_exhausted')
                    self.assertEqual(caught.exception.diagnostic['attempts'], 10)
                    reason = 'content_filter' if adapter == 'generator' else 'judge_content_filter'
                    self.assertEqual(caught.exception.diagnostic['rejected'][reason], 10)
                    self.assertEqual(self.opener.open.call_count, 10 if adapter == 'generator' else 150)

    def test_generator_rejects_count_evidence_and_extra_fields_before_returning_any_row(self):
        cases = [('{"questions":[]}', 'event', 1, 'invalid_count'),
                 ('{"questions":[{"text":"q","sources":["a"]},'
                  '{"text":"q2","sources":["other"]}]}', 'event', 2, 'invalid_evidence'),
                 ('{"questions":[{"text":"q","sources":["a"]}]}', 'multi_article', 1, 'invalid_evidence'),
                 ('{"questions":[{"text":"q","sources":["a"],"kind":"event"}]}', 'event', 1, 'invalid_questions'),
                 ('{"questions":[{"text":"q","sources":["a"]},{"text":"q2","sources":["a"]}]}',
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
        self.opener.open.side_effect = [response('{"grades":{"a":3}}'),
                                        response('{"grades":{"b":true}}'),
                                        response('{"grades":{"b":1,"b":2}}'),
                                        response('{"grades":{"b":1}}')]
        judge = live.ChatJudge({**config(), 'request_input_tokens': 2400})
        self.assertEqual(judge.grade(news.Question('q', 'event', '2026-01-02', ('a',)), candidates), {'a': 3, 'b': 1})
        self.assertEqual(judge.summary()['attempts'], 4)
        self.assertEqual(judge.summary()['rejected'], {'invalid_grades': 1, 'invalid_json': 1})
        batches = [json.loads(json.loads(call.args[0].data)['messages'][1]['content'])['articles']
                   for call in self.opener.open.call_args_list]
        self.assertEqual([[a['id'] for a in batch] for batch in batches], [['a'], ['b'], ['b'], ['b']])

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
                       {'build_max_usd': 1}, {'generator': {**config(), 'max_retries': 99}},
                       {'articles': {'filter': {'field': 'credit', 'values': []}}},
                       {'retrieval': {**live.example_config()['retrieval'], 'key_env': 'literal-secret'}},
                       {'jev': {**live.example_config()['jev'], 'key_env': 123}},
                       {'retrieval': {**live.example_config()['retrieval'], 'dense_weight': float('nan')}}]:
            with self.subTest(change=change), self.assertRaises(live.AdapterError):
                live.validate_config({**live.example_config(), **change})
        self.opener.open.assert_not_called()

    def test_cli_failure_retains_spend_from_a_configured_factory(self):
        # Owner regression: loading config and articles in one assignment lost the
        # initialized factory, so --usage-report omitted paid failed attempts.
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / 'input').mkdir()
            (root / 'input/articles.json').write_text(json.dumps([
                {'id': 'a', 'title': 'Port', 'text': 'A ferry opens Monday.', 'published_at': '2026-01-02'},
                {'id': 'b', 'title': 'Mountain', 'text': 'The road closes Tuesday.', 'published_at': '2026-01-02'}]))
            (root / 'config.json').write_text(json.dumps(live.example_config()))
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
            self.assertEqual(usage['generator']['attempts'], 1250)
            self.assertEqual(usage['generator']['rejected'], {'invalid_count': 1250})
            self.assertEqual(json.loads(completed.stdout.split('diagnostic: ', 1)[1]),
                             {'phase': 'generation', 'reason': 'generation_attempts_exhausted',
                              'kind': 'entity', 'attempts': 1250, 'accepted': 0, 'target': 250,
                              'rejected': {'invalid_count': 1250}})
            self.assertEqual(usage['generator']['confirmed_cost_usd'], .25)
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
        e5.embed.side_effect = lambda texts, mode: [[1., 0.] if 'ferry' in t else [0., 1.] for t in texts]
        hosted.embed.side_effect = lambda model, texts, mode, **kwargs: [[1., 0.] if 'ferry' in t else [0., 1.] for t in texts]
        with mock.patch.dict(os.environ, NEWS_ENDPOINT='https://example.invalid', NEWS_KEY='key',
                             TYPESAFE_API_KEY='key', AZURE_FOUNDRY_ENDPOINT='https://example.invalid', AZURE_FOUNDRY_KEY='key'), \
             mock.patch('direct_bakeoff.E5', return_value=e5), mock.patch('direct_bakeoff.Hosted', return_value=hosted), \
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
