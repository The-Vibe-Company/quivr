"""Serve synthetic S3 HTTP responses for the archive plugin Contract Runner.

The real-store and ingestion journey belongs to archive_source.py in the local
verification stack; this helper certifies the plugin protocol without Docker.
"""
import gzip
import io
import json
import pathlib
import signal
import sys
import tarfile
import threading
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def main(out):
    out = pathlib.Path(out)
    out.mkdir(parents=True, exist_ok=True)
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode='w') as archive:
        for name in ('001.xml', '002.xml', '003.xml'):
            data = f'<item>{name}</item>'.encode()
            entry = tarfile.TarInfo(name)
            entry.size = len(data)
            archive.addfile(entry, io.BytesIO(data))
    data = gzip.compress(raw.getvalue(), mtime=0)
    etag = '"synthetic-immutable"'
    key = 'inbox/2026.tar.gz'

    class Source(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_HEAD(self):
            self.do_GET()

        def do_GET(self):
            query = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
            if query.get('list-type') == ['2']:
                entry = '' if query.get('start-after') else f'<Contents><Key>{key}</Key><ETag>&quot;synthetic-immutable&quot;</ETag><Size>{len(data)}</Size></Contents>'
                body = f'<ListBucketResult><IsTruncated>false</IsTruncated>{entry}</ListBucketResult>'.encode()
                self.send_response(200)
                self.send_header('Content-Type', 'application/xml')
            elif self.headers.get('If-Match') != etag:
                self.send_response(412)
                self.end_headers()
                return
            else:
                body = data
                self.send_response(200)
                self.send_header('ETag', etag)
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            if self.command != 'HEAD':
                self.wfile.write(body)

    server = ThreadingHTTPServer(('127.0.0.1', 0), Source)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    path = out / 'archive.json'
    path.write_text(json.dumps({'description': 'Synthetic streamed archive and upload grants',
        'connector': {'kind': 'object_storage_archive', 'config': {
            'bucket': 'synthetic-archive', 'prefix': 'inbox/', 'region': 'us-east-1',
            'endpoint': f'http://127.0.0.1:{server.server_port}', 'media_type': 'application/xml',
            'batch_size': 2, 'concurrency': 4}},
        'credential': {'access_key_id': 'archive-certify-key', 'secret_access_key': 'archive-certify-secret'},
        'max_pages': 5,
        'expect': {'pages': [{'record_keys': ['001.xml', '002.xml'], 'more': True},
                             {'record_keys': ['003.xml'], 'more': True},
                             {'record_keys': [], 'more': False}]}}) + '\n')
    stopped = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stopped.set())
    print(path, flush=True)
    stopped.wait()
    server.shutdown()


if __name__ == '__main__':
    main(sys.argv[1])
