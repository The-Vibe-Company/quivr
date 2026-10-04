"""Remote smoke owner at the public HTTP boundary; no paid provider transport.

The stack path itself is proved separately by the local real-stack smoke.
These cheap cases protect failure semantics and exported evidence shape.
"""
import io
import json
import threading
import unittest
import os
import pathlib
import subprocess
import sys
import tempfile
from unittest import mock
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import engine_stack
import run


class Smoke(unittest.TestCase):
    def test_searches_all_modes_and_refuses_a_missing_relevant_record(self):
        calls = []
        class API(BaseHTTPRequestHandler):
            missing = False
            def log_message(self, *args):
                pass
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                calls.append((self.path, body))
                if self.path == '/v0/search':
                    reply = {'items': [] if self.missing else [{'record_id': 'record-apple'}], 'usage': {}}
                    code = 200
                elif self.path == '/v0/corpora':
                    reply, code = {'corpus_id': 'corpus-fixture'}, 201
                else:
                    reply, code = {}, 400
                encoded = json.dumps(reply).encode()
                self.send_response(code)
                self.end_headers()
                self.wfile.write(encoded)
        server = ThreadingHTTPServer(('127.0.0.1', 0), API)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            client = run.Client('http://127.0.0.1:' + str(server.server_port), 'fixture')
            # This boundary also serves the confirmation adapter later. Ingest
            # is exercised by the real local stack, not faked here.
            row = engine_stack.search_smoke(client, 'corpus-fixture', {'record-apple': 'orchard'})
            self.assertEqual(row, {'documents': 3, 'searches': 3, 'modes': ['lexical', 'semantic', 'hybrid']})
            self.assertEqual([body['mode'] for path, body in calls], ['lexical', 'semantic', 'hybrid'])
            API.missing = True
            with self.assertRaises(RuntimeError):
                engine_stack.search_smoke(client, 'corpus-fixture', {'record-apple': 'orchard'})
            self.assertEqual(len(calls), 4)  # fail before trying remaining modes
            self.assertNotIn('record-apple', json.dumps(row))
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


class Supervisor(unittest.TestCase):
    def test_entrypoint_reaps_daemon_and_runner_on_initial_or_active_keepalive_loss(self):
        # Real owned process groups, fake only executable transports and time.
        # Expiring the clock must kill both, even when create's response was lost.
        for active in (False, True):
            with self.subTest(active=active), tempfile.TemporaryDirectory() as temp:
                children = []
                popen = subprocess.Popen
                def child(args, **kwargs):
                    process = popen([sys.executable, '-c', 'import sys; sys.stdin.read()'], stdin=subprocess.PIPE, **kwargs)
                    children.append(process)
                    return process
                read, write = os.pipe()
                os.write(write, b'ping\n')
                clock = [0, 0, 0, 100] if active else [0, 100]
                cfg = {'max_seconds': 300, 'reaper_seconds': 30, 'cleanup_seconds': 10}
                try:
                    with os.fdopen(read) as incoming, mock.patch.object(engine_stack.sys, 'stdin', incoming), \
                            mock.patch.object(engine_stack.subprocess, 'Popen', side_effect=child), \
                            mock.patch.object(engine_stack.subprocess, 'run', return_value=mock.Mock(returncode=0)), \
                            mock.patch.object(engine_stack.time, 'monotonic', side_effect=clock), \
                            mock.patch('sys.stdout', new_callable=io.StringIO) as output:
                        self.assertEqual(engine_stack.supervise(cfg), 2)
                    self.assertEqual(len(children), 2 if active else 1)
                    self.assertTrue(all(process.poll() is not None for process in children))
                    row = json.loads(output.getvalue().splitlines()[-1])
                    self.assertEqual(row['error_class'], 'TimeoutError')
                    self.assertFalse(row['remote_cleanup_verified'])
                finally:
                    os.close(write)
                    for process in children:
                        process.stdin.close()


if __name__ == '__main__':
    unittest.main()
