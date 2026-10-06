"""Offline owner contracts for run-wide embedding admission and configured pins."""
import json
import pathlib
import os
import subprocess
import sys
from unittest import mock
import tempfile
import unittest

import embeddings


class Admission(unittest.TestCase):
    def test_all_models_and_attempts_share_cap_and_uncertain_usage_remains_reserved(self):
        budget = embeddings.Budget(100, 1)
        first = budget.reserve('first', 'tiny', 'indexing', 40, .12)
        budget.settle(first, 10)
        uncertain = budget.reserve('second', 'tiny', 'query', 30, .08)
        budget.settle(uncertain, None)
        last = budget.reserve('first', 'tiny', 'query', 60, .12)
        with self.assertRaises(embeddings.BudgetExceeded):
            budget.reserve('second', 'tiny', 'query', 1, .08)
        budget.settle(last, 0)
        totals = budget.summary()
        self.assertEqual(totals['confirmed_input_tokens'], 10)
        self.assertEqual(totals['reserved_input_tokens'], 30)
        self.assertEqual(totals['admitted_calls'], 3)
        self.assertEqual(totals['blocked_calls'], 1)
        self.assertTrue(budget.stopped.is_set())
        self.assertAlmostEqual(totals['cost_upper_bound_usd'], .0000036)
        with self.assertRaises(embeddings.BudgetExceeded):
            budget.check()

    def test_dollar_cap_and_invalid_usage_fail_closed(self):
        budget = embeddings.Budget(1000, .000002)
        call = budget.reserve('expensive', 'tiny', 'indexing', 20, .1)
        budget.settle(call, -1)
        with self.assertRaises(embeddings.BudgetExceeded):
            budget.reserve('cheap', 'tiny', 'query', 1, .01)
        self.assertEqual(budget.summary()['reserved_input_tokens'], 20)
        other = embeddings.Budget(100, 1)
        call = other.reserve('model', 'tiny', 'query', 10, .1)
        with self.assertRaises(embeddings.BudgetExceeded):
            other.settle(call, 11)
        self.assertTrue(other.stopped.is_set())


