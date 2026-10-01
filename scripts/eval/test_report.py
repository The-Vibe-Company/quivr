"""The Markdown report: readable for a completed run, and still written for a failed one."""
import unittest

import report


def system(ndcg, paid=0):
    return {'mean': {'ndcg@10': ndcg, 'recall@10': 0.5, 'mrr@10': 0.25}, 'latency_ms': {'p50': 40.0, 'p95': 90.0, 'max': 120.0},
            'failures': 1, 'paid_calls_per_query': paid, 'per_query': {}}


def timed(encoding):
    """A time_by_limit entry: 9 searches and 1 failure; encoding None when the engine did not report its phases."""
    p = lambda p50, p95: {'p50': p50, 'p95': p95}  # noqa: E731
    return {'searches': 9, 'failures': 1, 'client_ms': p(40.0, 90.0), 'elapsed_ms': p(30.0, 70.0), 'query_encoding_ms': p(encoding, encoding),
            'index_query_ms': p(4.0, 9.0), 'hydration_ms': p(6.0, 12.0), 'plugin_rounds_ms': p(2.0, 3.0),
            'query_encoding_share': None if encoding is None else 0.4}


RUN = {'id': 'r1', 'source_revision': 'abc', 'finished_at': '2026-09-30T00:00:00+00:00', 'duration_seconds': 61, 'target': 'isolated local stack', 'host': {}}


class Markdown(unittest.TestCase):
    def test_completed_run_shows_scores_deltas_and_what_was_not_served(self):
        significant = {'queries': 2, 'delta': -0.125, 'p_value': 0.01, 'significant': True}
        r = {'status': 'completed', 'run': RUN, 'convention': 'linear gain.', 'test': 'paired t-test', 'baseline_system': 'hybrid/default', 'limit': 50,
             'baseline_run': {'id': 'r0', 'source_revision': 'old', 'github': {'GITHUB_RUN_ID': '123'}},
             'sets': {'tiny': {'manifest': {'licence': 'MIT', 'sample': {'seed': 775, 'eligible_queries': 9}}, 'queries': 2, 'documents': 5,
                               'ingestion': {'searchable_with_vectors_seconds': 3.2},
                               'profiles': {'refused': {'deep': '422 unsupported_profile'}},
                               'systems': {'hybrid/default': {**system(0.5), 'time_by_limit': {'50': timed(12.0), '10': timed(None)}},
                                           'lexical/default': system(0.375, None)},
                               'against_baseline_system': {'lexical/default': {'ndcg@10': significant, 'recall@10': significant, 'mrr@10': significant}},
                               'against_baseline_run': {'hybrid/default': {'ndcg@10': significant, 'recall@10': significant, 'mrr@10': significant}}}}}
        text = report.markdown(r)
        self.assertIn('| hybrid/default | 0.5000 | 0.5000 | 0.2500 | baseline | baseline | 40 | 90 | 1/2 | 0 |', text)
        self.assertIn('| lexical/default | 0.3750 | 0.5000 | 0.2500 | -0.1250 (p 0.010) * | -0.1250 (p 0.010) * | 40 | 90 | 1/2 | — |', text)
        self.assertIn('| hybrid/default | 50 | 40 / 90 | 30 / 70 | 12 / 12 | 4 / 9 | 6 / 12 | 2 / 3 | 40% | 1/10 |', text)
        self.assertIn('| hybrid/default | 10 | 40 / 90 | 30 / 70 | — / — | 4 / 9 | 6 / 12 | 2 / 3 | — | 1/10 |', text)
        self.assertNotIn('| lexical/default | 50 |', text)  # a system from a run before phases were timed
        self.assertIn('`deep` (422 unsupported_profile)', text)
        self.assertIn('Against the same system in run 123 (source `old`)', text)
        self.assertIn('| tiny | 2 | 5 | seed 775, 9 eligible queries | MIT | 3 |', text)

    def test_compared_run_tables_base_branch_and_change_on_one_cpu(self):
        def timed(p50, p95):
            return {**system(0.5), 'latency_ms': {'p50': p50, 'p95': p95, 'max': None}}
        same = {'queries': 2, 'delta': 0.0, 'p_value': None, 'significant': False}
        r = {'status': 'completed', 'run': {**RUN, 'source_revision': 'b' * 40, 'host': {'cpu_model': 'Example CPU 9000'}}, 'convention': None, 'test': None,
             'baseline_system': 'hybrid/default', 'limit': 50,
             'baseline_run': {'ref': 'main', 'source_revision': 'a' * 40},
             'compare': {'ref': 'main', 'source_revision': 'a' * 40, 'sets': {'tiny': {'systems': {'hybrid/default': timed(200.0, 300.0)}}}},
             'sets': {'tiny': {'manifest': {}, 'queries': 2, 'documents': 5, 'ingestion': {'searchable_with_vectors_seconds': 3.2}, 'profiles': {},
                               'systems': {'hybrid/default': timed(100.0, 330.0), 'hybrid/deep': timed(90.0, 120.0)},
                               'against_baseline_run': {'hybrid/default': {'ndcg@10': same, 'recall@10': same, 'mrr@10': same}}}}}
        text = report.markdown(r)
        self.assertIn(f"Base `main` (`{'a' * 12}`) against this checkout (`{'b' * 12}`), both on Example CPU 9000 in one job.", text)
        self.assertIn('| tiny | hybrid/default | 200 | 100 | -100 (-50%) | 300 | 330 | +30 (+10%) |', text)
        self.assertNotIn('| tiny | hybrid/deep |', text)  # the base did not serve it: nothing to compare
        self.assertIn(f"Against the same system at `main` (source `{'a' * 40}`), measured in this run:", text)

    def test_failed_run_before_any_set_names_the_error(self):
        r = {'status': 'failed', 'error': 'RuntimeError: TEI never ready', 'run': RUN, 'convention': None, 'test': None,
             'baseline_system': 'hybrid/default', 'limit': 50, 'sets': {}}
        text = report.markdown(r)
        self.assertIn('Status: **failed**', text)
        self.assertIn('Error: `RuntimeError: TEI never ready`', text)

    def test_failed_run_names_the_resource_that_ran_short(self):
        switch = 'Set READONLY, disk usage currently at 90.02%, threshold set to 90.00%'
        snapshot = {'label': 'after mldr-fr', 'docker_disk': {'used_percent': 89.6, 'free_gb': 7.5},
                    'docker_storage': {'Images': '6.1GB', 'Local Volumes': '2.3GB', 'Build Cache': '0B'},
                    'memory_available_mb': 2048, 'memory': {'weaviate-1': '1.2GiB', 'worker': '300MiB'}}
        r = {'status': 'failed', 'error': 'RuntimeError: eval-scifact: 935/2000 Records have vectors after 1018 s', 'run': RUN,
             'convention': None, 'test': None, 'baseline_system': 'hybrid/default', 'limit': 50, 'sets': {},
             'resources': {'snapshots': [snapshot], 'cause': f'Weaviate turned its shards read-only at 2026-10-01T11:20:11Z: {switch}',
                           'weaviate': {'quivr-eval-1': [{'action': 'set_shard_read_only', 'time': '2026-10-01T11:20:11Z', 'msg': switch}]}}}
        text = report.markdown(r)
        self.assertIn(f'Cause: Weaviate turned its shards read-only at 2026-10-01T11:20:11Z: {switch}.', text)
        self.assertIn('| after mldr-fr | 89.6% | 7.5 GB | 6.1GB | 2.3GB | 0B | 2048 MB | weaviate-1 1.2GiB, worker 300MiB |', text)


if __name__ == '__main__':
    unittest.main()
