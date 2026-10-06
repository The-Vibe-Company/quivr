"""Authenticated OpenAI wire contract with fake inference; never launch Modal."""
import asyncio
import importlib.util
import json
import pathlib
import unittest
from unittest.mock import AsyncMock

PATH = pathlib.Path(__file__).resolve().parents[1] / 'deploy/modal/embedding_api.py'
spec = importlib.util.spec_from_file_location('embedding_api', PATH)
api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(api)


class EmbeddingAPITest(unittest.IsolatedAsyncioTestCase):
    async def request(self, body=None, *, token='Bearer placeholder-token', method='POST',
                      path='/v1/embeddings', raw=None, vectors=None, error=None):
        inference = AsyncMock(return_value=vectors if vectors is not None else [[1.0] + [0.0] * 767],
                              side_effect=error)
        app = api.create_app('placeholder-token', inference)
        messages = []
        raw = json.dumps(body).encode() if raw is None else raw
        receive = AsyncMock(return_value={'type': 'http.request', 'body': raw, 'more_body': False})
        async def send(message):
            messages.append(message)
        headers = [] if token is None else [(b'authorization', token.encode())]
        await app({'type': 'http', 'method': method, 'path': path, 'headers': headers}, receive, send)
        return messages[0]['status'], json.loads(messages[1]['body']), inference, receive

    async def test_authentication_precedes_payloads_health_and_gpu_dispatch(self):
        for path in ('/v1/embeddings', '/health'):
            for token in (None, '', 'Bearer wrong', 'Basic placeholder-token', 'Bearer '):
                with self.subTest(path=path, token=token):
                    status, body, inference, receive = await self.request(path=path, token=token)
                    self.assertEqual(status, 401)
                    self.assertEqual(body['error']['code'], 'unauthorized')
                    inference.assert_not_called()
                    receive.assert_not_called()
        status, body, inference, _ = await self.request(path='/health', method='GET')
        self.assertEqual((status, body['status'], body['model']), (200, 'ok', 'google/embeddinggemma-2'))
        inference.assert_not_called()
        for token in ('', ' ', 'has\nnewline'):
            with self.assertRaises(ValueError):
                api.create_app(token, AsyncMock())

    async def test_requests_validate_inputs_and_return_ordered_float_vectors(self):
        valid = {'model': 'google/embeddinggemma-2', 'input': ['title: none | text: A document'],
                 'dimensions': 768, 'encoding_format': 'float'}
        for patch, param in [({'model': 'other'}, 'model'), ({'dimensions': True}, 'dimensions'),
                             ({'dimensions': 256}, 'dimensions'), ({'input': []}, 'input'),
                             ({'input': [1]}, 'input'), ({'input': ['']}, 'input'),
                             ({'input': ['x'] * 33}, 'input'),
                             ({'input': ['é' * 1025]}, 'input'),
                             ({'input': ['\x00']}, 'input'), ({'input': ['\ud800']}, 'input'),
                             ({'encoding_format': 'base64'}, 'encoding_format')]:
            with self.subTest(patch=patch):
                status, body, inference, _ = await self.request({**valid, **patch})
                self.assertEqual(status, 400)
                self.assertEqual(body['error']['param'], param)
                inference.assert_not_called()
        for raw in (b'[]', b'null', b'{', b'{"model":"other","model":"google/embeddinggemma-2","input":"a"}',
                    b'x' * (512 * 1024 + 1)):
            status, _, inference, _ = await self.request(raw=raw)
            self.assertEqual(status, 400)
            inference.assert_not_called()
        vectors = [[1.0] + [0.0] * 767, [0.0, 1.0] + [0.0] * 766]
        status, body, inference, _ = await self.request({**valid, 'input': ['first', 'second']}, vectors=vectors)
        self.assertEqual(status, 200)
        self.assertEqual(body, {'object': 'list', 'model': 'google/embeddinggemma-2',
                               'data': [{'object': 'embedding', 'index': 0, 'embedding': vectors[0]},
                                        {'object': 'embedding', 'index': 1, 'embedding': vectors[1]}]})
        inference.assert_awaited_once_with(['first', 'second'])
        status, _, inference, _ = await self.request({**valid, 'input': 'a string'})
        self.assertEqual(status, 200)
        inference.assert_awaited_once_with(['a string'])
        # Go's encoding/json escapes HTML characters. A valid full provider
        # batch must fit the wire limit even when each byte expands sixfold.
        texts = ['<' * 2048] * 32
        raw = json.dumps({**valid, 'input': texts}).replace('<', '\\u003c').encode()
        status, _, inference, _ = await self.request(raw=raw, vectors=[vectors[0]] * 32)
        self.assertEqual(status, 200)
        inference.assert_awaited_once_with(texts)

    async def test_inference_failures_are_sanitized_and_bad_vectors_never_escape(self):
        body = {'model': 'google/embeddinggemma-2', 'input': 'document'}
        for vectors, error in [([[float('nan')] * 768], None), ([[1.0]], None), ([], None),
                               (None, RuntimeError('private diagnostic'))]:
            status, response, _, _ = await self.request(body, vectors=vectors, error=error)
            self.assertEqual(status, 503)
            self.assertEqual(response['error']['code'], 'encoding_failed')
            self.assertNotIn('private diagnostic', json.dumps(response))


if __name__ == '__main__':
    unittest.main()