class Installation(unittest.TestCase):
    def test_configured_plugin_secret_is_not_inherited_by_engine_children(self):
        with tempfile.TemporaryDirectory() as temp, \
             mock.patch.dict(os.environ, {'EMBED_TEST_KEY': 'fixture-only-key'}):
            directory = pathlib.Path(temp)
            (directory / 'plugin.yaml').write_text('id: example.embedding\n')
            selection = {'plugin': 'example.embedding', 'space': 'example.embedding.model@1',
                         'manifest': 'plugin.yaml', 'endpoint': 'http://127.0.0.1:8001',
                         'configuration': {}, 'secret_names': ['EMBED_TEST_KEY']}
            path = directory / 'selection.json'
            path.write_text(json.dumps(selection))
            embeddings.load_pin(path)
            # Exercise normal subprocess inheritance; print only a boolean.
            present = subprocess.check_output([sys.executable, '-c',
                'import os; print("EMBED_TEST_KEY" in os.environ)'], text=True).strip()
            self.assertEqual(present, 'False')
            self.assertNotIn('EMBED_TEST_KEY', os.environ)

    def test_pin_keeps_served_owner_and_installs_evaluation_on_both_processes(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = pathlib.Path(temp)
            manifest = directory / 'plugin.yaml'
            manifest.write_text('id: example.embedding\n')
            selection = {'plugin': 'example.embedding', 'space': 'example.embedding.model@1',
                         'manifest': str(manifest), 'endpoint': 'http://127.0.0.1:8001',
                         'configuration': {'model': 'neutral'}, 'secret_names': []}
            path = directory / 'selection.json'
            path.write_text(json.dumps(selection))
            loaded = embeddings.load_pin(path)
            for name in ('config.json', 'worker.json'):
                (directory / name).write_text(json.dumps({'plugins': [{'manifest': 'core.yaml'}],
                                                        'ingestion': {'default': 'core.ingest'}}))
            embeddings.install(directory, loaded)
            for name in ('config.json', 'worker.json'):
                config = json.loads((directory / name).read_text())
                self.assertEqual(config['ingestion'], {'default': 'core.ingest',
                                                      'evaluation': {'text/plain': ['example.embedding']}})
                self.assertEqual(config['plugins'][1]['configuration'], {'model': 'neutral'})
                self.assertNotIn('secret_names', config['plugins'][1])
                self.assertEqual(config['plugins'][1]['spaces'], {'example.embedding.model': 'served'})
                self.assertEqual((directory / name).stat().st_mode & 0o777, 0o600)


class Forwarding(unittest.TestCase):
    def test_openai_and_cohere_gate_accounts_retries_and_does_not_forward_after_cap(self):
        import http.server
        import threading
        import urllib.error
        import urllib.request
        for format in ('openai', 'cohere'):
            with self.subTest(format=format):
                received = []
                class Provider(http.server.BaseHTTPRequestHandler):
                    def log_message(self, *args):
                        pass
                    def do_POST(self):
                        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                        received.append((self.path, self.headers.get('api-key'), body))
                        self.send_response(429 if len(received) == 1 else 200)
                        self.send_header('Retry-After', '0')
                        self.end_headers()
                        if len(received) == 1:
                            self.wfile.write(b'{"error":"private-provider-error"}')
                        else:
                            result = ({'usage': {'prompt_tokens': 2},
                                       'data': [{'index': 0, 'embedding': [1, 0]}]} if format == 'openai' else
                                      {'meta': {'billed_units': {'input_tokens': 2}},
                                       'embeddings': {'float': [[1, 0]]}})
                            self.wfile.write(json.dumps(result).encode())
                server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Provider)
                thread = threading.Thread(target=server.serve_forever, kwargs={'poll_interval': .01})
                thread.start()
                try:
                    budget = embeddings.Budget(30, 1)
                    with embeddings.Gate(budget, 'deployment', format, f'http://127.0.0.1:{server.server_port}',
                                         'fixture-provider-key', .12, 2) as gate:
                        route, field, dims = (('/embeddings', 'input', 'dimensions') if format == 'openai'
                                               else ('/embed', 'texts', 'output_dimension'))
                        body = {'model': 'deployment', field: ['é'], dims: 2}
                        def request():
                            return urllib.request.urlopen(urllib.request.Request(gate.url + route,
                                data=json.dumps(body).encode(), headers={'Content-Type': 'application/json'}), timeout=2)
                        with self.assertRaises(urllib.error.HTTPError) as caught:
                            request()
                        self.assertEqual(caught.exception.code, 429)
                        self.assertEqual(caught.exception.headers['Retry-After'], '0')
                        self.assertNotIn(b'private-provider-error', caught.exception.read())
                        with request() as response:
                            self.assertEqual(response.status, 200)
                        with request():  # failed reservation 10, confirmed 2+2
                            pass
                        with request():
                            pass
                        with request():
                            pass
                        with request():
                            pass
                        with request():
                            pass
                        # 10 reserved + 12 confirmed = 22, next conservative 10 is refused.
                        with self.assertRaises(urllib.error.HTTPError) as caught:
                            request()
                        self.assertEqual(caught.exception.code, 402)
                        self.assertEqual(len(received), 7)
                        self.assertTrue(all(row[0] == route and row[1] == 'fixture-provider-key' for row in received))
                        self.assertEqual(budget.summary()['confirmed_input_tokens'], 12)
                        self.assertEqual(budget.summary()['reserved_input_tokens'], 10)
                        self.assertNotIn('fixture-provider-key', json.dumps(budget.summary()))
                finally:
                    server.shutdown()
                    server.server_close()
                    thread.join()

    def test_concurrent_requests_reserve_before_forwarding(self):
        from concurrent.futures import ThreadPoolExecutor
        budget = embeddings.Budget(100, 1)
        def reserve(_):
            try:
                budget.reserve('model', 'set', 'indexing', 60, .12)
                return True
            except embeddings.BudgetExceeded:
                return False
        with ThreadPoolExecutor(max_workers=8) as pool:
            admitted = list(pool.map(reserve, range(8)))
        self.assertEqual(sum(admitted), 1)
        self.assertEqual(budget.summary()['budgeted_input_tokens'], 60)


if __name__ == '__main__':
    unittest.main()
