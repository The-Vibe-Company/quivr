"""Owner-boundary tests for the offline CPU query encoder service."""
import hashlib
import http.client
import json
import math
import os
import pathlib
import queue
import socket
import sys
import tempfile
import threading
import types
import unittest
from unittest.mock import patch

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
            self.release.wait()
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
                raw=None, timeout=2):
        connection = http.client.HTTPConnection('127.0.0.1', self.server.server_port,
                                                timeout=timeout)
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

    def test_slow_headers_have_total_deadline_and_completed_headers_cancel_watchdog(self):
        timers = queue.Queue()

        class ControlledTimer:
            def __init__(self, _seconds, callback):
                self.callback = callback
                self.cancelled = threading.Event()

            def start(self):
                timers.put(self)

            def cancel(self):
                self.cancelled.set()

            def fire(self):
                self.callback()

        client = None
        completed = None
        with patch.object(text_encoder.threading, 'Timer', ControlledTimer):
            try:
                client = socket.create_connection(('127.0.0.1', self.server.server_port), timeout=1)
                client.sendall(b'GET /health HTTP/1.1\r\nHost: localhost\r\n')
                timers.get(timeout=1).fire()
                self.assertEqual(client.recv(1), b'')

                completed = socket.create_connection(
                    ('127.0.0.1', self.server.server_port), timeout=1)
                completed.sendall(b'GET /health HTTP/1.1\r\nHost: localhost\r\n\r\n')
                timer = timers.get(timeout=1)
                response = completed.recv(4096)
                self.assertIn(b' 200 ', response)
                self.assertTrue(timer.cancelled.wait(1))
            finally:
                if client is not None:
                    client.close()
                if completed is not None:
                    completed.close()

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
        try:
            self.assertTrue(self.model.started.wait(1))

            status, response = self.request(
                {'model': 'google/embeddinggemma-2', 'input': 'queued'},
                headers={'X-Quivr-Timeout-Ms': '1'})
            self.assertEqual(status, 504)
            self.assertEqual(response['error']['code'], 'deadline_exceeded')
            self.assertEqual(len(self.model.calls), 1)
        finally:
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
            results.append(self.request(
                {'model': 'google/embeddinggemma-2', 'input': 'queued'}, timeout=12,
                headers={'X-Quivr-Timeout-Ms': '10000'}))

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
                first.join(12)
            for waiter in waiters:
                waiter.join(12)
        self.assertEqual(len(results), 5)
        self.assertTrue(all(status == 200 for status, _ in results))


def _pb_varint(value):
    out = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        if value:
            out.append(byte | 0x80)
        else:
            out.append(byte)
            return bytes(out)


def _pb_bytes(field_no, data):
    return _pb_varint((field_no << 3) | 2) + _pb_varint(len(data)) + data


def _pb_int(field_no, value):
    return _pb_varint((field_no << 3) | 0) + _pb_varint(value)


class _FakeNumpyArray:
    """Minimal numeric array stand-in so unit tests run with stdlib only."""

    def __init__(self, data, dtype=None, shape=None):
        self._data = data
        self.dtype = dtype
        self.flags = types.SimpleNamespace(writeable=True)
        if shape is not None:
            self.shape = tuple(shape)
        elif isinstance(data, list) and data and isinstance(data[0], list):
            if data[0] and isinstance(data[0][0], list):
                self.shape = (len(data), len(data[0]), len(data[0][0]))
            else:
                self.shape = (len(data), len(data[0]))
        elif isinstance(data, list):
            self.shape = (len(data),)
        else:
            self.shape = ()

    def tolist(self):
        return self._data

    def __truediv__(self, other):
        if isinstance(other, _FakeNumpyArray):
            rows = []
            for row, denom in zip(self._data, other._data):
                scale = denom[0] if isinstance(denom, list) else denom
                rows.append([value / scale for value in row])
            return _FakeNumpyArray(rows, dtype=self.dtype)
        return _FakeNumpyArray([[v / other for v in row] for row in self._data], dtype=self.dtype)


