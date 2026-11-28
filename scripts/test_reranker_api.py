"""Exercise the deployed reranking wire contract with fake GPU inference."""
import json
import unittest
from unittest.mock import AsyncMock

from deploy.modal import reranker_api as api

AUTH = (b'authorization', b'Bearer test-token')
VALID = {'query': 'What is the capital of France?', 'texts': ['Berlin', 'Paris']}


class RerankerAPITest(unittest.IsolatedAsyncioTestCase):
    async def request(self, body=VALID, *, headers=(AUTH,), method='POST', path='/rerank',
                      chunks=None, margins=(-2.0, 2.0), failure=None):
        score = AsyncMock(return_value=margins, side_effect=failure)
        app = api.create_app('test-token', score, model='test/model', revision='test-revision')
        if chunks is None:
            chunks = [json.dumps(body).encode()]
        receive = AsyncMock(side_effect=[
            {'type': 'http.request', 'body': chunk, 'more_body': index < len(chunks) - 1}
            for index, chunk in enumerate(chunks)
        ])
        send = AsyncMock()
        await app({'type': 'http', 'headers': headers, 'method': method, 'path': path}, receive, send)
        start, payload = (call.args[0] for call in send.await_args_list)
        return start, json.loads(payload['body']), score, receive

    async def test_authentication_and_health_skip_inference_and_body(self):
        for path, method in [('/health', 'GET'), ('/rerank', 'POST')]:
            for headers in [(), ((b'authorization', b'Bearer wrong'),), (AUTH, AUTH)]:
                with self.subTest(path=path, headers=headers):
                    start, _, score, receive = await self.request(headers=headers, path=path, method=method)
                    self.assertEqual(start['status'], 401)
                    self.assertIn((b'www-authenticate', b'Bearer'), start['headers'])
                    receive.assert_not_awaited()
                    score.assert_not_awaited()
        start, body, score, receive = await self.request(path='/health', method='GET')
        self.assertEqual(start['status'], 200)
        self.assertEqual(body, {'status': 'ok', 'model': 'test/model', 'revision': 'test-revision'})
        receive.assert_not_awaited()
        score.assert_not_awaited()
        for invalid_key in ('', 'two words', 'line\nbreak', 'é', '\x00'):
            with self.assertRaises(ValueError):
                api.create_app(invalid_key, score, model='test/model', revision='test-revision')

    async def test_invalid_requests_never_dispatch(self):
        for patch, param in [
            ({'query': ' '}, 'query'), ({'query': 'x' * 8193}, 'query'),
            ({'texts': 'Paris'}, 'texts'), ({'texts': []}, 'texts'),
            ({'texts': ['x'] * 129}, 'texts'), ({'texts': [None]}, 'texts'),
            ({'texts': ['é' * 4097]}, 'texts'), ({'texts': ['\x00']}, 'texts'),
            ({'texts': ['\ud800']}, 'texts'), ({'instruction': 'x' * 2049}, 'instruction'),
            ({'raw_score': 1}, 'raw_score'), ({'raw_scores': 'true'}, 'raw_scores'),
            ({'raw_score': True, 'raw_scores': False}, 'raw_score'),
            ({'truncate': []}, 'truncate'), ({'return_text': None}, 'return_text'),
            ({'truncation_direction': 'center'}, 'truncation_direction'),
        ]:
            with self.subTest(param=param, patch=repr(patch)[:100]):
                start, body, score, _ = await self.request({**VALID, **patch})
                self.assertEqual(start['status'], 400)
                self.assertEqual(body['error']['param'], param)
                score.assert_not_awaited()
        for chunks in [
            [b'[]'], [b'{"query":'], [b'{"query":"x","query":"y","texts":["z"]}'],
            [b' ' * (4 * 1024 * 1024), b' ' * (4 * 1024 * 1024), b'x'],
        ]:
            start, _, score, _ = await self.request(chunks=chunks)
            self.assertEqual(start['status'], 400)
            score.assert_not_awaited()

    async def test_raw_and_probability_scores_keep_indices_and_rank_order(self):
        request = {**VALID, 'texts': ['first', 'second', 'third', 'fourth', 'fifth']}
        margins = (1000.0, 1001.0, -1000.0, 0.0, 2.0)
        for flags in ({'raw_score': True}, {'raw_scores': True}, {'raw_score': False}, {}):
            with self.subTest(flags=flags):
                start, ranks, score, _ = await self.request(
                    {**request, **flags, 'return_text': True, 'instruction': 'Find an exact match',
                     'truncate': True, 'truncation_direction': 'Left'}, margins=margins,
                )
                self.assertEqual(start['status'], 200)
                self.assertEqual([row['index'] for row in ranks], [1, 0, 4, 3, 2])
                self.assertEqual([row['text'] for row in ranks], ['second', 'first', 'fifth', 'fourth', 'third'])
                self.assertEqual([row['raw_score'] for row in ranks], [1001, 1000, 2, 0, -1000])
                scores = [row['score'] for row in ranks]
                if any(flags.values()):
                    self.assertEqual(scores, [1001, 1000, 2, 0, -1000])
                else:
                    self.assertEqual(scores[:2], [1, 1])
                    self.assertAlmostEqual(scores[2], 0.880797, places=6)
                    self.assertEqual(scores[3:], [0.5, 0])
                score.assert_awaited_once()
                scored = score.await_args.args[0]
                self.assertEqual((scored.instruction, scored.truncate, scored.truncation_direction),
                                 ('Find an exact match', True, 'left'))

    async def test_model_failures_are_sanitized(self):
        for margins, failure, status, code in [
            (None, None, 400, 'input_too_long'),
            ((1.0,), None, 503, 'reranking_failed'),
            ((0.0, float('nan')), None, 503, 'reranking_failed'),
            (None, RuntimeError('private model diagnostic'), 503, 'reranking_failed'),
        ]:
            start, body, _, _ = await self.request(margins=margins, failure=failure)
            self.assertEqual(start['status'], status)
            self.assertEqual(body['error']['code'], code)
            self.assertNotIn('private model diagnostic', json.dumps(body))


class ScoringTest(unittest.TestCase):
    def test_instruction_formats_for_both_model_kinds(self):
        self.assertEqual(api.qwen_pair('question', 'passage', 'Match the topic'),
                         '<Instruct>: Match the topic\n<Query>: question\n<Document>: passage')
        self.assertEqual(api.qwen_pair('question', 'passage', None),
                         '<Instruct>: Given a web search query, retrieve relevant passages that answer the query'
                         '\n<Query>: question\n<Document>: passage')
        self.assertEqual(api.classifier_query('question', 'Match the topic'), 'Match the topic\nquestion')
        self.assertEqual(api.classifier_query('question', None), 'question')

    def test_class_logits_yield_unsaturated_relevance_margins(self):
        self.assertEqual(api.logit_margin([-50.0, 30.0]), 80.0)
        self.assertEqual(api.logit_margin([30.0, -50.0]), -80.0)
        self.assertEqual(api.logit_margin([-30.0]), -30.0)
        with self.assertRaises(RuntimeError):
            api.logit_margin([1.0, 2.0, 3.0])


if __name__ == '__main__':
    unittest.main()
