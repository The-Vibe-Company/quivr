"""Run inside the release image with --network none and a mounted E5 tokenizer.

One small signed ingestion call checks packaging and real token packing; a
second process checks a corrupt pin. The provider is a loopback HTTP fake.
"""
import base64
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

from tokenizers import Tokenizer

from image_smoke import signed_request


def check(model, checksum):
    source = 'The quick brown fox jumps over the lazy dog.\n\nThe quick brown fox jumps over the lazy dog.'
    calls = []

    class Provider(BaseHTTPRequestHandler):
        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            if self.path != '/v1/embeddings' or request['model'] != 'intfloat/multilingual-e5-small':
                self.send_error(400)
                return
            calls.extend(request['input'])
            response = {'data': [{'index': index, 'embedding': [1.0] + [0.0] * 383}
                                 for index, _ in enumerate(request['input'])]}
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())

        def log_message(self, *args):
            pass

    provider = HTTPServer(('127.0.0.1', 0), Provider)
    thread = threading.Thread(target=lambda: provider.serve_forever(poll_interval=0.01), daemon=True)
    thread.start()
    try:
        configuration = json.loads(pathlib.Path('/smoke/tei.json').read_text())
        configuration.update(base_url=f'http://127.0.0.1:{provider.server_port}/v1',
                             body_tokens=32, max_tokens_per_segment=40, batch_wait_ms=0,
                             tokenizer={'python': sys.executable, 'model': model, 'sha256': checksum})
        for valid in (True, False):
            with tempfile.TemporaryDirectory() as tmp:
                directory = pathlib.Path(tmp)
                if not valid:
                    configuration['tokenizer']['sha256'] = '0' * 64
                config = directory / 'configuration.json'
                config.write_text(json.dumps(configuration))
                manifest = directory / 'quivr-plugin.yaml'
                manifest.write_bytes(subprocess.check_output(['/usr/local/bin/plugin', 'configure', str(config)]))
                declaration = json.loads(manifest.read_text())
                space = next(iter(declaration['contributions']['ingestion']['spaces']))
                secret = b'offline-image-smoke-signing-secret'
                ring = {'active': 'image-smoke', 'keys': [{'id': 'image-smoke',
                        'secret': base64.urlsafe_b64encode(secret).decode().rstrip('=')}]}
                environment = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1',
                               'QUIVR_PLUGIN_MANIFEST': str(manifest), 'QUIVR_PLUGIN_SIGNING_KEYS': json.dumps(ring)}
                with tempfile.TemporaryFile() as log:
                    process = subprocess.Popen(['/usr/local/bin/plugin'], env=environment, stdout=log, stderr=log)
                    try:
                        deadline = time.monotonic() + 10
                        while True:
                            try:
                                with urllib.request.urlopen('http://127.0.0.1:8080/v0/health', timeout=1):
                                    break
                            except OSError:
                                if process.poll() is not None or time.monotonic() >= deadline:
                                    raise RuntimeError('hosted image failed to start')
                        request = {'invocation_id': 'image-smoke', 'idempotency_key': 'image-smoke',
                                   'contribution': 'ingestion', 'organization_id': 'example',
                                   'configuration': configuration,
                                   'version': {'corpus_id': 'example', 'record_id': 'example', 'record_version_id': '1'},
                                   'parts': [{'key': 'body', 'role': 'body', 'text': source}], 'spaces': [space]}
                        count = len(calls)
                        wire = signed_request('127.0.0.1:8080', 'hosted.embed', secret,
                                              '/v0/contributions/ingestion/segment_and_embed', request)
                        try:
                            with urllib.request.urlopen(wire, timeout=10) as response:
                                result = json.load(response)
                            if not valid:
                                raise RuntimeError('corrupt tokenizer pin was accepted')
                        except urllib.error.HTTPError as error:
                            if valid or error.code != 503 or len(calls) != count:
                                raise
                            result = json.load(error)
                            if not result.get('retryable') or result.get('code') != 'tokenizer_unavailable':
                                raise RuntimeError('corrupt pin did not produce a retryable tokenizer failure')
                            continue
                        segments = result['segments']
                        if len(segments) != 1 or calls != ['passage: ' + source]:
                            raise RuntimeError('E5 did not pack both paragraphs without source loss')
                        tokenizer = Tokenizer.from_file(model)
                        tokenizer.no_padding()
                        tokenizer.no_truncation()
                        tokens = len(tokenizer.encode(calls[0]).ids)
                        provenance = segments[0]['provenance']
                        if tokens != 30 or provenance['input_tokens'] != tokens or provenance['token_estimate']:
                            raise RuntimeError('image did not report exact E5 prompt tokens')
                        if len(source.encode()) <= 32 or tokens > 40:
                            raise RuntimeError('smoke input does not distinguish tokens from byte packing')
                    finally:
                        process.terminate()
                        try:
                            process.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()
    finally:
        provider.shutdown()
        provider.server_close()
        thread.join(timeout=5)
    print('hosted-embed: offline E5 packing (90 body bytes, 30 prompt tokens) and corrupt-pin refusal passed')


if __name__ == '__main__':
    check(sys.argv[1], sys.argv[2])