class _FakeNumpy:
    int64 = 'int64'
    float32 = 'float32'

    @staticmethod
    def zeros(shape, dtype=None):
        if len(shape) == 2:
            return _FakeNumpyArray([[0.0] * shape[1] for _ in range(shape[0])], dtype=dtype, shape=shape)
        return _FakeNumpyArray([0.0] * shape[0], dtype=dtype, shape=shape)

    @staticmethod
    def asarray(data, dtype=None):
        if isinstance(data, _FakeNumpyArray):
            return _FakeNumpyArray(data._data, dtype=dtype or data.dtype)
        return _FakeNumpyArray(data, dtype=dtype)

    @staticmethod
    def maximum(arr, floor):
        return _FakeNumpyArray([[max(v, floor) for v in row] for row in arr._data], dtype=arr.dtype)

    @staticmethod
    def concatenate(arrays, axis=0):
        assert axis == 0
        merged = []
        for arr in arrays:
            merged.extend(arr._data)
        return _FakeNumpyArray(merged, dtype=arrays[0].dtype)

    class linalg:
        @staticmethod
        def norm(arr, axis=1, keepdims=True):
            assert axis == 1 and keepdims
            return _FakeNumpyArray([[math.sqrt(sum(v * v for v in row))] for row in arr._data], dtype=arr.dtype)


