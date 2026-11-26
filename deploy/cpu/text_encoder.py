"""Loopback-only, offline CPU text embeddings for the hosted query path."""

import argparse
import http.server
import importlib.metadata
import json
import os
import pathlib
import socket
import socketserver
import threading
import time

from deploy.modal.embedding_api import (
    DIMENSIONS,
    MODEL,
    InvalidRequest,
    encode_batch,
    request_texts,
    response_vectors,
    unique_members,
)
from scripts.prepare_query_encoder import verify_model


MODEL_REVISION = '914f7f89142e33e7'
SOURCE_REVISION = '914f7f89142e33e77833254d9c9b90c3cef7303b'
RUNTIME_LOCK_PATH = pathlib.Path('/app/third_party/query-encoder/model-lock.json')
REPOSITORY_LOCK_PATH = pathlib.Path(__file__).resolve().parents[2] / 'third_party' / 'query-encoder' / 'model-lock.json'
DEFAULT_MODEL_DIR = pathlib.Path('/opt/query-encoder/model')
DEFAULT_PORT = 9995
DEFAULT_THREADS = 4
DEFAULT_TIMEOUT_MS = 1000
MIN_TIMEOUT_MS = 1
MAX_TIMEOUT_MS = 10000
MAX_BODY = 512 * 1024
MAX_RUNNING = 1
MAX_WAITING = 4
MAX_HANDLERS = 8
WARMUP_TEXT = 'task: search result | query: readiness'
HEADER_TIMEOUT_SECONDS = MAX_TIMEOUT_MS / 1000
BACKEND_AUTO = 'auto'
BACKEND_ONNX = 'onnx'
BACKEND_PYTORCH = 'pytorch'
SUPPORTED_BACKENDS = (BACKEND_AUTO, BACKEND_ONNX, BACKEND_PYTORCH)
DEFAULT_ONNX_MODEL_FILE = 'onnx/model_quantized.onnx'
MAX_SEQUENCE_LENGTH = 2048
MULTIMODAL_FEATURE_DIM = 512
ONNX_MATMUL_NBITS_ACCURACY_LEVEL = 4
_MULTIMODAL_INPUTS = ('image_features', 'video_features', 'audio_features')


class RequestFailure(Exception):
    def __init__(self, status, code, param=None):
        self.status = status
        self.code = code
        self.param = param


def _default_lock_path():
    return RUNTIME_LOCK_PATH if RUNTIME_LOCK_PATH.exists() else REPOSITORY_LOCK_PATH


def _metadata(lock, *, threads=None, backend=None, precision=None,
              onnxruntime_version=None, torch_version=None):
    if (lock.get('model') != MODEL or lock.get('model_revision') != MODEL_REVISION
            or lock.get('source_revision') != SOURCE_REVISION
            or lock.get('dimensions') != DIMENSIONS):
        raise RuntimeError('query encoder lock identity does not match the service')
    metadata = {
        'status': 'ok',
        'model': MODEL,
        'model_revision': MODEL_REVISION,
        'source_revision': SOURCE_REVISION,
        'dimensions': DIMENSIONS,
    }
    if threads is not None:
        metadata['threads'] = threads
        if backend is not None:
            metadata['backend'] = backend
        if precision is not None:
            metadata['precision'] = precision
        if backend == BACKEND_ONNX:
            metadata.update({
                'onnxruntime_version': onnxruntime_version or 'unknown',
                'tokenizers_version': _distribution_version('tokenizers'),
                'numpy_version': _distribution_version('numpy'),
            })
        else:
            metadata.update({
                'torch_version': torch_version or 'unknown',
                'sentence_transformers_version': _distribution_version('sentence-transformers'),
                'transformers_version': _distribution_version('transformers'),
                'pillow_version': _distribution_version('pillow'),
            })
    return metadata


def _distribution_version(name):
    try:
        return importlib.metadata.version(name)
    except importlib.metadata.PackageNotFoundError:
        return 'unknown'


