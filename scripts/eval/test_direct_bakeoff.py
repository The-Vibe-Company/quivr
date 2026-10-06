"""Offline owner contracts for the developer-local direct comparison."""
import concurrent.futures
import threading
import importlib.util
import email.utils
import http.client
import io
import json
import os
import pathlib
import tempfile
import types
import unittest
import urllib.error
from unittest import mock

import embeddings
import trec

HAS_NUMPY = importlib.util.find_spec('numpy') is not None
HAS_RANX = importlib.util.find_spec('ranx') is not None
import direct_bakeoff as bakeoff


@unittest.skipUnless(HAS_NUMPY, 'local comparison needs numpy')
class Retrieval(unittest.TestCase):
    def test_cosine_best_piece_and_stable_document_ties(self):
        # b wins via its second piece; c and a tie despite different vector norms.
        ranking = bakeoff.rank(['a', 'b', 'c'], ['q'], [[1, 0]],
                               [[1, 1], [-1, 0], [4, 0], [2, 2]], [0, 1, 1, 2])
        self.assertEqual(ranking, {'q': ['b', 'a', 'c']})
        pieces, owners = bakeoff.split_documents(['abcdefghi', ''], 5, overlap=2)
        self.assertEqual(pieces, ['abcde', 'defgh', 'ghi', ''])
        self.assertEqual(owners, [0, 0, 0, 1])
        with self.assertRaises(ValueError):
            bakeoff.rank(['a'], ['q'], [[0, 0]], [[1, 0]], [0])