class OnnxEncoderBackendTest(unittest.TestCase):
    def test_matmul_nbits_protobuf_patcher_updates_only_matmul_nbits_nodes(self):
        existing_acc = _pb_bytes(1, b'accuracy_level') + _pb_int(3, 0) + _pb_int(20, 2)
        bits_attr = _pb_bytes(1, b'bits') + _pb_int(3, 8) + _pb_int(20, 2)
        nbits_without_acc = _pb_bytes(4, b'MatMulNBits') + _pb_bytes(5, bits_attr)
        nbits_with_zero = _pb_bytes(4, b'MatMulNBits') + _pb_bytes(5, bits_attr) + _pb_bytes(5, existing_acc)
        plain_matmul = _pb_bytes(4, b'MatMul') + _pb_bytes(2, b'out')
        graph = (_pb_bytes(1, nbits_without_acc)
                 + _pb_bytes(1, nbits_with_zero)
                 + _pb_bytes(1, plain_matmul))
        model_proto = _pb_int(1, 10) + _pb_bytes(7, graph)

        patched = text_encoder._patch_matmul_nbits_accuracy_level(model_proto, accuracy_level=4)
        self.assertEqual(patched.count(b'accuracy_level'), 2)
        expected_attr = _pb_bytes(1, b'accuracy_level') + _pb_int(3, 4) + _pb_int(20, 2)
        self.assertEqual(patched.count(expected_attr), 2)
        self.assertNotIn(existing_acc, patched)
        self.assertIn(plain_matmul, patched)
        # Idempotent when patched a second time.
        self.assertEqual(
            text_encoder._patch_matmul_nbits_accuracy_level(patched, accuracy_level=4),
            patched,
        )
        with self.assertRaises(ValueError):
            text_encoder._patch_matmul_nbits_accuracy_level(model_proto, accuracy_level=5)

    def test_onnx_load_model_verifies_lock_configures_ort_and_normalizes_vectors(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            model_dir = root / 'model'
            onnx_dir = model_dir / 'onnx'
            onnx_dir.mkdir(parents=True)

            nbits_node = _pb_bytes(4, b'MatMulNBits')
            model_bytes = _pb_int(1, 10) + _pb_bytes(7, _pb_bytes(1, nbits_node))
            data_bytes = b'quantized-weights'
            tok_bytes = b'{"version":"1.0"}'
            (onnx_dir / 'model_quantized.onnx').write_bytes(model_bytes)
            (onnx_dir / 'model_quantized.onnx_data').write_bytes(data_bytes)
            (model_dir / 'tokenizer.json').write_bytes(tok_bytes)

            lock_path = root / 'model-lock.json'
            lock_path.write_text(json.dumps({
                'model': text_encoder.MODEL,
                'model_revision': text_encoder.MODEL_REVISION,
                'source_revision': text_encoder.SOURCE_REVISION,
                'dimensions': text_encoder.DIMENSIONS,
                'backend': 'onnx',
                'precision': 'int8',
                'model_file': 'onnx/model_quantized.onnx',
                'files': {
                    'onnx/model_quantized.onnx': {
                        'sha256': hashlib.sha256(model_bytes).hexdigest(),
                        'size': len(model_bytes),
                    },
                    'onnx/model_quantized.onnx_data': {
                        'sha256': hashlib.sha256(data_bytes).hexdigest(),
                        'size': len(data_bytes),
                    },
                    'tokenizer.json': {
                        'sha256': hashlib.sha256(tok_bytes).hexdigest(),
                        'size': len(tok_bytes),
                    },
                },
            }))

            recorded = {}

            class FakeSessionOptions:
                def __init__(self):
                    self.intra_op_num_threads = None
                    self.inter_op_num_threads = None
                    self.graph_optimization_level = None
                    self.config_entries = {}

                def add_session_config_entry(self, key, value):
                    self.config_entries[key] = value

            class FakeSession:
                def __init__(self, payload, sess_options=None, providers=None):
                    recorded['payload'] = payload
                    recorded['options'] = sess_options
                    recorded['providers'] = providers
                    recorded['feeds'] = []

                def get_inputs(self):
                    return [
                        types.SimpleNamespace(name=name)
                        for name in ('input_ids', 'attention_mask', 'image_features', 'video_features', 'audio_features')
                    ]

                def get_outputs(self):
                    return [types.SimpleNamespace(name='sentence_embedding')]

                def run(self, outputs, feeds):
                    recorded['feeds'].append((outputs, feeds))
                    batch = feeds['input_ids'].shape[0]
                    # Non-unit [3.0, 4.0, 0, ...] verifies L2 normalization -> [0.6, 0.8, 0, ...].
                    return [_FakeNumpyArray([[3.0, 4.0] + [0.0] * (text_encoder.DIMENSIONS - 2) for _ in range(batch)], dtype='float32')]

            class FakeTokenizer:
                def __init__(self, path):
                    recorded['tokenizer_path'] = path
                    self.padding = None
                    self.truncation = None
                    self.encoded_batches = []

                @classmethod
                def from_file(cls, path):
                    return cls(path)

                def token_to_id(self, token):
                    return 0 if token == '<pad>' else None

                def enable_padding(self, **kwargs):
                    self.padding = kwargs

                def enable_truncation(self, **kwargs):
                    self.truncation = kwargs

                def encode_batch(self, texts):
                    self.encoded_batches.append(list(texts))
                    return [types.SimpleNamespace(ids=[2, 10, 1], attention_mask=[1, 1, 1]) for _ in texts]

            fake_ort = types.SimpleNamespace(
                __version__='1.32.0',
                SessionOptions=FakeSessionOptions,
                GraphOptimizationLevel=types.SimpleNamespace(ORT_ENABLE_ALL='all'),
                InferenceSession=FakeSession,
            )
            fake_tokenizers = types.SimpleNamespace(Tokenizer=FakeTokenizer)

            with patch.dict(sys.modules, {
                'numpy': _FakeNumpy,
                'onnxruntime': fake_ort,
                'tokenizers': fake_tokenizers,
                'torch': None,  # Ensures PyTorch is never imported by the ONNX path.
            }):
                model, metadata = text_encoder.load_model(model_dir, threads=4, lock_path=lock_path)

            self.assertEqual(metadata['backend'], 'onnx')
            self.assertEqual(metadata['precision'], 'int8')
            self.assertEqual(metadata['threads'], 4)
            self.assertEqual(metadata['onnxruntime_version'], '1.32.0')
            self.assertNotIn('torch_version', metadata)
            self.assertEqual(recorded['options'].intra_op_num_threads, 4)
            self.assertEqual(recorded['options'].inter_op_num_threads, 1)
            self.assertEqual(recorded['options'].graph_optimization_level, 'all')
            self.assertEqual(
                recorded['options'].config_entries['session.model_external_initializers_file_folder_path'],
                str(onnx_dir),
            )
            self.assertEqual(recorded['providers'], ['CPUExecutionProvider'])
            self.assertIn(b'accuracy_level', recorded['payload'])
            # Original on-disk file stays untouched for read-only containers.
            self.assertEqual((onnx_dir / 'model_quantized.onnx').read_bytes(), model_bytes)

            vectors = text_encoder.encode_batch(model, ['task: search result | query: alpha', 'task: search result | query: beta'])
            self.assertEqual(len(vectors), 2)
            self.assertAlmostEqual(vectors[0][0], 0.6, places=6)
            self.assertAlmostEqual(vectors[0][1], 0.8, places=6)
            _, last_feeds = recorded['feeds'][-1]
            self.assertEqual(last_feeds['image_features'].shape, (0, text_encoder.MULTIMODAL_FEATURE_DIM))
            self.assertEqual(last_feeds['video_features'].shape, (0, text_encoder.MULTIMODAL_FEATURE_DIM))
            self.assertEqual(last_feeds['audio_features'].shape, (0, text_encoder.MULTIMODAL_FEATURE_DIM))

            # Explicit backend mismatch fails fast before loading any runtime.
            with self.assertRaisesRegex(RuntimeError, 'pytorch backend requires'):
                text_encoder.load_model(model_dir, threads=4, lock_path=lock_path, backend='pytorch')
            with self.assertRaisesRegex(ValueError, 'auto, onnx or pytorch'):
                text_encoder.load_model(model_dir, threads=4, lock_path=lock_path, backend='invalid')


if __name__ == '__main__':
    unittest.main()
