"""Timing summaries: the numbers the query-vector cache decision reads (THE-873)."""
import unittest

import run


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


if __name__ == '__main__':
    unittest.main()
