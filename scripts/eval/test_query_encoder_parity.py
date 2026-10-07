"""Own the parity CLI's acceptance gate using independently calculated vectors."""
import base64
import contextlib
import hashlib
import hmac
import os
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

from scripts.eval import query_encoder_parity as parity


class QueryEncoderParityTest(unittest.TestCase):
    def test_plugin_attempts_have_distinct_invocations_and_ties_use_candidate_ids(self):
        vector = [1.0] + [0.0] * 767
        secret = b'neutral-fixture-engine-signing-key'
        ring = {'active': 'fixture', 'keys': [{'id': 'fixture',
                'secret': base64.urlsafe_b64encode(secret).decode().rstrip('=')}]}
        with patch.object(parity.urllib.request, 'build_opener') as opener, \
             patch.dict(os.environ, {'QUIVR_PLUGIN_SIGNING_KEYS': json.dumps(ring)}):
            opener.return_value.open.side_effect = [io.BytesIO(json.dumps({'vector': vector}).encode()) for _ in range(3)]
            for _ in range(2):
                parity.encode('http://127.0.0.1', 'A library opens.',
                              plugin={'configuration': {'plugin_id': 'hosted.embed.evaluation'}, 'space': 'evaluation'})
            with self.assertRaises(ValueError):
                parity.encode('http://192.0.2.1', 'A library opens.',
                              plugin={'configuration': {}, 'space': 'evaluation'})
            self.assertEqual(opener.return_value.open.call_count, 2)
        attempts = []
        for call in opener.return_value.open.call_args_list:
            request = call.args[0]
            header64, claims64, signature64 = request.get_header('Authorization')[7:].split('.')
            def decode(value):
                return base64.urlsafe_b64decode(value + '=' * (-len(value) % 4))
            self.assertEqual(hmac.digest(secret, (header64 + '.' + claims64).encode(), 'sha256'), decode(signature64))
            claims = json.loads(decode(claims64))
            self.assertEqual(claims['aud'], 'hosted.embed.evaluation')
            self.assertEqual(claims['plugin_id'], claims['aud'])
            self.assertEqual(claims['body_sha256'], hashlib.sha256(request.data).hexdigest())
            self.assertEqual(claims['target'], '/v0/contributions/ingestion/embed_query')
            self.assertEqual(claims['method'], 'POST')
            self.assertEqual(claims['contribution'], 'ingestion')
            attempts.append(json.loads(request.data)['invocation_id'])
        self.assertEqual(len(set(attempts)), 2)
        candidates = [{'id': str(n), 'vector': vector} for n in reversed(range(12))]
        ranking = parity.CandidateRanking(candidates)
        self.assertEqual(ranking.top10(vector, float('inf')),
                         ['0', '1', '10', '11', '2', '3', '4', '5', '6', '7'])

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
        for case in ('pass', 'cosine', 'rank', 'latency', 'insufficient', 'no_candidates', 'invalid_candidates'):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                source = pathlib.Path(tmp) / 'input.json'
                output = pathlib.Path(tmp) / 'result.json'
                count = 199 if case == 'insufficient' else 200
                queries = [{'id': str(n), 'text': 'A library opens. ' + str(n)} for n in range(count)]
                selected = candidates
                if case == 'no_candidates':
                    selected = []
                elif case == 'invalid_candidates':
                    selected = [{'id': item['id'], 'vector': vector(1e-300, 0)} for item in candidates]
                source.write_text(json.dumps({'queries': queries, 'candidates': selected}))
                def encode(url, text, **kwargs):
                    if url == 'http://local/v1':
                        if case == 'cosine':
                            drift = vector(1, 0)
                            drift[2] = 0.1  # Cosine changes; candidate rank stays identical.
                            return drift, 10.0
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
                    if case == 'cosine':
                        self.assertLess(result['minimum_cosine'], 0.999)
                        self.assertEqual(result['top10_mismatches'], 0)
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