def _bounded_threads(threads):
    if type(threads) is not int or not 1 <= threads <= 32:
        raise ValueError('threads must be an integer from 1 to 32')
    return threads


def _resolve_backend(lock, backend=None):
    """Select onnx or pytorch from explicit config, env override, or lock contents."""
    raw = backend if backend is not None else os.environ.get('QUIVR_QUERY_ENCODER_BACKEND', BACKEND_AUTO)
    if not isinstance(raw, str):
        raise ValueError('query encoder backend must be auto, onnx or pytorch')
    selected = raw.strip().lower() or BACKEND_AUTO
    if selected not in SUPPORTED_BACKENDS:
        raise ValueError('query encoder backend must be auto, onnx or pytorch')
    files = lock.get('files', {})
    has_onnx = bool(lock.get('model_file') and str(lock['model_file']).endswith('.onnx')) or any(
        name.endswith('.onnx') for name in files
    )
    has_pytorch = 'model.safetensors' in files
    if selected == BACKEND_AUTO:
        lock_backend = lock.get('backend')
        if lock_backend in (BACKEND_ONNX, BACKEND_PYTORCH):
            selected = lock_backend
        else:
            selected = BACKEND_ONNX if has_onnx else BACKEND_PYTORCH
    if selected == BACKEND_ONNX and not has_onnx:
        raise RuntimeError('onnx backend requires a locked ONNX model asset')
    if selected == BACKEND_PYTORCH and not has_pytorch:
        raise RuntimeError('pytorch backend requires a locked PyTorch model asset')
    return selected


def _onnx_model_file(lock):
    model_file = lock.get('model_file')
    if model_file:
        return model_file
    if DEFAULT_ONNX_MODEL_FILE in lock.get('files', {}):
        return DEFAULT_ONNX_MODEL_FILE
    candidates = sorted(name for name in lock.get('files', {}) if name.endswith('.onnx'))
    if len(candidates) != 1:
        raise RuntimeError('unable to identify a single ONNX model file in lock')
    return candidates[0]


