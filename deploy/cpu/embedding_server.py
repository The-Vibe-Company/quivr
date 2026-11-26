"""Authenticated network adapter for the pinned CPU EmbeddingGemma model."""

import argparse
import hmac
import json
import os
import pathlib
import time
import urllib.error
import urllib.request

from deploy.cpu import text_encoder
from deploy.modal.embedding_api import DIMENSIONS, MODEL, REVISION as SOURCE_REVISION


MODEL_REVISION = text_encoder.MODEL_REVISION
DEFAULT_HOST = '0.0.0.0'
DEFAULT_PORT = 8080
DEFAULT_MODEL_DIR = pathlib.Path('/models/gemma')
DEFAULT_THREADS = 4
DEFAULT_DEADLINE_SECONDS = 10.0
MAX_HANDLERS = text_encoder.MAX_HANDLERS
MARKER_NAME = 'installation-model.json'
MARKER_MODEL = 'gemma'

# Reuse the existing loader and bounded transport for this network adapter.
load_model = text_encoder.load_model
BoundedHTTPServer = text_encoder.BoundedHTTPServer


def validate_token(token):
    if not isinstance(token, str) or not token or not token.isascii() \
            or any(character.isspace() for character in token):
        raise ValueError('EMBED_API_KEY must be a nonempty ASCII bearer token without whitespace')
    return token


def _health_metadata(metadata):
    """Return only verified, non-sensitive fields from the loader metadata."""
    metadata = dict(metadata)
    if (metadata.get('model') != MODEL or metadata.get('source_revision') != SOURCE_REVISION
            or metadata.get('dimensions') != DIMENSIONS):
        raise RuntimeError('embedding model identity does not match the service')
    if metadata.get('model_revision') != MODEL_REVISION:
        raise RuntimeError('embedding model revision does not match the service')
    health = {
        'status': 'ok',
        'model': MODEL,
        'revision': SOURCE_REVISION,
        'model_revision': MODEL_REVISION,
        'source_revision': SOURCE_REVISION,
        'dimensions': DIMENSIONS,
    }
    for key in ('threads', 'backend', 'precision', 'onnxruntime_version',
                'tokenizers_version', 'numpy_version', 'torch_version',
                'sentence_transformers_version', 'transformers_version', 'pillow_version'):
        if key in metadata:
            health[key] = metadata[key]
    return health


def _marker_path(model_dir, marker=None):
    return pathlib.Path(marker) if marker else pathlib.Path(model_dir).parent / MARKER_NAME


def _check_marker(path):
    """Require the cache selector to identify Gemma without exposing its contents."""
    path = pathlib.Path(path)
    if not path.is_file():
        return False
    try:
        marker = json.loads(path.read_text(encoding='utf-8'))
    except (OSError, UnicodeError, ValueError, TypeError):
        raise RuntimeError('invalid installation model marker') from None
    if not isinstance(marker, dict):
        raise RuntimeError('invalid installation model marker')
    if marker.get('model') != MARKER_MODEL:
        raise RuntimeError('installation model identity mismatch')
    return True


class EmbeddingHandler(text_encoder.EncoderHandler):
    """Add bearer authentication and the CPU provider's longer default deadline."""

    def _authenticated(self):
        values = [value for name, value in self.headers.items()
                  if name.lower() == 'authorization']
        if len(values) != 1:
            self._error(401, 'unauthorized')
            return False
        try:
            supplied = values[0].encode('ascii')
        except UnicodeEncodeError:
            self._error(401, 'unauthorized')
            return False
        if not hmac.compare_digest(supplied, self.server.expected_authorization):
            self._error(401, 'unauthorized')
            return False
        return True

    def _deadline(self):
        # The normal provider has no timeout header; its CPU request budget is
        # ten seconds. Keep the inherited bounded override for callers that
        # explicitly send one.
        values = self.headers.get_all('X-Quivr-Timeout-Ms', [])
        timeout_ms = (text_encoder._timeout_ms(self.headers) if values else
                      int(DEFAULT_DEADLINE_SECONDS * 1000))
        return time.monotonic() + timeout_ms / 1000

    def do_GET(self):
        if self._authenticated():
            super().do_GET()

    def do_POST(self):
        if self._authenticated():
            super().do_POST()


