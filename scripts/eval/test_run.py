"""Timing summaries: the numbers the query-vector cache decision reads (THE-873)."""
import contextlib
import io
import importlib.util
import json
import pathlib
import tempfile
import types
import unittest
from unittest import mock

import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from api_contract import check_response, checked_fake

import jev
import run
import scoring
import trec


def timing(encoding, elapsed):
    return {'client_ms': elapsed + 2.0, 'elapsed_ms': elapsed, 'query_encoding_ms': encoding, 'index_query_ms': 1, 'hydration_ms': 1, 'plugin_rounds_ms': 1}


class TimeSummary(unittest.TestCase):
    def test_encoding_share_is_over_the_searches_that_report_phases(self):
        older = {'client_ms': 500.0}  # an installation that reports no usage
        s = run.time_summary([timing(10, 40), timing(30, 60), older], failures=2)
        self.assertEqual(s['query_encoding_share'], 0.4)
        self.assertEqual(s['query_encoding_ms'], {'p50': 10, 'p95': 30})
        self.assertEqual(s['client_ms'], {'p50': 62.0, 'p95': 500.0})
        self.assertEqual((s['searches'], s['failures']), (3, 2))

    def test_no_phases_reported_leaves_the_share_unknown(self):
        s = run.time_summary([{'client_ms': 5.0}], failures=0)
        self.assertIsNone(s['query_encoding_share'])
        self.assertEqual(s['hydration_ms'], {'p50': None, 'p95': None})