def _patch_matmul_nbits_accuracy_level(raw, accuracy_level=ONNX_MATMUL_NBITS_ACCURACY_LEVEL):
    """Set MatMulNBits accuracy_level in an ONNX ModelProto without extra dependencies.

    ONNX Runtime CPU executes MatMulNBits with accuracy_level=4 using its fast
    MLAS INT8 GEMM kernels (AVX-512 VNNI / AVX2) instead of slow FP32 weight
    dequantization, while keeping the on-disk verified model file untouched on
    read-only container filesystems.
    """
    if type(accuracy_level) is not int or not 0 <= accuracy_level <= 4:
        raise ValueError('accuracy_level must be an integer from 0 to 4')

    def read_varint(buf, pos):
        value = 0
        shift = 0
        while True:
            byte = buf[pos]
            pos += 1
            value |= (byte & 0x7F) << shift
            if not (byte & 0x80):
                return value, pos
            shift += 7
            if shift > 63:
                raise ValueError('invalid protobuf varint')

    def encode_varint(value):
        out = bytearray()
        while True:
            byte = value & 0x7F
            value >>= 7
            if value:
                out.append(byte | 0x80)
            else:
                out.append(byte)
                return bytes(out)

    def encode_bytes_field(field_no, data):
        return encode_varint((field_no << 3) | 2) + encode_varint(len(data)) + data

    def encode_varint_field(field_no, value):
        return encode_varint((field_no << 3) | 0) + encode_varint(value)

    def iter_fields(buf):
        pos = 0
        end = len(buf)
        while pos < end:
            start = pos
            tag, pos = read_varint(buf, pos)
            field_no = tag >> 3
            wire_type = tag & 7
            if wire_type == 0:
                value, pos = read_varint(buf, pos)
            elif wire_type == 1:
                if pos + 8 > end:
                    raise ValueError('truncated protobuf fixed64')
                value, pos = buf[pos:pos + 8], pos + 8
            elif wire_type == 2:
                length, pos = read_varint(buf, pos)
                if pos + length > end:
                    raise ValueError('truncated protobuf length-delimited field')
                value, pos = buf[pos:pos + length], pos + length
            elif wire_type == 5:
                if pos + 4 > end:
                    raise ValueError('truncated protobuf fixed32')
                value, pos = buf[pos:pos + 4], pos + 4
            else:
                raise ValueError(f'unsupported protobuf wire type: {wire_type}')
            yield field_no, wire_type, value, buf[start:pos]

    def attr_name(attr_bytes):
        for field_no, wire_type, value, _ in iter_fields(attr_bytes):
            if field_no == 1 and wire_type == 2:
                return value
        return b''

    # AttributeProto: name(1)='accuracy_level', i(3)=accuracy_level, type(20)=INT(2).
    acc_attr = (
        encode_bytes_field(1, b'accuracy_level')
        + encode_varint_field(3, accuracy_level)
        + encode_varint_field(20, 2)
    )

    def patch_node(node_bytes):
        fields = list(iter_fields(node_bytes))
        if not any(field_no == 4 and wire_type == 2 and value == b'MatMulNBits'
                   for field_no, wire_type, value, _ in fields):
            return node_bytes
        out = bytearray()
        for field_no, wire_type, value, raw_field in fields:
            if field_no == 5 and wire_type == 2 and attr_name(value) == b'accuracy_level':
                continue
            out.extend(raw_field)
        out.extend(encode_bytes_field(5, acc_attr))
        return bytes(out)

    def patch_graph(graph_bytes):
        out = bytearray()
        for field_no, wire_type, value, raw_field in iter_fields(graph_bytes):
            if field_no == 1 and wire_type == 2:
                out.extend(encode_bytes_field(1, patch_node(value)))
            else:
                out.extend(raw_field)
        return bytes(out)

    out = bytearray()
    for field_no, wire_type, value, raw_field in iter_fields(raw):
        if field_no == 7 and wire_type == 2:
            out.extend(encode_bytes_field(7, patch_graph(value)))
        else:
            out.extend(raw_field)
    return bytes(out)


class OnnxTextEncoder:
    """Offline CPU text encoder backed by ONNX Runtime and HuggingFace tokenizers."""

    def __init__(self, session, tokenizer, np_module, *, max_length=MAX_SEQUENCE_LENGTH):
        self._session = session
        self._tokenizer = tokenizer
        self._np = np_module
        self._input_names = {item.name for item in session.get_inputs()}
        self._output_names = [item.name for item in session.get_outputs()]
        self._has_sentence_embedding = 'sentence_embedding' in self._output_names
        if not self._has_sentence_embedding and 'last_hidden_state' not in self._output_names:
            raise RuntimeError('ONNX graph must expose sentence_embedding or last_hidden_state')
        pad_id = tokenizer.token_to_id('<pad>')
        tokenizer.enable_padding(pad_id=0 if pad_id is None else pad_id, pad_token='<pad>')
        tokenizer.enable_truncation(max_length=max_length)
        self._empty_features = np_module.zeros((0, MULTIMODAL_FEATURE_DIM), dtype=np_module.float32)
        self._empty_features.flags.writeable = False

    def encode(self, texts, *, prompt='', batch_size=32, show_progress_bar=False,
               convert_to_numpy=True, normalize_embeddings=True):
        del show_progress_bar
        np = self._np
        if isinstance(texts, str):
            texts = [texts]
        if prompt:
            texts = [prompt + text for text in texts]
        if not texts:
            return np.zeros((0, DIMENSIONS), dtype=np.float32)
        chunks = []
        step = max(1, int(batch_size))
        for start in range(0, len(texts), step):
            batch = texts[start:start + step]
            encoded = self._tokenizer.encode_batch(batch)
            input_ids = np.asarray([item.ids for item in encoded], dtype=np.int64)
            attention_mask = np.asarray([item.attention_mask for item in encoded], dtype=np.int64)
            feeds = {'input_ids': input_ids, 'attention_mask': attention_mask}
            for name in _MULTIMODAL_INPUTS:
                if name in self._input_names:
                    feeds[name] = self._empty_features
            if self._has_sentence_embedding:
                vectors = self._session.run(['sentence_embedding'], feeds)[0]
            else:
                hidden = self._session.run(['last_hidden_state'], feeds)[0]
                mask = attention_mask[:, :, None].astype(np.float32)
                denom = np.maximum(mask.sum(axis=1), 1e-12)
                vectors = (hidden * mask).sum(axis=1) / denom
            vectors = np.asarray(vectors, dtype=np.float32)
            if normalize_embeddings:
                norms = np.linalg.norm(vectors, axis=1, keepdims=True)
                vectors = vectors / np.maximum(norms, 1e-12)
            chunks.append(vectors)
        result = chunks[0] if len(chunks) == 1 else np.concatenate(chunks, axis=0)
        return result if convert_to_numpy else result.tolist()


