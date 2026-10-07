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


class RequestFailure(Exception):
    def __init__(self, status, code, param=None):
        self.status = status
        self.code = code
        self.param = param


def _default_lock_path():
    return RUNTIME_LOCK_PATH if RUNTIME_LOCK_PATH.exists() else REPOSITORY_LOCK_PATH


def _metadata(lock, *, threads=None, torch_version=None):
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
        metadata.update({
            'threads': threads,
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


def load_model(model_dir=DEFAULT_MODEL_DIR, *, threads=DEFAULT_THREADS, lock_path=None):
    """Verify assets, load the CPU text model, and warm it before readiness."""
    threads = _bounded_threads(threads)
    os.environ['HF_HUB_OFFLINE'] = '1'
    os.environ['TRANSFORMERS_OFFLINE'] = '1'
    lock = verify_model(model_dir, lock_path=lock_path or _default_lock_path())
    identity = _metadata(lock)

    # Imports happen only after the complete asset tree has passed its lock.
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
    warm_vectors = encode_batch(model, [WARMUP_TEXT])
    response_vectors(warm_vectors, 1)
    identity.update({
        'threads': threads,
        'torch_version': str(torch.__version__),
        'sentence_transformers_version': _distribution_version('sentence-transformers'),
        'transformers_version': _distribution_version('transformers'),
        'pillow_version': _distribution_version('pillow'),
    })
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
    return parser


def main(argv=None):
    args = _parser().parse_args(argv)
    if not 1 <= args.port <= 65535:
        raise SystemExit('port must be from 1 to 65535')
    try:
        model, metadata = load_model(args.model_dir, threads=args.threads)
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
