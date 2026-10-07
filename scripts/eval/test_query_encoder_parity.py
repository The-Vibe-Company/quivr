"""Own the parity CLI's acceptance gate using independently calculated vectors."""
import contextlib
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

from scripts.eval import query_encoder_parity as parity


class QueryEncoderParityTest(unittest.TestCase):
    def test_expired_comparison_stops_worker_and_writes_failure_report(self):
        class StalledWorker:
            exitcode = None
            alive = True

            def start(self):
                pass

            def join(self, timeout):
                pass

            def is_alive(self):
                return self.alive

            def terminate(self):
                self.alive = False
                self.exitcode = -15

        worker = StalledWorker()
        with tempfile.TemporaryDirectory() as tmp:
            output = pathlib.Path(tmp) / 'report.json'
            with patch.object(parity.multiprocessing, 'get_context') as context, \
                 contextlib.redirect_stderr(io.StringIO()):
                context.return_value.Process.return_value = worker
                status = parity.run_bounded([
                    '--input', str(pathlib.Path(tmp) / 'private-input.json'),
                    '--output', str(output), '--local-url', 'http://local/v1',
                    '--remote-url', 'https://remote/v1', '--cpu-cores', '4',
                    '--threads', '4', '--max-seconds', '1'])
            self.assertEqual(status, 1)
            self.assertFalse(worker.is_alive())
            self.assertEqual(json.loads(output.read_text()), {
                'passed': False, 'demo_acceptance_measured': False,
                'error': 'comparison exceeded its overall deadline or was interrupted'})

    def test_response_transfer_observes_absolute_deadline(self):
        class TrickleResponse:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read1(self, _limit):
                return b' '

            def read(self, _limit):
                raise AssertionError('unbounded buffered read')

        with patch.object(parity.urllib.request, 'build_opener') as opener, \
             patch.object(parity.time, 'monotonic', side_effect=[0, 0, 2]):
            opener.return_value.open.return_value = TrickleResponse()
            with self.assertRaises(TimeoutError):
                parity.request_json('http://local/v1/embeddings', timeout=1)

    def test_cli_refuses_cosine_rank_latency_and_incomplete_evidence(self):
        # No real model calls: vary the HTTP dependency and measurement clock.
        # 768d candidates differ only in the first two coordinates.
        def vector(x, y):
            return [x, y] + [0.0] * 766
        candidates = [{'id': str(n), 'vector': vector(1, n / 1000)} for n in range(10)]
        for case in ('pass', 'cosine', 'rank', 'latency', 'insufficient', 'no_candidates'):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                source = pathlib.Path(tmp) / 'input.json'
                output = pathlib.Path(tmp) / 'result.json'
                count = 199 if case == 'insufficient' else 200
                queries = [{'id': str(n), 'text': 'A library opens. ' + str(n)} for n in range(count)]
                source.write_text(json.dumps({'queries': queries, 'candidates': [] if case == 'no_candidates' else candidates}))
                def encode(url, text, **kwargs):
                    if url == 'http://local/v1':
                        if case == 'cosine':
                            return vector(0, 1), 10.0
                        if case == 'rank':
                            return vector(1, 0.009), 10.0
                        return vector(1, 0), 101.0 if case == 'latency' else 10.0
                    return vector(1, 0), 1000.0
                with patch.object(parity, 'encode', side_effect=encode), \
                     patch.object(parity, 'check_readiness', return_value={'threads': 4}), \
                     contextlib.redirect_stderr(io.StringIO()):
                    status = parity.main(['--input', str(source), '--output', str(output),
                                          '--local-url', 'http://local/v1', '--remote-url', 'https://remote/v1',
                                          '--cpu-cores', '4', '--threads', '4', '--concurrency', '2'])
                self.assertEqual(status, 0 if case == 'pass' else 1)
                if output.exists():
                    result = json.loads(output.read_text())
                    self.assertEqual(result['passed'], case == 'pass')
                    self.assertNotIn('A library opens.', output.read_text())
                    if case == 'rank':
                        self.assertGreater(result['minimum_cosine'], 0.999)
                        self.assertGreater(result['top10_mismatches'], 0)
                    if case == 'pass':
                        self.assertEqual(result['query_count'], 200)
                        self.assertEqual(result['local_sequential_ms']['p95'], 10.0)
                        self.assertEqual(result['local_concurrent_ms']['p95'], 10.0)
                        self.assertFalse(result['demo_acceptance_measured'])


if __name__ == '__main__':
    unittest.main()