def _load_onnx_model(model_dir, lock, threads):
    import numpy as np
    import onnxruntime as ort
    from tokenizers import Tokenizer

    model_root = pathlib.Path(model_dir)
    model_path = model_root / pathlib.PurePosixPath(_onnx_model_file(lock))
    tokenizer_path = model_root / 'tokenizer.json'

    options = ort.SessionOptions()
    options.intra_op_num_threads = threads
    options.inter_op_num_threads = 1
    options.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    options.add_session_config_entry(
        'session.model_external_initializers_file_folder_path',
        str(model_path.parent),
    )
    model_bytes = _patch_matmul_nbits_accuracy_level(
        model_path.read_bytes(),
        accuracy_level=ONNX_MATMUL_NBITS_ACCURACY_LEVEL,
    )
    session = ort.InferenceSession(
        model_bytes,
        sess_options=options,
        providers=['CPUExecutionProvider'],
    )
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    model = OnnxTextEncoder(session, tokenizer, np)
    return model, {
        'threads': threads,
        'backend': BACKEND_ONNX,
        'precision': lock.get('precision', 'int8'),
        'onnxruntime_version': str(ort.__version__),
        'tokenizers_version': _distribution_version('tokenizers'),
        'numpy_version': _distribution_version('numpy'),
    }


def _load_pytorch_model(model_dir, lock, threads):
    import torch
    from sentence_transformers import SentenceTransformer

    torch.set_num_threads(threads)
    torch.set_num_interop_threads(1)
    model = SentenceTransformer(
        str(model_dir),
        local_files_only=True,
        device='cpu',
        config_kwargs={'vision_config': None, 'audio_config': None},
        model_kwargs={'torch_dtype': torch.float32},
    )
    return model, {
        'threads': threads,
        'backend': BACKEND_PYTORCH,
        'precision': lock.get('precision', 'float32'),
        'torch_version': str(torch.__version__),
        'sentence_transformers_version': _distribution_version('sentence-transformers'),
        'transformers_version': _distribution_version('transformers'),
        'pillow_version': _distribution_version('pillow'),
    }


def load_model(model_dir=DEFAULT_MODEL_DIR, *, threads=DEFAULT_THREADS, lock_path=None, backend=None):
    """Verify assets, load the CPU text model, and warm it before readiness."""
    threads = _bounded_threads(threads)
    os.environ['HF_HUB_OFFLINE'] = '1'
    os.environ['TRANSFORMERS_OFFLINE'] = '1'
    lock = verify_model(model_dir, lock_path=lock_path or _default_lock_path())
    identity = _metadata(lock)
    selected_backend = _resolve_backend(lock, backend=backend)

    # Runtime imports happen only after the complete asset tree has passed its lock.
    if selected_backend == BACKEND_ONNX:
        model, runtime_metadata = _load_onnx_model(model_dir, lock, threads)
    else:
        model, runtime_metadata = _load_pytorch_model(model_dir, lock, threads)
    warm_vectors = encode_batch(model, [WARMUP_TEXT])
    response_vectors(warm_vectors, 1)
    identity.update(runtime_metadata)
    return model, identity