class Providers(unittest.TestCase):
    def test_concurrent_reads_count_only_their_own_http_time(self):
        # Own the HTTP timing boundary: overlapping reads must not subtract
        # another request's duration from excluded admission time.
        client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(100, 1), 'tiny')
        local, started, first_done = threading.local(), threading.Barrier(2), threading.Event()
        class Response(io.BytesIO):
            def read(response, *args):
                started.wait(5)
                if local.duration == 2:
                    self.assertTrue(first_done.wait(5))
                local.clock = local.duration
                return super().read(*args)
        def run(duration):
            local.clock, local.duration = 0., duration
            try:
                return client.read(None, 'query')
            finally:
                if duration == 1:
                    first_done.set()
        with mock.patch('time.monotonic', side_effect=lambda: local.clock), \
                mock.patch.object(client.opener, 'open', side_effect=lambda *a, **k: Response(b'body')), \
                concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(run, duration) for duration in (1, 2)]
            self.assertEqual([future.result(5) for future in futures], [b'body', b'body'])
        self.assertEqual(client.http_seconds, 3)
        self.assertEqual(client.admission_seconds, 0)

    def test_hosted_success_requires_exact_nonnegative_usage(self):
        # Own successful-response admission; unknown failed attempts are a
        # separate trial contract. Only provider HTTP is replaced.
        for model in ('Cohere-Embed-V5-Pro', 'text-embedding-3-large'):
            for used in (None, '1', True, -1, 1.5, 0, 1):
                with self.subTest(model=model, used=used):
                    budget = embeddings.Budget(100, 1)
                    client = bakeoff.Hosted('https://example.com', 'fixture-key', budget, 'tiny')
                    result = ({'embeddings': {'float': [[1, 0]]},
                               'meta': {'billed_units': {'input_tokens': used}}}
                              if model.startswith('Cohere') else
                              {'data': [{'index': 0, 'embedding': [1, 0]}],
                               'usage': {'prompt_tokens': used}})
                    with mock.patch.object(client.opener, 'open', return_value=io.BytesIO(json.dumps(result).encode())):
                        if type(used) is int and used >= 0:
                            self.assertEqual(client.embed(model, ['a'], 'document', dimensions=2), [[1, 0]])
                            self.assertEqual(budget.summary()['confirmed_input_tokens'], used)
                            self.assertEqual(budget.summary()['reserved_input_tokens'], 0)
                        else:
                            with self.assertRaisesRegex(RuntimeError, '^provider omitted confirmed usage; measurement rejected$'):
                                client.embed(model, ['a'], 'document', dimensions=2)
                            self.assertEqual(budget.summary()['reserved_input_tokens'], 9)
            # Missing the entire usage envelope must also fail closed.
            result = ({'embeddings': {'float': [[1, 0]]}} if model.startswith('Cohere') else
                      {'data': [{'index': 0, 'embedding': [1, 0]}]})
            client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(100, 1), 'tiny')
            with mock.patch.object(client.opener, 'open', return_value=io.BytesIO(json.dumps(result).encode())):
                with self.assertRaisesRegex(RuntimeError, 'omitted confirmed usage'):
                    client.embed(model, ['a'], 'document', dimensions=2)

    def test_configured_openai_prefixes_usage_and_response_validation(self):
        # Owns the configured serving wire contract; fake HTTP only.
        config = {'format': 'openai', 'auth': 'none', 'base_url': 'http://127.0.0.1:8080/v1',
                  'model': 'example/model', 'dimensions': 2, 'send_dimensions': False,
                  'query_prefix': 'Instruct: retrieve\nQuery:', 'document_prefix': '', 'batch_size': 2}
        budget = embeddings.Budget(1000, 1)
        client = bakeoff.OpenAI(config, budget, 'scifact', 'candidate')
        response = {'data': [{'index': 1, 'embedding': [0, 1]}, {'index': 0, 'embedding': [1, 0]}],
                    'usage': {'prompt_tokens': 10}}
        with mock.patch.object(client.opener, 'open', return_value=io.BytesIO(json.dumps(response).encode())) as opened:
            self.assertEqual(client.embed(['a', 'b'], 'query'), [[1, 0], [0, 1]])
        request = opened.call_args.args[0]
        self.assertEqual(request.full_url, 'http://127.0.0.1:8080/v1/embeddings')
        self.assertEqual(json.loads(request.data), {'model': 'example/model',
                         'input': ['Instruct: retrieve\nQuery:a', 'Instruct: retrieve\nQuery:b']})
        self.assertEqual(budget.summary()['confirmed_input_tokens'], 10)
        for bad in ([{'index': 0, 'embedding': [1, 0]}],
                    [{'index': 0, 'embedding': [1, 0]}, {'index': 0, 'embedding': [0, 1]}],
                    [{'index': 0, 'embedding': [float('nan'), 0]}, {'index': 1, 'embedding': [0, 1]}]):
            with mock.patch.object(client.opener, 'open', return_value=io.BytesIO(json.dumps({'data': bad}).encode())):
                with self.assertRaisesRegex(RuntimeError, 'invalid'):
                    client.embed(['a', 'b'], 'document')
        exhausted = bakeoff.OpenAI(config, embeddings.Budget(1, 1), 'scifact', 'candidate')
        with mock.patch.object(exhausted.opener, 'open') as opened:
            with self.assertRaises(embeddings.BudgetExceeded):
                exhausted.embed(['a'], 'query')
            opened.assert_not_called()
        for changes in ({'metric': 'dot'}, {'max_retries': 2}, {'request_timeout_ms': 4000}):
            with self.subTest(changes=changes), self.assertRaisesRegex(ValueError, 'benchmark'):
                bakeoff.OpenAI(dict(config, **changes), budget, 'scifact', 'candidate')

    def test_formats_batching_dimensions_and_response_order(self):
        for model, dimension in [('Cohere-Embed-V5-Pro', 1024), ('text-embedding-3-large', None)]:
            with self.subTest(model=model):
                client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(1000, 1), 'scifact')
                received = []
                def respond(request, timeout):
                    body = json.loads(request.data)
                    received.append((request.full_url, body))
                    count = len(body.get('texts', body.get('input', [])))
                    width = body.get('output_dimension', bakeoff.HOSTED_DIMENSIONS[model])
                    result = ({'embeddings': {'float': [[i] + [1] * (width - 1) for i in range(count)]},
                               'meta': {'billed_units': {'input_tokens': 1}}} if dimension else
                              {'data': [{'index': i, 'embedding': [i] + [1] * (width - 1)} for i in reversed(range(count))],
                               'usage': {'prompt_tokens': 1}})
                    return io.BytesIO(json.dumps(result).encode())
                with mock.patch.object(client.opener, 'open', side_effect=respond):
                    vectors = client.embed(model, ['a', 'b', 'c'], 'query', batch=2, dimensions=dimension)
                width = dimension or bakeoff.HOSTED_DIMENSIONS[model]
                self.assertEqual(vectors, [[i] + [1] * (width - 1) for i in [0, 1, 0]])
                self.assertEqual(len(received), 2)
                first = received[0]
                if dimension:
                    self.assertTrue(first[0].endswith('/providers/cohere/v2/embed'))
                    self.assertEqual(first[1]['input_type'], 'search_query')
                    self.assertEqual(first[1]['output_dimension'], 1024)
                else:
                    self.assertTrue(first[0].endswith('/openai/v1/embeddings'))
                    self.assertEqual(first[1]['input'], ['a', 'b'])
                malformed = ({'embeddings': {'float': [[1, 0]]},
                              'meta': {'billed_units': {'input_tokens': 1}}} if dimension else
                             {'data': [{'index': 0, 'embedding': [1, 0]}], 'usage': {'prompt_tokens': 1}})
                with mock.patch.object(client.opener, 'open', return_value=io.BytesIO(json.dumps(malformed).encode())):
                    with self.assertRaisesRegex(RuntimeError, 'invalid provider embeddings'):
                        client.embed(model, ['a'], 'document', dimensions=dimension)

    def test_retry_and_unknown_usage_cannot_escape_run_cap_or_leak_errors(self):
        client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(20, 1), 'scifact')
        error = urllib.error.HTTPError('https://example.com', 503, 'reflected secret', {}, io.BytesIO(b'sensitive body'))
        with mock.patch.object(client.opener, 'open', side_effect=error) as network, mock.patch.object(bakeoff.time, 'sleep'):
            with self.assertRaises(embeddings.BudgetExceeded):
                client.embed('Cohere-Embed-V5-Pro', ['abc'], 'document')
            self.assertEqual(network.call_count, 1)  # 11 reserved, another 11 refused
        self.assertEqual(client.budget.summary()['reserved_input_tokens'], 11)
        client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(100, 1), 'scifact')
        error.code = 401
        with mock.patch.object(client.opener, 'open', side_effect=error), mock.patch.object(bakeoff.time, 'sleep'):
            with self.assertRaisesRegex(RuntimeError, '^provider HTTP 401$'):
                client.embed('Cohere-Embed-V5-Pro', ['abc'], 'document')
        self.assertEqual(client.budget.summary()['admitted_calls'], 1)
        self.assertEqual(client.budget.summary()['reserved_input_tokens'], 11)

    def test_retry_after_jitter_and_exhaustion_use_bounded_safe_errors(self):
        # Both opening and reading the response can fail. Keep private bytes
        # inside the fake transport while real retry/admission owns accounting.
        def transport(boundary, failure, recover):
            attempts = 0
            class FailedRead(io.BytesIO):
                def read(self, *args):
                    raise failure
            def open_response(*args, **kwargs):
                nonlocal attempts
                attempts += 1
                if recover and attempts > 1:
                    return io.BytesIO(b'{"embeddings":{"float":[[1,0]]},"meta":{"billed_units":{"input_tokens":1}}}')
                if boundary == 'read':
                    return FailedRead()
                raise failure
            return open_response

        cases = [
            ('open', 429, '3', 3.5, True, 0),
            ('open', 503, email.utils.formatdate(1_000_004, usegmt=True), 4.5, True, 9),
            ('open', 429, 'invalid', 1.5, True, 0),
            ('open', 429, '-3', 1.5, True, 0),
            ('open', 429, '9' * 400, 60, True, 0),
            ('open', 429, '', None, False, 0),
            ('open', 503, '', None, False, 72),
            ('open', urllib.error.URLError('private transport details'), '', None, False, 72),
        ]
        for error in (http.client.IncompleteRead(b'private body', 10), ConnectionResetError('private details')):
            cases.extend(('read', error, '', None, recover, 9 if recover else 72) for recover in (False, True))
        for boundary, error, header, delay, recover, reserved in cases:
            with self.subTest(boundary=boundary, error=error if type(error) is int else type(error).__name__,
                              header=header, recover=recover):
                client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(1000, 1), 'tiny')
                failure = (urllib.error.HTTPError('https://example.com', error, 'private text',
                           {'Retry-After': header}, io.BytesIO(b'private body')) if type(error) is int else error)
                with mock.patch.object(client.opener, 'open', side_effect=transport(boundary, failure, recover)) as network, \
                        mock.patch.object(bakeoff.time, 'sleep') as sleep, \
                        mock.patch.object(bakeoff.time, 'time', return_value=1_000_000), \
                        mock.patch.object(bakeoff.random, 'uniform', return_value=.5):
                    if recover:
                        self.assertEqual(client.embed('Cohere-Embed-V5-Pro', ['a'], 'document', dimensions=2), [[1, 0]])
                    else:
                        message = f'provider HTTP {error}' if type(error) is int else 'provider transport failed after 8 attempts'
                        with self.assertRaisesRegex(RuntimeError, '^' + message + '$'):
                            client.embed('Cohere-Embed-V5-Pro', ['a'], 'document', dimensions=2)
                self.assertEqual(network.call_count, 2 if recover else 8)
                self.assertEqual(sleep.call_count, 1 if recover else 7)
                if delay is not None:
                    sleep.assert_called_once_with(delay)
                usage = client.budget.summary()
                self.assertEqual(usage['admitted_calls'], 2 if recover else 8)
                self.assertEqual(usage['reserved_input_tokens'], reserved)
                self.assertEqual(usage['confirmed_input_tokens'], 1 if recover else 0)

    def test_document_admission_reduces_after_429_and_recovers_slowly(self):
        # Own request-level pacing at the transport boundary. Blocking fake
        # responses expose admission without wall-clock sleeps or polling.
        client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(10000, 1), 'tiny')
        failure = urllib.error.HTTPError('https://example.com', 429, 'limited', {}, io.BytesIO())
        def success(*args, **kwargs):
            return io.BytesIO(b'{"embeddings":{"float":[[1,0]]},"meta":{"billed_units":{"input_tokens":1}}}')
        with mock.patch.object(client.opener, 'open', side_effect=[failure, success()]), mock.patch.object(bakeoff.time, 'sleep'):
            client.embed('Cohere-Embed-V5-Pro', ['a'], 'document', dimensions=2)
        # After one rejection, only two of four workers may enter transport.
        admitted, release, lock = threading.Event(), threading.Event(), threading.Lock()
        active = maximum = entered = 0
        def held(*args, **kwargs):
            nonlocal active, maximum, entered
            with lock:
                active += 1
                entered += 1
                maximum = max(maximum, active)
                if entered == 2:
                    admitted.set()
            if not release.wait(timeout=5):
                raise AssertionError('test transport was not released')
            with lock:
                active -= 1
            return success()
        with mock.patch.object(client.opener, 'open', side_effect=held), concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            futures = [pool.submit(client.embed, 'Cohere-Embed-V5-Pro', ['a'], 'document', dimensions=2) for _ in range(4)]
            try:
                self.assertTrue(admitted.wait(timeout=5))
                with lock:
                    self.assertEqual(entered, 2)
            finally:
                release.set()
            for future in futures:
                self.assertEqual(future.result(), [[1, 0]])
        self.assertLessEqual(maximum, 2)
        # A brief clean stretch must not restore the original burst size.
        with mock.patch.object(client.opener, 'open', side_effect=success):
            for _ in range(40):
                client.embed('Cohere-Embed-V5-Pro', ['a'], 'document', dimensions=2)
        # Recovery becomes observable by admitting at least three held calls.
        entered = active = maximum = 0
        release.clear()
        admitted.clear()
        def recovered(*args, **kwargs):
            nonlocal entered
            with lock:
                entered += 1
                if entered == 3:
                    admitted.set()
            if not release.wait(timeout=5):
                raise AssertionError('recovery transport was not released')
            return success()
        with mock.patch.object(client.opener, 'open', side_effect=recovered), concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
            futures = [pool.submit(client.embed, 'Cohere-Embed-V5-Pro', ['a'], 'document', dimensions=2) for _ in range(3)]
            try:
                self.assertTrue(admitted.wait(timeout=5))
            finally:
                release.set()
            for future in futures:
                future.result()

    def test_e5_applies_query_and_passage_prefixes_without_download(self):
        encoder = mock.Mock()
        with mock.patch.dict('sys.modules', {'sentence_transformers': types.SimpleNamespace(SentenceTransformer=mock.Mock(return_value=encoder))}):
            local = bakeoff.E5()
            local.embed(['question'], 'query')
            local.embed(['article'], 'document')
        self.assertEqual(encoder.encode.call_args_list[0].args[0], ['query: question'])
        self.assertEqual(encoder.encode.call_args_list[1].args[0], ['passage: article'])


