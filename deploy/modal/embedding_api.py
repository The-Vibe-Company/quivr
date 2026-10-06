"""Authenticated ASGI boundary for text-only embeddings; independent of Modal/GPU imports."""
import hmac
import json
import math

MODEL = 'google/embeddinggemma-2'
REVISION = '914f7f89142e33e77833254d9c9b90c3cef7303b'
DIMENSIONS = 768
MAX_BODY = 512 * 1024  # Fits 32 maximum-size texts even with sixfold JSON escaping.
MAX_TEXT_BYTES = 2048  # Matches hosted.embed's conservative byte/token window.


class InvalidRequest(ValueError):
    def __init__(self, param):
        self.param = param


def unique_members(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise InvalidRequest(None)
        result[key] = value
    return result


def request_texts(body):
    if not isinstance(body, dict):
        raise InvalidRequest(None)
    if body.get('model') != MODEL:
        raise InvalidRequest('model')
    dimensions = body.get('dimensions', DIMENSIONS)
    if type(dimensions) is not int or dimensions != DIMENSIONS:
        raise InvalidRequest('dimensions')
    if body.get('encoding_format', 'float') != 'float':
        raise InvalidRequest('encoding_format')
    texts = body.get('input')
    if isinstance(texts, str):
        texts = [texts]
    if not isinstance(texts, list) or not 1 <= len(texts) <= 32:
        raise InvalidRequest('input')
    try:
        if any(not isinstance(text, str) or not text.strip() or '\x00' in text
               or len(text.encode('utf-8')) > MAX_TEXT_BYTES for text in texts):
            raise InvalidRequest('input')
    except UnicodeError:
        raise InvalidRequest('input') from None
    return texts


def encode_batch(model, texts):
    """The client supplies prompt bytes; suppress SentenceTransformer defaults."""
    vectors = model.encode(texts, prompt='', batch_size=32, show_progress_bar=False,
                           convert_to_numpy=True, normalize_embeddings=True)
    if vectors.shape != (len(texts), DIMENSIONS):
        raise RuntimeError('unexpected embedding shape')
    return vectors.tolist()


def response_vectors(vectors, count):
    if (len(vectors) != count or any(len(row) != DIMENSIONS
            or any(type(value) not in (int, float) or not math.isfinite(value) for value in row)
            for row in vectors)):
        raise RuntimeError('invalid embeddings')
    return {'object': 'list', 'model': MODEL,
            'data': [{'object': 'embedding', 'index': index, 'embedding': row}
                     for index, row in enumerate(vectors)]}


def create_app(token, embed):
    """embed is the production async Modal adapter; reject auth before reading inputs."""
    if not token or not token.isascii() or any(c.isspace() for c in token):
        raise ValueError('EMBED_API_KEY must be a nonempty ASCII bearer token without whitespace')
    expected = ('Bearer ' + token).encode()

    async def app(scope, receive, send):
        if scope['type'] == 'lifespan':
            while True:
                message = await receive()
                if message['type'] == 'lifespan.startup':
                    await send({'type': 'lifespan.startup.complete'})
                elif message['type'] == 'lifespan.shutdown':
                    await send({'type': 'lifespan.shutdown.complete'})
                    return
        if scope['type'] != 'http':
            return

        async def reply(status, body):
            raw = json.dumps(body, allow_nan=False).encode()
            headers = [(b'content-type', b'application/json'), (b'content-length', str(len(raw)).encode())]
            if status == 401:
                headers.append((b'www-authenticate', b'Bearer'))
            await send({'type': 'http.response.start', 'status': status, 'headers': headers})
            await send({'type': 'http.response.body', 'body': raw})

        async def error(status, code, param=None):
            await reply(status, {'error': {'message': code, 'type': 'invalid_request_error'
                                          if status < 500 else 'server_error', 'code': code, 'param': param}})

        authorization = [value for name, value in scope.get('headers', []) if name.lower() == b'authorization']
        if len(authorization) != 1 or not hmac.compare_digest(authorization[0], expected):
            await error(401, 'unauthorized')
            return
        if scope['path'] == '/health' and scope['method'] == 'GET':
            # Gateway readiness: health never wakes a GPU container.
            await reply(200, {'status': 'ok', 'model': MODEL, 'revision': REVISION})
            return
        if scope['path'] != '/v1/embeddings':
            await error(404, 'not_found')
            return
        if scope['method'] != 'POST':
            await error(405, 'method_not_allowed')
            return
        try:
            raw = bytearray()
            while True:
                message = await receive()
                if message['type'] == 'http.disconnect':
                    return
                raw.extend(message.get('body', b''))
                if len(raw) > MAX_BODY:
                    raise InvalidRequest(None)
                if not message.get('more_body', False):
                    break
            texts = request_texts(json.loads(raw, object_pairs_hook=unique_members))
        except (ValueError, UnicodeError) as exc:
            await error(400, 'invalid_request', getattr(exc, 'param', None))
            return
        try:
            vectors = await embed(texts)
            result = response_vectors(vectors, len(texts))
        except Exception:
            # Dependency errors may contain inputs; never return or log their payloads.
            await error(503, 'encoding_failed')
            return
        await reply(200, result)

    return app