def _timeout_ms(headers):
    values = headers.get_all('X-Quivr-Timeout-Ms', [])
    if len(values) > 1:
        raise RequestFailure(400, 'invalid_request')
    if not values:
        return DEFAULT_TIMEOUT_MS
    raw = values[0].strip()
    if (not raw or len(raw) > len(str(MAX_TIMEOUT_MS)) or not raw.isascii()
            or not raw.isdecimal()):
        raise RequestFailure(400, 'invalid_request')
    value = int(raw)
    if not MIN_TIMEOUT_MS <= value <= MAX_TIMEOUT_MS:
        raise RequestFailure(400, 'invalid_request')
    return value


def _reject_json_constant(value):
    raise ValueError('non-finite JSON number')


class HeaderWatchdog:
    """Close an accepted socket if its complete HTTP headers take too long."""

    def __init__(self, connection, seconds):
        self.connection = connection
        self._lock = threading.Lock()
        self._cancelled = False
        self.timer = threading.Timer(seconds, self._expire)
        self.timer.daemon = True

    def start(self):
        self.timer.start()

    def cancel(self):
        with self._lock:
            self._cancelled = True
        self.timer.cancel()

    def _expire(self):
        with self._lock:
            if self._cancelled:
                return
            self._cancelled = True
        try:
            self.connection.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass


class EncoderHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    server_version = 'QuivrQueryEncoder'
    sys_version = ''

    def log_message(self, *_args):
        # Requests and dependency diagnostics must never enter service logs.
        return

    def log_error(self, *_args):
        return

    def setup(self):
        self._header_watchdog = self.server._take_header_watchdog(self.request)
        try:
            super().setup()
        except BaseException:
            self._cancel_header_watchdog()
            raise

    def finish(self):
        self._cancel_header_watchdog()
        super().finish()

    def _cancel_header_watchdog(self):
        watchdog = getattr(self, '_header_watchdog', None)
        if watchdog is not None:
            self._header_watchdog = None
            watchdog.cancel()

    def parse_request(self):
        try:
            return super().parse_request()
        finally:
            # The body and inference use their own propagated deadline after
            # this point; a completed header parse must stop the watchdog.
            self._cancel_header_watchdog()

    def send_error(self, code, *_args, **_kwargs):
        # BaseHTTPRequestHandler's default error page can echo request details.
        self._cancel_header_watchdog()
        status = code if 400 <= code <= 599 else 400
        self._error(status, 'invalid_request')

    def _reply(self, status, body, *, deadline=None):
        raw = json.dumps(body, allow_nan=False, separators=(',', ':')).encode('utf-8')
        try:
            if deadline is not None:
                remaining = deadline - time.monotonic()
                self.connection.settimeout(max(0.01, min(1.0, remaining)))
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(raw)))
            self.send_header('Connection', 'close')
            self.end_headers()
            self.wfile.write(raw)
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError, OSError):
            # A disconnected caller cannot receive a useful response. The
            # inference thread still reaches its finally block before ending.
            pass
        finally:
            self.close_connection = True

    def _error(self, status, code, param=None, *, deadline=None):
        self._reply(status, {
            'error': {
                'message': code,
                'type': 'server_error' if status >= 500 else 'invalid_request_error',
                'code': code,
                'param': param,
            },
        }, deadline=deadline)

    def _deadline(self):
        started = time.monotonic()
        timeout_ms = _timeout_ms(self.headers)
        return started + timeout_ms / 1000

    def _read_body(self, deadline):
        if self.headers.get('Transfer-Encoding'):
            raise RequestFailure(400, 'invalid_request')
        values = self.headers.get_all('Content-Length', [])
        if len(values) != 1:
            raise RequestFailure(400, 'invalid_request')
        raw_length = values[0].strip()
        if not raw_length or not raw_length.isascii() or not raw_length.isdecimal():
            raise RequestFailure(400, 'invalid_request')
        if len(raw_length) > len(str(MAX_BODY)):
            raise RequestFailure(400, 'invalid_request')
        length = int(raw_length)
        if length > MAX_BODY:
            raise RequestFailure(400, 'invalid_request')
        body = bytearray()
        read_chunk = getattr(self.rfile, 'read1', self.rfile.read)
        while len(body) < length:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RequestFailure(504, 'deadline_exceeded')
            try:
                # Recompute the socket timeout for every bounded read. A
                # client dripping one byte per read cannot extend the deadline.
                self.connection.settimeout(max(0.001, remaining))
                chunk = read_chunk(min(64 * 1024, length - len(body)))
            except (TimeoutError, socket.timeout):
                if deadline <= time.monotonic():
                    raise RequestFailure(504, 'deadline_exceeded') from None
                raise RequestFailure(408, 'request_timeout') from None
            except OSError:
                raise RequestFailure(408, 'request_timeout') from None
            if not chunk:
                raise RequestFailure(400, 'invalid_request')
            body.extend(chunk)
        return bytes(body)

    def _parse_body(self, raw):
        try:
            return request_texts(json.loads(
                raw,
                object_pairs_hook=unique_members,
                parse_constant=_reject_json_constant,
            ))
        except (InvalidRequest, ValueError, UnicodeError, RecursionError):
            raise RequestFailure(400, 'invalid_request') from None

    def do_GET(self):
        if self.path == '/health':
            self._reply(200, self.server.metadata)
        else:
            self._error(404, 'not_found')

    def do_POST(self):
        if self.path != '/v1/embeddings':
            self._error(404, 'not_found')
            return

        try:
            deadline = self._deadline()
        except RequestFailure as failure:
            self._error(failure.status, failure.code, failure.param)
            return

        if not self.server.admission.acquire(blocking=False):
            self._error(503, 'overloaded', deadline=deadline)
            return
        admitted = True
        running = False
        try:
            texts = self._parse_body(self._read_body(deadline))
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RequestFailure(504, 'deadline_exceeded')
            if not self.server.running.acquire(timeout=remaining):
                raise RequestFailure(504, 'deadline_exceeded')
            running = True
            if deadline <= time.monotonic():
                raise RequestFailure(504, 'deadline_exceeded')
            try:
                vectors = encode_batch(self.server.model, texts)
            except Exception:
                self._error(503, 'encoding_failed', deadline=deadline)
                return
            # CPU inference cannot be interrupted safely. Keep the running
            # semaphore until encode_batch has actually returned, then discard
            # vectors if the plugin's deadline elapsed during inference.
            if deadline <= time.monotonic():
                raise RequestFailure(504, 'deadline_exceeded')
            try:
                result = response_vectors(vectors, len(texts))
            except Exception:
                self._error(503, 'encoding_failed', deadline=deadline)
                return
            self._reply(200, result, deadline=deadline)
        except RequestFailure as failure:
            self._error(failure.status, failure.code, failure.param, deadline=deadline)
        finally:
            if running:
                self.server.running.release()
            if admitted:
                self.server.admission.release()


def _send_overload(request):
    body = b'{"error":{"message":"overloaded","type":"server_error","code":"overloaded","param":null}}'
    response = (b'HTTP/1.1 503 Service Unavailable\r\n'
                + b'Content-Type: application/json\r\n'
                + ('Content-Length: ' + str(len(body)) + '\r\n').encode('ascii')
                + b'Connection: close\r\n\r\n'
                + body)
    try:
        request.settimeout(0.25)
        request.sendall(response)
    except OSError:
        pass


class BoundedHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    """Threaded HTTP server with a hard cap on handler threads."""

    daemon_threads = True
    allow_reuse_address = True
    request_queue_size = 16

    def __init__(self, server_address, handler_cls, model, metadata, *, max_handlers=MAX_HANDLERS):
        if type(max_handlers) is not int or max_handlers < 1:
            raise ValueError('max_handlers must be positive')
        self._handler_slots = threading.BoundedSemaphore(max_handlers)
        self._header_watchdogs = {}
        self._header_watchdogs_lock = threading.Lock()
        self.model = model
        self.metadata = metadata
        self.admission = threading.BoundedSemaphore(MAX_RUNNING + MAX_WAITING)
        self.running = threading.Semaphore(MAX_RUNNING)
        super().__init__(server_address, handler_cls)

    def _register_header_watchdog(self, request, watchdog):
        with self._header_watchdogs_lock:
            self._header_watchdogs[id(request)] = watchdog

    def _take_header_watchdog(self, request):
        with self._header_watchdogs_lock:
            return self._header_watchdogs.pop(id(request), None)

    def _cancel_registered_header_watchdog(self, request):
        watchdog = self._take_header_watchdog(request)
        if watchdog is not None:
            watchdog.cancel()

    def process_request(self, request, client_address):
        if not self._handler_slots.acquire(blocking=False):
            _send_overload(request)
            self.shutdown_request(request)
            return
        try:
            # A client that drips headers must not occupy a handler forever.
            request.settimeout(MAX_TIMEOUT_MS / 1000)
            watchdog = HeaderWatchdog(request, HEADER_TIMEOUT_SECONDS)
            self._register_header_watchdog(request, watchdog)
            watchdog.start()
            thread = threading.Thread(
                target=self.process_request_thread,
                args=(request, client_address),
                daemon=self.daemon_threads,
            )
            thread.start()
        except BaseException:
            self._cancel_registered_header_watchdog(request)
            self._handler_slots.release()
            self.shutdown_request(request)
            raise

    def process_request_thread(self, request, client_address):
        try:
            socketserver.ThreadingMixIn.process_request_thread(self, request, client_address)
        finally:
            self._cancel_registered_header_watchdog(request)
            self._handler_slots.release()

    def handle_error(self, *_args):
        # Do not emit dependency errors or request content through the stdlib logger.
        return


def create_server(model, *, metadata=None, host='127.0.0.1', port=DEFAULT_PORT,
                  max_handlers=MAX_HANDLERS):
    if host != '127.0.0.1':
        raise ValueError('query encoder must bind loopback')
    metadata = metadata or {
        'status': 'ok', 'model': MODEL, 'model_revision': MODEL_REVISION,
        'source_revision': SOURCE_REVISION, 'dimensions': DIMENSIONS,
    }
    return BoundedHTTPServer((host, port), EncoderHandler, model, metadata,
                             max_handlers=max_handlers)


def _parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--model-dir', type=pathlib.Path, default=DEFAULT_MODEL_DIR)
    parser.add_argument('--port', type=int, default=DEFAULT_PORT)
    parser.add_argument('--threads', type=int, default=DEFAULT_THREADS)
    parser.add_argument('--backend', choices=SUPPORTED_BACKENDS,
                        default=os.environ.get('QUIVR_QUERY_ENCODER_BACKEND', BACKEND_AUTO))
    parser.add_argument('--lock-path', type=pathlib.Path, default=None)
    return parser


def main(argv=None):
    args = _parser().parse_args(argv)
    if not 1 <= args.port <= 65535:
        raise SystemExit('port must be from 1 to 65535')
    try:
        model, metadata = load_model(args.model_dir, threads=args.threads,
                                     lock_path=args.lock_path, backend=args.backend)
        server = create_server(model, port=args.port, metadata=metadata)
    except Exception:
        # Startup diagnostics stay generic: asset and dependency details belong
        # in the supervisor's private health record, never in the HTTP API/log.
        print('query encoder failed to start', file=os.sys.stderr)
        return 1
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
