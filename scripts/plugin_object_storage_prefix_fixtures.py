"""Serve synthetic S3 HTTP responses for the prefix connector Contract Runner.

Certifies both key-ordered and timestamp-ordered resumable checkpoints across
text, XML, and PNG objects without Docker.
"""
import base64
import json
import pathlib
import signal
import sys
import threading
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# Minimal valid 1x1 red PNG.
PNG_BYTES = base64.b64decode(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='
)


def main(out):
    out = pathlib.Path(out)
    out.mkdir(parents=True, exist_ok=True)
    objects = [
        {
            'key': 'inbox/01-note.txt',
            'etag': '"etag-note-1"',
            'mtime': '2026-01-10T10:00:00Z',
            'content_type': 'text/plain',
            'body': b'Harbour notice: berth four opens at dawn.',
        },
        {
            'key': 'inbox/02-record.xml',
            'etag': '"etag-xml-2"',
            'mtime': '2026-01-10T10:05:00Z',
            'content_type': 'application/xml',
            'body': b'<notice><id>2</id><title>Berth schedule</title></notice>',
        },
        {
            'key': 'inbox/03-photo.png',
            'etag': '"etag-img-3"',
            'mtime': '2026-01-10T09:55:00Z',
            'content_type': 'image/png',
            'body': PNG_BYTES,
        },
    ]
    by_key = {o['key']: o for o in objects}

    class Source(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_HEAD(self):
            self.do_GET()

        def do_GET(self):
            parsed = urllib.parse.urlsplit(self.path)
            query = urllib.parse.parse_qs(parsed.query)
            if query.get('list-type') == ['2']:
                prefix = query.get('prefix', [''])[0]
                start_after = query.get('start-after', [''])[0]
                entries = []
                for obj in objects:
                    if not obj['key'].startswith(prefix):
                        continue
                    if start_after and obj['key'] <= start_after:
                        continue
                    etag_xml = obj['etag'].replace('"', '&quot;')
                    entries.append(
                        f"<Contents><Key>{obj['key']}</Key>"
                        f"<LastModified>{obj['mtime']}</LastModified>"
                        f"<ETag>{etag_xml}</ETag>"
                        f"<Size>{len(obj['body'])}</Size></Contents>"
                    )
                body = (
                    '<ListBucketResult><IsTruncated>false</IsTruncated>'
                    + ''.join(entries)
                    + '</ListBucketResult>'
                ).encode()
                self.send_response(200)
                self.send_header('Content-Type', 'application/xml')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                if self.command != 'HEAD':
                    self.wfile.write(body)
                return

            # Path-style: /<bucket>/<key>
            parts = parsed.path.lstrip('/').split('/', 1)
            key = urllib.parse.unquote(parts[1]) if len(parts) == 2 else ''
            obj = by_key.get(key)
            if obj is None:
                self.send_response(404)
                self.end_headers()
                return
            if self.headers.get('If-Match') and self.headers.get('If-Match') != obj['etag']:
                self.send_response(412)
                self.end_headers()
                return
            body = obj['body']
            self.send_response(200)
            self.send_header('Content-Type', obj['content_type'])
            self.send_header('ETag', obj['etag'])
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            if self.command != 'HEAD':
                self.wfile.write(body)

    server = ThreadingHTTPServer(('127.0.0.1', 0), Source)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    endpoint = f'http://127.0.0.1:{server.server_port}'
    key_fixture = out / 'prefix-key.json'
    key_fixture.write_text(json.dumps({
        'description': 'Synthetic S3 prefix stream with key checkpoint across text, XML and image objects',
        'connector': {
            'kind': 'object_storage_prefix',
            'config': {
                'bucket': 'synthetic-prefix',
                'prefix': 'inbox/',
                'region': 'us-east-1',
                'endpoint': endpoint,
                'checkpoint_mode': 'key',
                'batch_size': 2,
                'concurrency': 4,
            },
        },
        'credential': {'access_key_id': 'prefix-certify-key', 'secret_access_key': 'prefix-certify-secret'},
        'max_pages': 5,
        'expect': {
            'pages': [
                {'record_keys': ['01-note.txt', '02-record.xml'], 'more': True},
                {'record_keys': ['03-photo.png'], 'more': False},
            ],
        },
    }) + '\n')
    ts_fixture = out / 'prefix-timestamp.json'
    ts_fixture.write_text(json.dumps({
        'description': 'Synthetic S3 prefix stream with timestamp checkpoint across text, XML and image objects',
        'connector': {
            'kind': 'object_storage_prefix',
            'config': {
                'bucket': 'synthetic-prefix',
                'prefix': 'inbox/',
                'region': 'us-east-1',
                'endpoint': endpoint,
                'checkpoint_mode': 'timestamp',
                'batch_size': 2,
                'concurrency': 2,
            },
        },
        'credential': {'access_key_id': 'prefix-certify-key', 'secret_access_key': 'prefix-certify-secret'},
        'max_pages': 5,
        'expect': {
            'pages': [
                {'record_keys': ['03-photo.png', '01-note.txt'], 'more': True},
                {'record_keys': ['02-record.xml'], 'more': False},
            ],
        },
    }) + '\n')
    stopped = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stopped.set())
    print(key_fixture, ts_fixture, flush=True)
    stopped.wait()
    server.shutdown()


if __name__ == '__main__':
    main(sys.argv[1])
