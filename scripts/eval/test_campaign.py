"""Campaign input contracts: paid opt-in and cumulative spend admission, offline."""
import contextlib
import io
import json
import pathlib
import tempfile
import unittest
from unittest import mock

import campaign


class Dispatch(unittest.TestCase):
    def test_paid_work_requires_dispatch_and_explicit_opt_in(self):
        for arguments, event in [([], 'workflow_dispatch'), (['--allow-paid'], 'pull_request'),
                                 (['--allow-paid'], 'schedule')]:
            with self.subTest(event=event, arguments=arguments), \
                 mock.patch('sys.argv', ['campaign', '--max-input-tokens', '100', '--max-usd', '8'] + arguments), \
                 mock.patch.dict('os.environ', {'GITHUB_EVENT_NAME': event}, clear=True), \
                 mock.patch.object(campaign, 'execute') as execute, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as refused:
                    campaign.main()
                self.assertEqual(refused.exception.code, 2)
                execute.assert_not_called()

    def test_winner_debits_prior_upper_bound_and_prepares_four_variants(self):
        with tempfile.TemporaryDirectory() as directory:
            report = pathlib.Path(directory) / 'report.json'
            prior = {'status': 'completed', 'run': {'id': 'bakeoff-run'},
                     'embedding_campaign': {'kind': 'bakeoff',
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