class EvaluationSelection(unittest.TestCase):
    def test_evaluation_search_contract_selects_owner_and_collapses_offsets(self):
        class Client:
            def __init__(self):
                self.requests = []

            def call(self, method, path, body=None, **kwargs):
                self.requests.append((method, path, body))
                return 200, {'items': [
                    {'record_id': 'r1', 'excerpt': {'start': 0}},
                    {'record_id': 'r1', 'excerpt': {'start': 12}},
                    {'record_id': 'r2', 'excerpt': {'start': 0}},
                ], 'usage': {}}

        client = Client()
        client.evaluation = {'plugin': 'plugin.eval', 'space': 'model@1'}
        ranked, _, error = run.search(client, 'corpus', 'query', 'lexical', 'default',
                                      {'r1': 'doc-1', 'r2': 'doc-2'})

        self.assertIsNone(error)
        self.assertEqual(ranked, ['doc-1', 'doc-2'])
        self.assertEqual(client.requests, [
            ('POST', '/v0/search', {'query': 'query', 'corpus_ids': ['corpus'], 'mode': 'lexical',
                                    'profile': 'default', 'limit': 50,
                                    'evaluation_plugin': 'plugin.eval', 'evaluation_space': 'model@1'})
        ])

    def test_evaluation_cut_reuses_corpus_and_marks_only_selected_default_searches(self):
        class Client(run.Client):
            def __init__(self):
                super().__init__('http://127.0.0.1', 'test-key')
                self.requests = []
                self.corpora = 0

            @checked_fake
            def call(self, method, path, body=None, **kwargs):
                self.requests.append((method, path, body))
                if path == '/v0/corpora':
                    self.corpora += 1
                    return 201, {'corpus_id': 'corpus', 'name': 'Evaluation', 'effective_retrieval': {}}
                if path == '/v0/corpora/corpus/vector-spaces':
                    return 200, {'projection_generation_id': 'generation', 'segments': 1, 'items': [{'name': 'model', 'vector_space_id': 'model@1', 'version': '1',
                                           'owner': {'kind': 'plugin', 'plugin_id': 'plugin.eval', 'plugin_version': '1'},
                                           'model': 'model', 'dimensions': 2, 'metric': 'cosine', 'indexes': ['text'],
                                           'query_modalities': ['text'], 'role': 'evaluation',
                                           'coverage': {'segments': 1, 'versions_covered': 1}}]}
                if path == '/v0/search/profiles':
                    return 200, {'items': [{'name': 'default', 'full_name': 'core.retrieve/default', 'aliases': ['default'],
                                            'provider': {'kind': 'plugin', 'plugin_id': 'core.retrieve', 'plugin_version': '0.1.0'}}]}
                return 200, {'retrieval_profile': {'name': 'default', 'version': 'engine/default'},
                             'items': [{'record_id': 'r1', 'version_id': 'v1', 'part_key': 'body', 'segment_id': 's1',
                                        'segmentation_id': 'sg1', 'projection_generation_id': 'pg1', 'rank': 1,
                                        'excerpt': {'text': 'a record', 'start': 0, 'end': 8,
                                                    'coordinate_system': 'unicode_codepoint'},
                                        'availability': {'state': 'retrieval_ready', 'is_current': True, 'searchable': True}},
                                       {'record_id': 'r1', 'version_id': 'v1', 'part_key': 'body', 'segment_id': 's2',
                                        'segmentation_id': 'sg1', 'projection_generation_id': 'pg1', 'rank': 2,
                                        'excerpt': {'text': 'record', 'start': 15, 'end': 21,
                                                    'coordinate_system': 'unicode_codepoint'},
                                        'availability': {'state': 'retrieval_ready', 'is_current': True, 'searchable': True}}],
                             'usage': {'rounds': 1, 'elapsed_ms': 0, 'paid_calls': 0, 'cost_cents': 0}}

        with tempfile.TemporaryDirectory() as directory:
            directory = pathlib.Path(directory)
            trec.write(directory, {'d1': {'title': '', 'text': 'a record. More record content'}}, {'q1': 'find a record'}, {'q1': {'d1': 1}})
            client = Client()
            options = types.SimpleNamespace(ingest_timeout=1, stall=1, paid_calls=0, live_paid=False,
                                            evaluation={'plugin': 'plugin.eval', 'space': 'model@1'})
            with mock.patch.object(run, 'ingest', return_value=({'r1': 'd1'}, {})), \
                 mock.patch.object(scoring, 'score', return_value={'mean': {}, 'per_query': {}}), \
                 mock.patch.object(run, 'compare_within'), contextlib.redirect_stdout(io.StringIO()):
                result, = run.measure_set([client], 'tiny', directory, 'run', options)

        self.assertEqual(client.corpora, 1)
        self.assertTrue(all(value['paid_calls_per_query'] is None
                            for system, value in result['systems'].items() if '/evaluation/' in system))
        self.assertEqual(set(result['systems']), {
            'lexical/default', 'semantic/default', 'hybrid/default',
            'lexical/evaluation/plugin.eval/model@1',
            'semantic/evaluation/plugin.eval/model@1',
            'hybrid/evaluation/plugin.eval/model@1',
        })
        searches = [body for method, path, body in client.requests
                    if method == 'POST' and path == '/v0/search' and body.get('query') == 'find a record']
        self.assertTrue(searches)
        served = [body for body in searches if 'evaluation_plugin' not in body]
        evaluation = [body for body in searches if 'evaluation_plugin' in body]
        self.assertTrue(served)
        self.assertTrue(evaluation)
        self.assertTrue(all('evaluation_space' not in body for body in served))
        self.assertTrue(all(body['evaluation_plugin'] == 'plugin.eval' and body['evaluation_space'] == 'model@1'
                            for body in evaluation))
        self.assertEqual({body['mode'] for body in evaluation}, set(run.MODES))

    @unittest.skipUnless(importlib.util.find_spec('scipy'), 'measurement lane installs scipy')
    def test_evaluation_significance_uses_the_same_served_mode(self):
        values = lambda value: {key: {'q1': value, 'q2': value} for key in scoring.METRICS}
        result = {'systems': {'semantic/default': {'per_query': values(.5)},
                              'hybrid/default': {'per_query': values(.25)},
                              'semantic/evaluation/example.embedding/model@1': {'per_query': values(.75)},
                              'hybrid/evaluation/example.embedding/model@1': {'per_query': values(.5)}}}
        run.compare_within(result)
        for key, comparison in result['against_served_mode'].items():
            for metric in scoring.METRICS:
                self.assertEqual(comparison[metric]['queries'], 2)
                self.assertEqual(comparison[metric]['delta'], .25)
        # Historical within-run baseline remains hybrid/default.
        self.assertEqual(result['against_baseline_system']['semantic/evaluation/example.embedding/model@1']['mrr@10']['delta'], .5)

    def test_paid_admission_is_run_wide_and_missing_telemetry_stays_reserved(self):
        class Response:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *args):
                pass

            def read(self):
                body = {'items': [], 'retrieval_profile': {'name': 'deep', 'version': 'v1'},
                        'usage': {'rounds': 1, 'elapsed_ms': 0, 'paid_calls': 1, 'cost_cents': 0}}
                check_response('POST', '/v0/search', self.status, body)
                return json.dumps(body).encode()

        with tempfile.TemporaryDirectory() as directory, contextlib.redirect_stdout(io.StringIO()):
            log = pathlib.Path(directory) / 'plugin.log'
            log.touch()
            budget = jev.Budget()
            clients = [run.Client('http://127.0.0.1', 'fixture-key') for _ in range(2)]
            for client in clients:
                client.budget, client.jev_log = budget, log
            count = 0
            verified = False

            def received(request, **kwargs):
                nonlocal count
                count += 1
                if count <= 3 or verified:
                    event = {'event': 'jev_rerank', 'profile': 'deep',
                             'input_tokens': 1000 if count == 1 or verified else 65536 if count == 2 else 0,
                             'estimated_tokens': 65536 if count == 2 and not verified else 0,
                             'reason': 'deadline' if count == 3 and not verified else ''}
                    with log.open('a') as stream:
                        stream.write(json.dumps(event) + '\n')
                return Response()

            with mock.patch.object(run.urllib.request, 'urlopen', side_effect=received) as transport:
                for index in range(27):
                    clients[index % 2].call('POST', '/v0/search', {'profile': 'deep'})
                for client in clients:
                    with self.assertRaisesRegex(RuntimeError, 'run input token budget exhausted'):
                        client.call('POST', '/v0/search', {'profile': 'deep'})
                self.assertEqual(transport.call_count, 27)
                clients[0].call('POST', '/v0/search', {'profile': 'default'})
            totals = budget.summary()
            self.assertEqual(totals['actual_input_tokens'], 1000)
            self.assertEqual(totals['reserved_input_tokens'], 65536 + 25 * 196608)
            self.assertLessEqual(totals['budgeted_input_tokens'], 5_000_000)
            self.assertAlmostEqual(totals['actual_cost_cents'], .0042)
            self.assertEqual(totals['blocked_searches'], 2)
            clients[0].budget = jev.Budget()
            count, verified = 0, True
            with mock.patch.object(run.urllib.request, 'urlopen', side_effect=received) as transport:
                for _ in range(150):
                    clients[0].call('POST', '/v0/search', {'profile': 'deep'})
                with self.assertRaisesRegex(RuntimeError, 'run input token budget exhausted'):
                    clients[0].call('POST', '/v0/search', {'profile': 'deep'})
                self.assertEqual(transport.call_count, 150)
            self.assertEqual(clients[0].budget.summary()['actual_input_tokens'], 150_000)

    def test_comparisons_require_matching_scored_page_sizes(self):
        import scoring
        metrics = {metric: {'q1': 0.5} for metric in scoring.METRICS}
        baseline = {'sets': {'tiny': {'manifest': {'fingerprint': 'fixture'},
                    'systems': {'hybrid/default': {'per_query': metrics}}}}}
        current = {'sets': {'tiny': {'manifest': {'fingerprint': 'fixture'},
                   'systems': {'hybrid/default': {'per_query': metrics, 'scored_limit': 10}}}}}
        with mock.patch.object(scoring, 'paired', return_value={'delta': 0}) as paired:
            run.compare_runs(current, baseline)
            self.assertEqual(current['sets']['tiny']['against_baseline_run'], {})
            paired.assert_not_called()
            baseline['sets']['tiny']['systems']['hybrid/default']['scored_limit'] = 10
            run.compare_runs(current, baseline)
            self.assertEqual(set(current['sets']['tiny']['against_baseline_run']), {'hybrid/default'})
            self.assertEqual(paired.call_count, len(scoring.METRICS))

    def test_matrix_switches_deep_configuration_after_default_and_replays_each_scored_pass_separately(self):
        class Stack:
            def __init__(self, directory):
                self.directory = directory
                self.state = {'jev_pin': {'manifest': '/plugins/jev-rerank/quivr-plugin.yaml',
                                          'configuration': {'tokenizer_path': '/tokenizer.json'}}}
                self.started = []
                self.active = {}

            def stop_processes(self):
                pass

            def save(self):
                pass

            def config(self):
                for name in ['config.json', 'worker.json']:
                    (self.directory / name).write_text(json.dumps({'plugins': [self.state['jev_pin']]}))
                jev.configure(self)

            def start_processes(self):
                self.active = json.loads((self.directory / 'config.json').read_text())['plugins'][0]['configuration']
                self.started.append(dict(self.active))

        class Client(run.Client):
            def __init__(self, stack, enabled, log):
                super().__init__('http://127.0.0.1', 'test-key')
                self.stack, self.deep_enabled, self.jev_log = stack, enabled, log
                if log is not None:
                    log.touch()
                self.searches = []
                self.corpora = 0

            @checked_fake
            def call(self, method, path, body=None, **kwargs):
                if path == '/v0/corpora':
                    self.corpora += 1
                    return 201, {'corpus_id': 'corpus', 'name': 'Example corpus', 'effective_retrieval': {}}
                if path == '/v0/search/profiles':
                    provider = {'kind': 'plugin', 'plugin_id': 'jev.rerank', 'plugin_version': '0.1.0'}
                    # The shape of SearchProfileDescription in contracts/http/v0/openapi.yaml: no version field.
                    return 200, {'items': [{'name': name, 'full_name': 'jev.rerank/' + name,
                                           'aliases': [name], 'provider': provider}
                                          for name in ['deep', 'default']]}
                config = dict(self.stack.active) if self.stack else {}
                self.searches.append({**body, 'configuration': config})
                usage = {'rounds': 1, 'elapsed_ms': 0, 'paid_calls': int(body['profile'] == 'deep'), 'cost_cents': .1 if body['profile'] == 'deep' else 0}
                if body['profile'] == 'deep':
                    with self.jev_log.open('a') as output:
                        output.write(json.dumps({'event': 'jev_rerank', 'profile': 'deep', **usage,
                                                 'input_tokens': 100, 'pairs': config.get('candidate_count', 30),
                                                 'cache_hits': 0, 'fallback': False}) + '\n')
                return 200, {'retrieval_profile': {'name': body['profile'], 'version': 'plugin:jev.rerank@0.1.0/' + body['profile']},
                             'items': [{'record_id': 'r1', 'version_id': 'v1', 'part_key': 'body', 'segment_id': 's1',
                                        'segmentation_id': 'sg1', 'projection_generation_id': 'pg1', 'rank': 1,
                                        'excerpt': {'text': 'a record', 'start': 0, 'end': 8, 'coordinate_system': 'unicode_codepoint'},
                                        'availability': {'state': 'retrieval_ready', 'is_current': True, 'searchable': True}}], 'usage': usage}

        for enabled, allow_paid in [(False, True), (True, True), (True, False)]:
            with self.subTest(enabled=enabled, allow_paid=allow_paid), tempfile.TemporaryDirectory() as directory:
                directory = pathlib.Path(directory)
                queries = {f'q{index:03d}': f'find a record {index}' for index in range(151)}
                trec.write(directory, {'d1': {'title': '', 'text': 'a record. More record content'}}, queries, {query: {'d1': 1} for query in queries})
                stack = Stack(directory)
                baseline = Client(None, False, None)
                current = Client(stack, enabled, directory / 'plugin.log' if enabled else None)
                options = types.SimpleNamespace(ingest_timeout=1, stall=1, paid_calls=0, live_paid=enabled)
                scores = []

                def capture_scores(qrels, ranking):
                    scores.append(dict(ranking))
                    return {'mean': {}, 'per_query': {}}

                with mock.patch.object(run, 'ingest', return_value=({'r1': 'd1'}, {})), \
                     mock.patch.object(scoring, 'score', side_effect=capture_scores), \
                     mock.patch.object(jev, 'restart'), \
                     mock.patch.object(run, 'compare_within'), contextlib.redirect_stdout(io.StringIO()):
                    before, result = run.measure_set([baseline, current], 'miracl-fr', directory, 'run', options, allow_paid=allow_paid)
                selected = sorted(queries)[:150] if enabled and allow_paid else sorted(queries)
                self.assertEqual((before['queries'], result['queries']), (len(selected), len(selected)))
                self.assertEqual((baseline.corpora, current.corpora), (1, 1))
                self.assertEqual(set(before['systems']), {'lexical/default', 'semantic/default', 'hybrid/default'})
                self.assertTrue(all(search['profile'] == 'default' for search in baseline.searches))
                for value in before['systems'].values():
                    self.assertEqual(value['scored_limit'], 10 if enabled and allow_paid else 50)
                for name, value in result['systems'].items():
                    if name.endswith('/default'):
                        self.assertEqual(value['scored_limit'], 10 if enabled and allow_paid else 50)
                if not enabled or not allow_paid:
                    self.assertEqual(set(result['systems']), set(before['systems']))
                    self.assertIn('TYPESAFE_API_KEY absent' if allow_paid else 'private', result['profiles']['refused']['deep'])
                    self.assertTrue(all(search['profile'] == 'default' for search in current.searches))
                    self.assertEqual(stack.started, [])
                    continue
                deep = [(name, value) for name, value in result['systems'].items() if name.startswith('hybrid/deep-')]
                self.assertEqual(len(deep), 1)
                self.assertEqual(len(stack.started), 1)
                scored_searches = [search for search in current.searches if search['query'] in queries.values()]
                first_deep = next(index for index, search in enumerate(scored_searches) if search['profile'] == 'deep')
                self.assertTrue(all(search['profile'] == 'default' for search in scored_searches[:first_deep]))
                matrix_searches = scored_searches[first_deep:]
                self.assertTrue(all(search['profile'] == 'deep' and search['mode'] == 'hybrid' and search['limit'] == 10
                                    for search in matrix_searches))
                self.assertEqual(len(matrix_searches), len(selected))
                self.assertTrue(all(search['configuration'] == stack.started[0] for search in matrix_searches))
                self.assertEqual(stack.started[0], {'tokenizer_path': '/tokenizer.json', 'candidate_count': 30,
                                                  'trim_tokens': '256', 'ranking': 'noul'})
                for name, value in deep:
                    self.assertEqual(set(value['time_by_limit']), {'10'})
                    self.assertEqual(value['scored_limit'], 10)
                    self.assertEqual(value['paid_calls_per_query'], 1)
                    self.assertEqual(set(value['accounting']), {'scored'})
                    for phase in ['scored']:
                        self.assertEqual(value['accounting'][phase]['searches'], len(selected))
                        self.assertEqual(value['accounting'][phase]['log_records'], len(selected))
                self.assertTrue(all(ranking == {query: ['d1'] for query in selected} for ranking in scores))


if __name__ == '__main__':
    unittest.main()