def create_server(model, token, *, metadata=None, host=DEFAULT_HOST, port=DEFAULT_PORT,
                  max_handlers=MAX_HANDLERS):
    """Bind the shared bounded server with the network adapter handler."""
    validate_token(token)
    if not isinstance(host, str) or not host:
        raise ValueError('host must be nonempty')
    if type(port) is not int or not 0 <= port <= 65535:
        raise ValueError('port must be from 0 to 65535')
    if metadata is None:
        metadata = {
            'status': 'ok', 'model': MODEL, 'model_revision': MODEL_REVISION,
            'source_revision': SOURCE_REVISION, 'dimensions': DIMENSIONS,
        }
    health = _health_metadata(metadata)
    server = BoundedHTTPServer((host, port), EmbeddingHandler, model, health,
                               max_handlers=max_handlers)
    # The shared server owns admission, running and handler bounds. The
    # adapter authentication is set before any request is served.
    server.expected_authorization = ('Bearer ' + token).encode('ascii')
    return server


def _env_int(name, default, low, high):
    raw = os.environ.get(name, str(default))
    if not raw.isascii() or not raw.isdecimal():
        raise ValueError(name + ' must be an integer')
    value = int(raw)
    if not low <= value <= high:
        raise ValueError(name + ' is outside its supported range')
    return value


def _parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', nargs='?', choices=('serve', 'healthcheck'), default='serve')
    parser.add_argument('--model-dir', type=pathlib.Path,
                        default=pathlib.Path(os.environ.get('QUIVR_MODEL_DIR', DEFAULT_MODEL_DIR)))
    parser.add_argument('--installation-marker', type=pathlib.Path)
    parser.add_argument('--host', default=os.environ.get('QUIVR_EMBEDDING_HOST', DEFAULT_HOST))
    parser.add_argument('--port', type=int,
                        default=_env_int('QUIVR_EMBEDDING_PORT', DEFAULT_PORT, 1, 65535))
    parser.add_argument('--threads', type=int,
                        default=_env_int('QUIVR_EMBEDDING_THREADS', DEFAULT_THREADS, 1, 32))
    parser.add_argument('--health-url', default=os.environ.get(
        'QUIVR_EMBEDDING_HEALTH_URL', 'http://127.0.0.1:8080/health'))
    return parser


def _healthcheck(url):
    try:
        token = validate_token(os.environ.get('EMBED_API_KEY', ''))
        request = urllib.request.Request(url, headers={'Authorization': 'Bearer ' + token})
        with urllib.request.urlopen(request, timeout=4) as response:
            if response.status != 200:
                return 1
            body = json.loads(response.read())
        if not isinstance(body, dict):
            return 1
        if (body.get('status') != 'ok' or body.get('model') != MODEL
                or body.get('revision') != SOURCE_REVISION
                or body.get('dimensions') != DIMENSIONS):
            return 1
        return 0
    except (OSError, ValueError, TypeError, UnicodeError, json.JSONDecodeError,
            urllib.error.URLError, AttributeError):
        return 1


def main(argv=None):
    args = _parser().parse_args(argv)
    if args.mode == 'healthcheck':
        return _healthcheck(args.health_url)
    try:
        token = validate_token(os.environ.get('EMBED_API_KEY', ''))
        marker = _marker_path(args.model_dir, args.installation_marker)
        if not _check_marker(marker):
            raise RuntimeError('installation model marker is missing')
        model, metadata = load_model(args.model_dir, threads=args.threads)
        server = create_server(model, token=token, metadata=metadata,
                               host=args.host, port=args.port)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass
        finally:
            server.server_close()
        return 0
    except Exception:
        print('CPU embedding service failed to start', file=os.sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
