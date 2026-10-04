"""Campaign input contracts: paid opt-in and cumulative spend admission, offline."""
import contextlib
import io
import json
import pathlib
import tempfile
import unittest
import types
from collections import Counter

import run
import trec
from unittest import mock

import campaign


class Dispatch(unittest.TestCase):
    def test_paid_work_requires_local_execution_and_explicit_opt_in(self):
        for arguments, flags in [([], {}), (['--allow-paid'], {'CI': 'true'}),
                                 (['--allow-paid'], {'GITHUB_ACTIONS': 'true'})]:
            with self.subTest(flags=flags, arguments=arguments), \
                 mock.patch('sys.argv', ['campaign', '--max-input-tokens', '100', '--max-usd', '8'] + arguments), \
                 mock.patch.dict('os.environ', flags, clear=True), \
                 mock.patch.object(campaign, 'execute') as execute, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as refused:
                    campaign.main()
                self.assertEqual(refused.exception.code, 2)
                execute.assert_not_called()

    def test_winner_debits_prior_upper_bound_and_prepares_four_variants(self):
        with tempfile.TemporaryDirectory() as directory:
            report = pathlib.Path(directory) / 'report.json'
            prior = {'status': 'completed', 'run': {'id': 'bakeoff-run'},
                     'embedding_campaign': {'kind': 'bakeoff', 'complete': True,
                                            'candidates': [{'model': 'Cohere-Embed-V5-Pro'}],
                                            'budget': {'cost_upper_bound_usd': 8.1}}}
            report.write_text(json.dumps(prior))
            args = ['campaign', '--campaign', 'winner', '--winner', 'Cohere-Embed-V5-Pro',
                    '--prior-report', str(report), '--max-input-tokens', '2000000', '--dry-run', '--max-usd']
            with mock.patch('sys.argv', args + ['2']), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    campaign.main()
            output = io.StringIO()
            with mock.patch('sys.argv', args + ['1.9']), contextlib.redirect_stdout(output):
                campaign.main()
            plan = json.loads(output.getvalue())
            self.assertEqual(plan['prior_cost_upper_bound_usd'], 8.1)
            self.assertEqual(plan['prior_run_id'], 'bakeoff-run')
            self.assertEqual(plan['sets'], ['scifact'])
            self.assertEqual({(c['dimensions'], c['segment_tokens']) for c in plan['candidates']},
                             {(2048, 512), (2048, 2048), (1024, 512), (1024, 2048)})
            prior['status'] = 'capped'
            report.write_text(json.dumps(prior))
            with self.assertRaisesRegex(ValueError, 'completed bakeoff'):
                campaign.prior_spend(report, 'Cohere-Embed-V5-Pro')

    def test_campaign_starts_all_owners_together_and_submits_each_set_once(self):
        # Fake only engine/provider boundaries. execute, ingest, coverage waits,
        # measurement routing, partial publication and accounting remain real.
        class Engine(run.Client):
            def __init__(self):
                super().__init__('http://localhost', 'fixture')
                self.created, self.submitted, self.searches = [], [], []
                self.owners = []
            def call(self, method, path, body=None, **kwargs):
                if path == '/v0/corpora':
                    corpus = 'c' + str(len(self.created))
                    self.created.append(corpus)
                    return 201, {'corpus_id': corpus}
                if path == '/v0/records/batch':
                    self.submitted.extend(body['items'])
                    return 200, {'items': [{'receipt': {'receipt_id': 'receipt'}}]}
                if path.startswith('/v0/changes'):
                    return 200, {'next_cursor': 'cursor', 'has_more': False,
                                 'items': [{'type': 'record.enrichment_available', 'resource': {'id': 'r1'}}]}
                if path.startswith('/v0/records?'):
                    return 200, {'items': [{'record_id': 'r1', 'source': {'record_key': 'd1'}}]}
                if path.endswith('/vector-spaces'):
                    return 200, {'items': [{'owner': {'plugin_id': e['plugin']}, 'vector_space_id': e['space'],
                                           'coverage': {'versions_covered': 1}} for e in self.owners]}
                if path == '/v0/search/profiles':
                    return 200, {'items': [{'name': 'default', 'provider': {'kind': 'engine'}}]}
                if path == '/v0/search':
                    self.searches.append(body)
                    return 200, {'items': [{'record_id': 'r1'}], 'usage': {}, 'retrieval_profile': {'version': 'engine/default'}}
                raise AssertionError(path)
        client = Engine()
        def start(phases, stacks, **kwargs):
            client.owners = run.evaluation_selections(kwargs['ingestion'])
            return client
        def gate(*args):
            return contextlib.nullcontext(types.SimpleNamespace(budget=args[0], label=args[-1]))
        def hosted(binary, directory, candidate, gate):
            gate.plugin = 'example.' + candidate['label']
            return contextlib.nullcontext({'plugin': gate.plugin, 'space': 'model@1'})
        with tempfile.TemporaryDirectory() as directory:
            directory = pathlib.Path(directory)
            trec.write(directory, {'d1': {'title': '', 'text': 'a record'}}, {'q1': 'find a record'}, {'q1': {'d1': 1}})
            candidates = [{'model': 'neutral', 'label': name, 'dimensions': 8, 'segment_tokens': 512,
                           'price': {'usd_per_million_tokens': 0, 'date': '2026-10-03', 'source': 'https://example.org'}}
                          for name in ['one', 'two', 'three']]
            plan = {'candidates': candidates, 'sets': ['tiny', 'other'], 'campaign_cap_usd': 10}
            report = {'status': 'failed', 'run': {'id': 'run'}, 'sets': {}, 'embedding_campaign': plan,
                      'baseline_system': 'hybrid/default'}
            options = types.SimpleNamespace(cache=directory, ingest_timeout=60, stall=60, report=report)
            budget = campaign.embeddings.Budget(100, 1)
            import scoring
            empty = {'mean': {metric: 1. for metric in scoring.METRICS}, 'per_query': {metric: {'q1': 1.} for metric in scoring.METRICS}}
            with mock.patch.object(campaign.subprocess, 'run'), \
                 mock.patch.object(run.public_sets, 'prepare', return_value=directory), \
                 mock.patch.object(campaign.embeddings, 'Gate', side_effect=gate), \
                 mock.patch.object(campaign, 'hosted', side_effect=hosted), \
                 mock.patch.object(run, 'start_stack', side_effect=start) as stack, \
                 mock.patch.object(scoring, 'score', return_value=empty), \
                 mock.patch.object(scoring, 'paired', return_value={'queries': 1, 'delta': 0, 'p_value': None, 'significant': False}), \
                 contextlib.redirect_stdout(io.StringIO()):
                campaign.execute(plan, options, 'https://example.org', 'fixture', budget, directory)
            self.assertEqual(stack.call_count, 1)
            self.assertEqual(len(stack.call_args.kwargs['ingestion']), 3)
            self.assertEqual(len(client.created), 2)
            self.assertEqual(Counter(c['source']['corpus_id'] for c in client.submitted), {'c0': 1, 'c1': 1})
            served = [s for s in client.searches if 'evaluation_plugin' not in s and s['query'] == 'find a record']
            # Two modes, one warmup + two timed pages: baseline searched once per set.
            self.assertEqual(len(served), 12)
            self.assertEqual(len(report['sets']), 6)
            for result in report['sets'].values():
                self.assertEqual(result['status'], 'completed')
                self.assertIn('owner_vectors_seconds', result['ingestion'])
                self.assertEqual(len(result['systems']), 4)
                self.assertEqual(len(result['against_served_mode']), 2)
            persisted = json.loads((directory / 'report.json').read_text())
            self.assertEqual(len(persisted['sets']), 6)

    def test_split_dispatch_carries_scores_and_cost_without_allowing_an_early_winner(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = pathlib.Path(directory)
            first = directory / 'first.json'
            variants = campaign.candidates('bakeoff')
            cut = {'manifest': {}, 'queries': 1, 'documents': 1, 'systems': {}, 'status': 'completed',
                   'ingestion': {'owner_vectors_seconds': 1}, 'embedding_model': variants[0], 'embedding_set': 'miracl-fr',
                   'embedding_indexing': {'confirmed_input_tokens': 10, 'cost_upper_bound_usd': .25},
                   'embedding_total': {'confirmed_input_tokens': 10, 'cost_upper_bound_usd': .25}}
            prior = {'status': 'completed', 'run': {'id': 'earlier'},
                     'sets': {variants[0]['label'] + '/miracl-fr': cut},
                     'embedding_campaign': {'kind': 'bakeoff', 'candidates': variants,
                                            'sets': ['miracl-fr'], 'complete': False,
                                            'prior_cost_upper_bound_usd': .03,
                                            'budget': {'cost_upper_bound_usd': .25}}}
            first.write_text(json.dumps(prior))
            self.assertEqual(campaign.prior_spend(first), (.28, 'earlier'))
            with self.assertRaisesRegex(ValueError, 'completed bakeoff'):
                campaign.prior_spend(first, variants[0]['model'])
            args = ['campaign', '--sets', 'mldr-fr', '--prior-report', str(first),
                    '--max-input-tokens', '100', '--max-usd', '1', '--allow-paid', '--out', str(directory / 'out')]
            with mock.patch('sys.argv', args), \
                 mock.patch.dict('os.environ', {'CI': '', 'GITHUB_ACTIONS': '',
                                               'AZURE_FOUNDRY_KEY': 'fixture', 'AZURE_FOUNDRY_ENDPOINT': 'https://example.org'}), \
                 mock.patch.object(campaign, 'execute'), contextlib.redirect_stdout(io.StringIO()):
                campaign.main()
            actual = json.loads((directory / 'out/report.json').read_text())
            carried = actual['sets'][variants[0]['label'] + '/miracl-fr']
            self.assertEqual(carried['embedding_indexing'], cut['embedding_indexing'])
            self.assertEqual(carried['embedding_total'], cut['embedding_total'])
            self.assertEqual(carried['embedding_dispatch_id'], 'earlier')
            self.assertEqual(actual['embedding_campaign']['prior_cost_upper_bound_usd'], .28)
            self.assertFalse(actual['embedding_campaign']['complete'])
            # A failed dispatch still contributes its full conservative spend.
            prior['status'] = 'failed'
            first.write_text(json.dumps(prior))
            self.assertEqual(campaign.prior_spend(first), (.28, 'earlier'))

    def test_final_report_refreshes_accounting_after_inflight_settlement(self):
        budget = campaign.embeddings.Budget(20, 1)
        call = budget.reserve('neutral-d8-s512', 'tiny', 'indexing', 20, .1)
        candidate = {'model': 'neutral', 'label': 'neutral-d8-s512', 'dimensions': 8,
                     'segment_tokens': 512, 'price': {'usd_per_million_tokens': .1,
                                                    'date': '2026-10-03', 'source': 'https://example.org/pricing'}}
        partial = {'manifest': {}, 'queries': 1, 'documents': 1, 'systems': {}, 'status': 'indexing',
                   'ingestion': {'partial_seconds': 1}, 'embedding_model': candidate, 'embedding_set': 'tiny',
                   'embedding_indexing': budget.summary('neutral-d8-s512', 'tiny', 'indexing')}
        value = {'status': 'capped', 'run': {}, 'baseline_system': 'hybrid/default',
                 'sets': {'neutral/tiny': partial},
                 'embedding_campaign': {'candidates': [candidate], 'campaign_cap_usd': 10}}
        # An earlier snapshot precedes a provider response arriving during shutdown.
        budget.settle(call, 10)
        with tempfile.TemporaryDirectory() as directory:
            campaign.write_report(value, budget, pathlib.Path(directory))
            actual = json.loads((pathlib.Path(directory) / 'report.json').read_text())
        index = actual['sets']['neutral/tiny']['embedding_indexing']
        self.assertEqual(index['confirmed_input_tokens'], 10)
        self.assertEqual(index['reserved_input_tokens'], 0)
        self.assertEqual(actual['sets']['neutral/tiny']['embedding_total']['confirmed_input_tokens'], 10)


if __name__ == '__main__':
    unittest.main()