@unittest.skipUnless(HAS_NUMPY and HAS_RANX, 'local comparison needs numpy and ranx')
class Run(unittest.TestCase):
    def test_configured_candidate_uses_real_scoring_and_records_query_latency(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            trec.write(root / 'set', {'a': {'text': 'relevant'}, 'b': {'text': 'other'}},
                       {'q': 'question'}, {'q': {'a': 1}})
            (root / 'set' / 'manifest.json').write_text('{"sample":{"seed":775}}')
            config = {'format': 'openai', 'auth': 'none', 'base_url': 'http://localhost:8080/v1',
                      'model': 'example/model', 'dimensions': 2, 'send_dimensions': False,
                      'query_prefix': 'query: ', 'document_prefix': ''}
            (root / 'config.json').write_text(json.dumps(config))
            documents = {'data': [{'index': 0, 'embedding': [1, 0]}, {'index': 1, 'embedding': [0, 1]}],
                         'usage': {'prompt_tokens': 5}}
            query = {'data': [{'index': 0, 'embedding': [1, 0]}], 'usage': {'prompt_tokens': 3}}
            with mock.patch.dict(os.environ, {'CI': '', 'GITHUB_ACTIONS': ''}), \
                 mock.patch.object(bakeoff.public_sets, 'prepare', return_value=root / 'set'), \
                 mock.patch.object(bakeoff.E5, 'embed', side_effect=[[[1, 0], [0, 1]], [[1, 0]]]), \
                 mock.patch('urllib.request.OpenerDirector.open',
                            side_effect=[io.BytesIO(json.dumps(v).encode()) for v in (documents, query)]):
                code = bakeoff.main(['--set', 'scifact', '--openai-config', 'candidate=' + str(root / 'config.json'),
                                     '--max-input-tokens', '1000', '--max-usd', '1', '--query-latency',
                                     '--out', str(root / 'out.json')])
            report = json.loads((root / 'out.json').read_text())
            self.assertEqual(code, 0)
            self.assertEqual(report['settings']['openai_configs']['candidate'], config)
            self.assertEqual(report['results']['candidate']['per_query']['ndcg@10'], {'q': 1})
            self.assertEqual(report['by_model']['candidate']['confirmed_input_tokens'], 8)
            self.assertEqual(report['results']['candidate']['latency_ms']['samples'], 1)
            self.assertGreaterEqual(report['results']['candidate']['latency_ms']['p95'], 0)

    def test_reference_import_failures_report_module_and_wrapped_cause(self):
        # Child JSON owns E5 failures: stdout/stderr are discarded by Modal.
        missing = ModuleNotFoundError('private dependency detail', name='sentencepiece')
        wrapped = ModuleNotFoundError("Could not import module 'PreTrainedModel'. private dependency detail")
        wrapped.__cause__ = RuntimeError('private dependency detail')
        for error, diagnostic in ((missing, {'missing_module': 'sentencepiece'}),
                                  (wrapped, {'import_target': 'PreTrainedModel', 'cause_error_type': 'RuntimeError'})):
            with self.subTest(error=diagnostic), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                trec.write(root / 'set', {'a': {'text': 'relevant'}}, {'q': 'question'}, {'q': {'a': 1}})
                (root / 'set' / 'manifest.json').write_text('{"sample":{"seed":775}}')
                with mock.patch.dict(os.environ, {'CI': '', 'GITHUB_ACTIONS': ''}), \
                     mock.patch.object(bakeoff.public_sets, 'prepare', return_value=root / 'set'), \
                     mock.patch.object(bakeoff.E5, 'embed', side_effect=error):
                    code = bakeoff.main(['--set', 'scifact', '--models', bakeoff.BASELINE,
                        '--max-input-tokens', '1000', '--max-usd', '1', '--out', str(root / 'out.json')])
                report = json.loads((root / 'out.json').read_text())
                self.assertEqual(code, 2)
                self.assertEqual(report['status'], 'failed')
                self.assertEqual(report['reason'], {'kind': 'provider_error',
                    'error_type': 'ModuleNotFoundError', **diagnostic})
                self.assertNotIn('private dependency detail', json.dumps(report))

    def test_cached_reference_is_reused_and_stale_samples_are_refused(self):
        # Owns reference provenance and paired scores, using real scoring.
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            trec.write(root / 'set', {'a': {'text': 'relevant'}, 'b': {'text': 'other'}},
                       {'q': 'question'}, {'q': {'a': 1}})
            (root / 'set' / 'manifest.json').write_text('{"sample":{"seed":775}}')
            common = ['--set', 'scifact', '--max-input-tokens', '1000', '--max-usd', '1']
            with mock.patch.dict(os.environ, {'CI': '', 'GITHUB_ACTIONS': ''}), \
                 mock.patch.object(bakeoff.public_sets, 'prepare', return_value=root / 'set'), \
                 mock.patch.object(bakeoff.E5, 'embed', side_effect=[[[1, 0], [0, 1]], [[1, 0]]]):
                self.assertEqual(bakeoff.main(common + ['--models', bakeoff.BASELINE,
                    '--out', str(root / 'reference.json')]), 0)
            config = {'format': 'openai', 'auth': 'none', 'base_url': 'http://localhost:8080/v1',
                      'model': 'example/model', 'dimensions': 2, 'send_dimensions': False}
            (root / 'config.json').write_text(json.dumps(config))
            for stale in (False, True):
                if stale:
                    reference = json.loads((root / 'reference.json').read_text())
                    reference['fingerprint'] = 'different sample'
                    (root / 'reference.json').write_text(json.dumps(reference))
                out = root / ('stale.json' if stale else 'candidate.json')
                with mock.patch.dict(os.environ, {'CI': '', 'GITHUB_ACTIONS': ''}), \
                     mock.patch.object(bakeoff.public_sets, 'prepare', return_value=root / 'set'), \
                     mock.patch.object(bakeoff.E5, 'embed', side_effect=AssertionError('reference recomputed')), \
                     mock.patch.object(bakeoff.OpenAI, 'embed', side_effect=[[[1, 0], [0, 1]], [[1, 0]]]):
                    code = bakeoff.main(common + ['--openai-config', 'candidate=' + str(root / 'config.json'),
                        '--e5-reference', str(root / 'reference.json'), '--out', str(out)])
                report = json.loads(out.read_text())
                self.assertEqual(code, 2 if stale else 0)
                if stale:
                    self.assertEqual(report['reason']['kind'], 'reference_mismatch')
                    self.assertEqual(report['results'], {})
                else:
                    self.assertEqual(report['results']['candidate']['vs_current']['ndcg@10']['delta'], 0)
                    self.assertEqual(report['results'][bakeoff.BASELINE]['mean']['ndcg@10'], 1)

    def test_completed_scores_and_capped_partial_evidence_survive(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            trec.write(root / 'set', {'a': {'title': 'title', 'text': 'body'}, 'b': {'text': 'other'}},
                       {'q': 'question'}, {'q': {'a': 1}})
            (root / 'set' / 'manifest.json').write_text('{"sample":{"seed":775}}')
            for capped in (False, True):
                output = root / ('capped.json' if capped else 'complete.json')
                def hosted(*args, **kwargs):
                    if capped:
                        raise embeddings.BudgetExceeded('run embedding input-token or USD budget exhausted')
                    return [[1, 0], [0, 1]] if args[2] == 'document' else [[1, 0]]
                with mock.patch.dict(os.environ, {'AZURE_FOUNDRY_ENDPOINT': 'https://example.com', 'AZURE_FOUNDRY_KEY': 'fixture-key', 'CI': '', 'GITHUB_ACTIONS': ''}), \
                     mock.patch.object(bakeoff.public_sets, 'prepare', return_value=root / 'set'), \
                     mock.patch.object(bakeoff.E5, 'embed', side_effect=[[[1, 0], [0, 1]], [[1, 0]]]), \
                     mock.patch.object(bakeoff.Hosted, 'embed', side_effect=hosted):
                    code = bakeoff.main(['--set', 'scifact', '--models', 'Cohere-Embed-V5-Pro-1024',
                                         '--max-input-tokens', '1000', '--max-usd', '1', '--out', str(output)])
                report = json.loads(output.read_text())
                self.assertEqual(code, 2 if capped else 0)
                self.assertEqual(report['status'], 'capped' if capped else 'complete')
                self.assertEqual(report['fingerprint'], trec.fingerprint(root / 'set'))
                self.assertEqual(report['results'][bakeoff.BASELINE]['mean']['ndcg@10'], 1)
                self.assertEqual(report['results'][bakeoff.BASELINE]['per_query']['ndcg@10'], {'q': 1.0})
                self.assertNotIn('fixture-key', output.read_text())
                self.assertEqual(len(report['source']['git_sha']), 40)
                self.assertTrue(report['source']['plugin_digest'].startswith('sha256:'))
                if not capped:
                    self.assertEqual(report['results']['Cohere-Embed-V5-Pro-1024']['vs_current']['ndcg@10']['delta'], 0)
                with self.assertRaises(FileExistsError):
                    bakeoff.main(['--set', 'scifact', '--max-input-tokens', '100', '--max-usd', '1', '--out', str(output)])


if __name__ == '__main__':
    unittest.main()
