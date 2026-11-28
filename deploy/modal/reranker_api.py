"""Authenticated TEI rerank boundary with no Modal or ML dependencies."""
import hmac
import json
import math
from dataclasses import dataclass

MAX_BODY = 8 * 1024 * 1024
MAX_TEXT_BYTES = 8 * 1024
MAX_TEXTS = 128
MAX_INSTRUCTION_BYTES = 2048

# Prompt protocol from the Qwen3-Reranker model card.
QWEN_PREFIX = (
    '<|im_start|>system\nJudge whether the Document meets the requirements based on the '
    'Query and the Instruct provided. Note that the answer can only be "yes" or "no".'
    '<|im_end|>\n<|im_start|>user\n'
)
QWEN_SUFFIX = '<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n'
QWEN_INSTRUCTION = 'Given a web search query, retrieve relevant passages that answer the query'


class InvalidRequest(ValueError):
    pass


@dataclass(frozen=True)
class RerankRequest:
    query: str
    texts: list[str]
    instruction: str | None
    truncate: bool
    truncation_direction: str
    raw_score: bool
    return_text: bool


def unique_members(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise InvalidRequest()
        result[key] = value
    return result


def checked_text(text, limit, param):
    try:
        if isinstance(text, str) and text.strip() and '\x00' not in text and len(text.encode()) <= limit:
            return text
    except UnicodeError:
        pass
    raise InvalidRequest(param)


def parse_request(body):
    if not isinstance(body, dict):
        raise InvalidRequest()
    query = checked_text(body.get('query'), MAX_TEXT_BYTES, 'query')
    texts = body.get('texts')
    if not isinstance(texts, list) or not 1 <= len(texts) <= MAX_TEXTS:
        raise InvalidRequest('texts')
    for text in texts:
        checked_text(text, MAX_TEXT_BYTES, 'texts')

    instruction = body.get('instruction')
    if instruction is not None:
        checked_text(instruction, MAX_INSTRUCTION_BYTES, 'instruction')

    for option in ('raw_score', 'raw_scores', 'truncate', 'return_text'):
        if option == 'truncate' and body.get(option) is None:
            continue
        if type(body.get(option, False)) is not bool:
            raise InvalidRequest(option)
    if 'raw_score' in body and 'raw_scores' in body and body['raw_score'] != body['raw_scores']:
        raise InvalidRequest('raw_score')
    raw_score = body.get('raw_score', body.get('raw_scores', False))
    direction = body.get('truncation_direction', 'Right')
    if not isinstance(direction, str) or direction.lower() not in ('left', 'right'):
        raise InvalidRequest('truncation_direction')
    return RerankRequest(query, texts, instruction, bool(body.get('truncate')), direction.lower(),
                         raw_score, body.get('return_text', False))


def qwen_pair(query, text, instruction):
    return f'<Instruct>: {instruction or QWEN_INSTRUCTION}\n<Query>: {query}\n<Document>: {text}'


def classifier_query(query, instruction):
    return f'{instruction}\n{query}' if instruction is not None else query


def logit_margin(logits):
    """Single relevance logit or positive minus negative class logits."""
    if len(logits) == 1:
        return logits[0]
    if len(logits) == 2:
        return logits[1] - logits[0]
    raise RuntimeError('expected one or two logits per pair')


def sigmoid(margin):
    # The negative branch avoids overflow on even very negative logits.
    if margin < 0:
        exp_margin = math.exp(margin)
        return exp_margin / (1 + exp_margin)
    return 1 / (1 + math.exp(-margin))


def response_ranks(margins, request):
    if len(margins) != len(request.texts) or any(
        type(margin) not in (int, float) or not math.isfinite(margin) for margin in margins
    ):
        raise RuntimeError('invalid rerank margins')
    # Sort in logit space so probability saturation cannot change the ranking.
    ranks = []
    for index in sorted(range(len(margins)), key=margins.__getitem__, reverse=True):
        margin = margins[index]
        rank = {'index': index, 'score': margin if request.raw_score else sigmoid(margin),
                'raw_score': margin}
        if request.return_text:
            rank['text'] = request.texts[index]
        ranks.append(rank)
    return ranks


def create_app(token, score, *, model, revision):
    """Score asynchronously in request order, returning None on token overflow."""
    if not token or any(not '!' <= c <= '~' for c in token):
        raise ValueError('RERANKER_API_KEY must be a nonempty printable ASCII bearer token without spaces')
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
            await reply(status, {'error': {'code': code, 'param': param}})

        authorization = [value for name, value in scope.get('headers', []) if name.lower() == b'authorization']
        if len(authorization) != 1 or not hmac.compare_digest(authorization[0], expected):
            await error(401, 'unauthorized')
            return
        path, method = scope['path'], scope['method']
        if path == '/health' and method == 'GET':
            await reply(200, {'status': 'ok', 'model': model, 'revision': revision})
            return
        if path not in ('/health', '/rerank'):
            await error(404, 'not_found')
            return
        if path != '/rerank' or method != 'POST':
            await error(405, 'method_not_allowed')
            return
        try:
            raw = bytearray()
            while True:
                message = await receive()
                if message['type'] == 'http.disconnect':
                    return
                chunk = message.get('body', b'')
                if len(raw) + len(chunk) > MAX_BODY:
                    raise InvalidRequest()
                raw.extend(chunk)
                if not message.get('more_body', False):
                    break
            request = parse_request(json.loads(raw, object_pairs_hook=unique_members))
        except (ValueError, RecursionError) as exc:
            param = exc.args[0] if isinstance(exc, InvalidRequest) and exc.args else None
            await error(400, 'invalid_request', param)
            return

        try:
            margins = await score(request)
            ranks = response_ranks(margins, request) if margins is not None else None
        except Exception:
            # Dependency errors can contain input text. Return only a stable error code.
            await error(503, 'reranking_failed')
            return
        if ranks is None:
            await error(400, 'input_too_long', 'texts')
            return
        await reply(200, ranks)

    return app
