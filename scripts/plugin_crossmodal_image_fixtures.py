"""Serve a local HTTP cross-modal embedding stub for `quivr plugin test plugins/crossmodal-image`.

Certifies the normalizer, the builtin crossmodal.image.vision space, AND the
authenticated HTTP crossmodal.image.external space (verifying Bearer auth with
CROSSMODAL_API_KEY so the secrets check genuinely exercises the secret).
"""
import hashlib
import json
import math
import pathlib
import shutil
import signal
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = pathlib.Path(__file__).resolve().parents[1]
EXPECTED_BEARER = 'Bearer crossmodal-certify-secret'
DIMS = 512


def deterministic_vector(seed: bytes) -> list[float]:
    raw = []
    counter = 0
    while len(raw) < DIMS:
        digest = hashlib.sha256(seed + counter.to_bytes(4, 'big')).digest()
        for b in digest:
            raw.append((b / 127.5) - 1.0)
            if len(raw) == DIMS:
                break
        counter += 1
    norm = math.sqrt(sum(v * v for v in raw)) or 1.0
    return [round(v / norm, 6) for v in raw]


def main(out):
    out = pathlib.Path(out)
    out.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(
        ROOT / 'plugins' / 'crossmodal-image' / 'fixtures' / 'sample-red.png',
        out / 'sample-red.png',
    )

    class EmbedBackend(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            if self.headers.get('Authorization') != EXPECTED_BEARER:
                self.send_response(401)
                self.end_headers()
                return
            length = int(self.headers.get('Content-Length', '0'))
            payload = self.rfile.read(length)
            vec = deterministic_vector(payload)
            body = json.dumps({'vector': vec}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    server = ThreadingHTTPServer(('127.0.0.1', 0), EmbedBackend)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    endpoint = f'http://127.0.0.1:{server.server_port}/embed'

    norm_fixture = out / 'normalizer-png.json'
    norm_fixture.write_text(json.dumps({
        'description': 'Synthetic PNG image normalized into an immutable Blob Part with crossmodal.image.metadata dimensions.',
        'input': {
            'path': 'sample-red.png',
            'media_type': 'image/png',
        },
    }, indent=2) + '\n')

    builtin_fixture = out / 'ingestion-builtin.json'
    builtin_fixture.write_text(json.dumps({
        'description': 'Synthetic PNG image embedded into crossmodal.image.vision via the builtin backend.',
        'ingestion': {
            'configuration': {'backend': 'builtin'},
            'spaces': ['crossmodal.image.vision'],
            'blob_parts': [{
                'key': 'image',
                'role': 'image',
                'path': 'sample-red.png',
                'media_type': 'image/png',
            }],
            'queries': ['bright red square', 'dark blue landscape'],
        },
    }, indent=2) + '\n')

    http_fixture = out / 'ingestion-http.json'
    http_fixture.write_text(json.dumps({
        'description': 'Synthetic PNG image embedded into crossmodal.image.external via the authenticated HTTP backend.',
        'ingestion': {
            'configuration': {
                'backend': 'http',
                'base_url': endpoint,
                'auth': 'bearer',
            },
            'spaces': ['crossmodal.image.external'],
            'blob_parts': [{
                'key': 'image',
                'role': 'image',
                'path': 'sample-red.png',
                'media_type': 'image/png',
            }],
            'queries': ['bright red square'],
        },
    }, indent=2) + '\n')

    stopped = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stopped.set())
    print(norm_fixture, flush=True)
    print(builtin_fixture, flush=True)
    print(http_fixture, flush=True)
    stopped.wait()
    server.shutdown()


if __name__ == '__main__':
    main(sys.argv[1])
