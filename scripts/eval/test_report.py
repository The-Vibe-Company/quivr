"""The Markdown report: readable for a completed run, and still written for a failed one."""
import unittest

import report


def system(ndcg, paid=0):
    return {'mean': {'ndcg@10': ndcg, 'recall@10': 0.5, 'mrr@10': 0.25}, 'latency_ms': {'p50': 40.0, 'p95': 90.0, 'max': 120.0},
            'failures': 1, 'paid_calls_per_query': paid, 'per_query': {}}


RUN = {'id': 'r1', 'source_revision': 'abc', 'finished_at': '2026-09-30T00:00:00+00:00', 'duration_seconds': 61, 'target': 'isolated local stack', 'host': {}}


class Markdown(unittest.TestCase):
    def test_completed_run_shows_scores_deltas_and_what_was_not_served(self):
        significant = {'queries': 2, 'delta': -0.125, 'p_value': 0.01, 'significant': True}
        r = {'status': 'completed', 'run': RUN, 'convention': 'linear gain.', 'test': 'paired t-test', 'baseline_system': 'hybrid/default', 'limit': 50,
             'baseline_run': {'id': 'r0', 'source_revision': 'old', 'github': {'GITHUB_RUN_ID': '123'}},
             'sets': {'tiny': {'manifest': {'licence': 'MIT', 'sample': {'seed': 775, 'eligible_queries': 9}}, 'queries': 2, 'documents': 5,
                               'ingestion': {'searchable_with_vectors_seconds': 3.2},
                               'profiles': {'refused': {'deep': '422 unsupported_profile'}},
                               'systems': {'hybrid/default': system(0.5), 'lexical/default': system(0.375, None)},
                               'against_baseline_system': {'lexical/default': {'ndcg@10': significant, 'recall@10': significant, 'mrr@10': significant}},
                               'against_baseline_run': {'hybrid/default': {'ndcg@10': significant, 'recall@10': significant, 'mrr@10': significant}}}}}
        text = report.markdown(r)
        self.assertIn('| hybrid/default | 0.5000 | 0.5000 | 0.2500 | baseline | baseline | 40 | 90 | 1/2 | 0 |', text)
        self.assertIn('| lexical/default | 0.3750 | 0.5000 | 0.2500 | -0.1250 (p 0.010) * | -0.1250 (p 0.010) * | 40 | 90 | 1/2 | — |', text)
        self.assertIn('`deep` (422 unsupported_profile)', text)
        self.assertIn('Against the same system in run 123 (source `old`)', text)
        self.assertIn('| tiny | 2 | 5 | seed 775, 9 eligible queries | MIT | 3 |', text)

    def test_failed_run_before_any_set_names_the_error(self):
        r = {'status': 'failed', 'error': 'RuntimeError: TEI never ready', 'run': RUN, 'convention': None, 'test': None,
             'baseline_system': 'hybrid/default', 'limit': 50, 'sets': {}}
        text = report.markdown(r)
        self.assertIn('Status: **failed**', text)
        self.assertIn('Error: `RuntimeError: TEI never ready`', text)


if __name__ == '__main__':
    unittest.main()
