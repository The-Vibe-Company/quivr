"""Owner-boundary tests for the offline CPU query encoder service."""
import http.client
import json
import queue
import threading
import unittest

from deploy.cpu import text_encoder


VECTOR = [1.0] + [0.0] * (text_encoder.DIMENSIONS - 1)


class Array:
    def __init__(self, rows):
        self._rows = rows
        self.shape = (len(rows), text_encoder.DIMENSIONS)

    def tolist(self):
        return self._rows


class FakeModel:
    def __init__(self, *, block=False):
        self.block = block
        self.started = threading.Event()
        self.release = threading.Event()
        self.calls = []

    def encode(self, texts, **kwargs):
        self.calls.append((list(texts), kwargs))
        self.started.set()
        if self.block:
            self.release.wait(2)
        return Array([VECTOR[:] for _ in texts])


class QueryEncoderHTTPTest(unittest.TestCase):
    def setUp(self):
        self.model = FakeModel()
        self.server = text_encoder.create_server(self.model, port=0)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(2)

    def request(self, body=None, *, method='POST', path='/v1/embeddings', headers=None,
                raw=None):
        connection = http.client.HTTPConnection('127.0.0.1', self.server.server_port, timeout=2)
        if raw is None:
            raw = json.dumps(body).encode()
        connection.request(method, path, body=raw, headers={
            'Content-Type': 'application/json', **(headers or {})})
        response = connection.getresponse()
        payload = response.read()
        connection.close()
        return response.status, json.loads(payload)

    def test_health_and_prefixed_query_and_document_text(self):
        status, health = self.request(method='GET', path='/health')
        self.assertEqual(status, 200)
        self.assertEqual(health, {
            'status': 'ok',
            'model': 'google/embeddinggemma-2',
            'model_revision': '914f7f89142e33e7',
            'source_revision': '914f7f89142e33e77833254d9c9b90c3cef7303b',
            'dimensions': 768,
        })

        texts = ['task: search result | query: a future question',
                 'title: none | text: a future document']
        status, response = self.request({
            'model': 'google/embeddinggemma-2', 'input': texts, 'dimensions': 768})
        self.assertEqual(status, 200)
        self.assertEqual(len(response['data']), 2)
        self.assertEqual(self.model.calls[0][0], texts)

    def test_malformed_request_and_output_are_sanitized(self):
        valid = {'model': 'google/embeddinggemma-2', 'input': 'question'}
        for raw in (b'[]', b'{', b'{"model":"other","input":"question"}',
                    b'{"model":"google/embeddinggemma-2","model":"other","input":"question"}'):
            with self.subTest(raw=raw):
                status, response = self.request(raw=raw)
                self.assertEqual(status, 400)
                self.assertEqual(response['error']['code'], 'invalid_request')
        self.assertEqual(len(self.model.calls), 0)

        for timeout in ('0', '10001', '999999999999999999999999'):
            with self.subTest(timeout=timeout):
                status, response = self.request(valid,
                                                headers={'X-Quivr-Timeout-Ms': timeout})
                self.assertEqual(status, 400)
                self.assertEqual(response['error']['code'], 'invalid_request')
        self.assertEqual(len(self.model.calls), 0)

        self.model.encode = lambda texts, **kwargs: Array([[float('nan')] * text_encoder.DIMENSIONS])
        status, response = self.request(valid)
        self.assertEqual(status, 503)
        self.assertEqual(response['error']['code'], 'encoding_failed')
        self.assertNotIn('nan', json.dumps(response).lower())

    def test_queued_deadline_expires_and_running_slot_is_retained_until_model_returns(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(2)
        self.model = FakeModel(block=True)
        self.server = text_encoder.create_server(self.model, port=0)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

        first_result = []

        def first_request():
            first_result.append(self.request(
                {'model': 'google/embeddinggemma-2', 'input': 'first'}))

        first = threading.Thread(target=first_request)
        first.start()
        self.assertTrue(self.model.started.wait(1))

        status, response = self.request(
            {'model': 'google/embeddinggemma-2', 'input': 'queued'},
            headers={'X-Quivr-Timeout-Ms': '1'})
        self.assertEqual(status, 504)
        self.assertEqual(response['error']['code'], 'deadline_exceeded')
        self.assertEqual(len(self.model.calls), 1)

        self.model.release.set()
        first.join(2)
        self.assertEqual(first_result[0][0], 200)

    def test_fifth_waiter_is_admitted_and_sixth_request_is_overloaded(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(2)
        model = FakeModel(block=True)
        self.server = text_encoder.BoundedHTTPServer(
            ('127.0.0.1', 0), text_encoder.EncoderHandler, model,
            {'status': 'ok', 'model': 'google/embeddinggemma-2',
             'model_revision': '914f7f89142e33e7',
             'source_revision': '914f7f89142e33e77833254d9c9b90c3cef7303b',
             'dimensions': 768})
        admitted = queue.Queue()

        class ObservedAdmission:
            """Test-only proxy that observes successful existing gate acquires."""
            def __init__(self, semaphore):
                self.semaphore = semaphore

            def acquire(self, *args, **kwargs):
                result = self.semaphore.acquire(*args, **kwargs)
                if result:
                    admitted.put(True)
                return result

            def release(self):
                return self.semaphore.release()

        self.server.admission = ObservedAdmission(self.server.admission)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

        results = []
        first = None
        waiters = []

        def request():
            results.append(self.request({'model': 'google/embeddinggemma-2', 'input': 'queued'}))

        try:
            first = threading.Thread(target=request)
            first.start()
            self.assertTrue(model.started.wait(1))
            self.assertTrue(admitted.get(timeout=1))
            waiters = [threading.Thread(target=request) for _ in range(4)]
            for waiter in waiters:
                waiter.start()
            for _ in waiters:
                self.assertTrue(admitted.get(timeout=1))

            status, response = self.request(
                {'model': 'google/embeddinggemma-2', 'input': 'overload'})
            self.assertEqual(status, 503)
            self.assertEqual(response['error']['code'], 'overloaded')
        finally:
            model.release.set()
            if first is not None:
                first.join(2)
            for waiter in waiters:
                waiter.join(2)
        self.assertEqual(len(results), 5)
        self.assertTrue(all(status == 200 for status, _ in results))


if __name__ == '__main__':
    unittest.main()
