"""Unit checks for the retrieval measurement protocol helpers."""
import hashlib
import json
import pathlib
import tempfile
import unittest

import measure_metrics as m


class Percentiles(unittest.TestCase):
    def test_nearest_rank(self):
        values = [float(v) for v in range(1, 101)]
        self.assertEqual(m.percentile(values, 50), 50.0)
        self.assertEqual(m.percentile(values, 95), 95.0)
        self.assertEqual(m.percentile(values, 100), 100.0)

    def test_single_and_unsorted(self):
        self.assertEqual(m.percentile([7.0], 95), 7.0)
        self.assertEqual(m.percentile([30.0, 10.0, 20.0], 50), 20.0)

    def test_empty_is_none(self):
        self.assertIsNone(m.percentile([], 95))


class Quality(unittest.TestCase):
    def test_rank_metrics(self):
        # ranks: 1, 2, 4, missing
        q = m.quality([1, 2, 4, 0])
        self.assertAlmostEqual(q['mrr_at_10'], (1 + 0.5 + 0.25) / 4)
        self.assertAlmostEqual(q['recall_at_3'], 2 / 4)
        self.assertAlmostEqual(q['recall_at_10'], 3 / 4)

    def test_rank_beyond_ten_counts_as_miss(self):
        self.assertEqual(m.quality([11])['mrr_at_10'], 0)

    def test_first_rank_of_record(self):
        hits = [{'record_id': 'b'}, {'record_id': 'a'}, {'record_id': 'a'}]
        self.assertEqual(m.rank_of(hits, 'a'), 2)
        self.assertEqual(m.rank_of(hits, 'z'), 0)


class Latency(unittest.TestCase):
    def test_failures_counted_not_percentiled(self):
        samples = [{'ok': True, 'ms': 10.0}, {'ok': True, 'ms': 30.0}, {'ok': False, 'ms': 5000.0, 'error': 'timeout'}]
        s = m.latency_summary(samples, 1000)
        self.assertEqual(s['samples'], 3)
        self.assertEqual(s['failures'], 1)
        self.assertEqual(s['p95_ms'], 30.0)
        self.assertFalse(s['target_met'])  # a failure never satisfies the target

    def test_target(self):
        ok = [{'ok': True, 'ms': float(v)} for v in range(1, 21)]
        self.assertTrue(m.latency_summary(ok, 1000)['target_met'])
        self.assertFalse(m.latency_summary(ok, 19)['target_met'])

    def test_no_samples_does_not_meet_target(self):
        self.assertFalse(m.latency_summary([], 1000)['target_met'])


class Schedule(unittest.TestCase):
    def test_deterministic_and_complete(self):
        a = m.schedule(['q1', 'q2', 'q3'], ['lexical', 'semantic', 'hybrid'], passes=2, seed=661)
        b = m.schedule(['q1', 'q2', 'q3'], ['lexical', 'semantic', 'hybrid'], passes=2, seed=661)
        self.assertEqual(a, b)
        self.assertEqual(len(a), 18)
        self.assertEqual(sorted(set(a)), sorted({(q, mo) for q in ['q1', 'q2', 'q3'] for mo in ['lexical', 'semantic', 'hybrid']}))
        # Modes are interleaved per query: each consecutive triple covers one query in all modes.
        for i in range(0, 18, 3):
            self.assertEqual(len({q for q, _ in a[i:i + 3]}), 1)


class Workload(unittest.TestCase):
    def test_repository_workload_matches_fixture(self):
        root = pathlib.Path(__file__).resolve().parents[1]
        workload, rows = m.load_workload(root / 'tests/measurement/workload-v2.json', root)
        self.assertEqual(len(rows), workload['fixture']['queries'])

    def test_fixture_hash_mismatch_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            (root / 'fixture.json').write_text(json.dumps([['a', 't', 'b', 'fr', 'q']]))
            (root / 'w.json').write_text(json.dumps({'fixture': {'path': 'fixture.json', 'sha256': '0' * 64, 'queries': 1}}))
            with self.assertRaisesRegex(RuntimeError, 'fixture sha256'):
                m.load_workload(root / 'w.json', root)

    def test_query_count_mismatch_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            data = json.dumps([['a', 't', 'b', 'fr', 'q']]).encode()
            (root / 'fixture.json').write_bytes(data)
            (root / 'w.json').write_text(json.dumps({'fixture': {'path': 'fixture.json', 'sha256': hashlib.sha256(data).hexdigest(), 'queries': 2}}))
            with self.assertRaisesRegex(RuntimeError, 'queries'):
                m.load_workload(root / 'w.json', root)


if __name__ == '__main__':
    unittest.main()
