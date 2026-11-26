"""Persistent pinned tokenizer of the core.ingest plugin. stdout is protocol JSON, never diagnostics or source logs.

Loads the pinned tokenizer once (checking the tokenizers version and the tokenizer.json digest): one
JSON request per stdin line, one JSON response per stdout line. The first line is "ready" once the
tokenizer is loaded. A request over the batch limits gets "null"; any framing problem ends the process.
"""
import hashlib, json, pathlib, sys
import tokenizers
from tokenizers import Tokenizer

TOKENIZERS_VERSION = '0.23.2'
TOKENIZER_SHA256 = '0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39'
MAX_REQUEST_BYTES = 4 * 1024 * 1024
MAX_ITEMS = 512
MAX_TEXT_BYTES = 2 * 1024 * 1024


def load(path):
    if tokenizers.__version__ != TOKENIZERS_VERSION: raise ValueError('tokenizer version mismatch')
    data = pathlib.Path(path).read_bytes()
    if hashlib.sha256(data).hexdigest() != TOKENIZER_SHA256: raise ValueError('tokenizer checksum mismatch')
    tokenizer = Tokenizer.from_str(data.decode('utf-8')); tokenizer.no_truncation(); tokenizer.no_padding()
    return tokenizer


def encode(tokenizer, request):
    if len(request) > MAX_ITEMS or sum(len(x['text'].encode('utf-8')) for x in request) > MAX_TEXT_BYTES: raise ValueError('tokenizer batch limit')
    result = []
    for item in request:
        encoded = tokenizer.encode(item['text'], add_special_tokens=item['special'])
        result.append(dict(tokens=len(encoded.ids), offsets=encoded.offsets))
    return result


def serve():
    tokenizer = load(sys.argv[1])
    stdin, stdout = sys.stdin.buffer, sys.stdout
    stdout.write('ready\n'); stdout.flush()
    while True:
        line = stdin.readline(MAX_REQUEST_BYTES + 2)
        if not line: return
        if not line.endswith(b'\n'): raise ValueError('tokenizer request framing')
        try:
            response = json.dumps(encode(tokenizer, json.loads(line)), ensure_ascii=True)
        except Exception:
            response = 'null'
        stdout.write(response + '\n'); stdout.flush()


try: serve()
except Exception:
    sys.stderr.write('tokenizer server failed\n'); sys.exit(1)
