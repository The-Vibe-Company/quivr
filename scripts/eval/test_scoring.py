"""Scoring pins one nDCG convention and pairs systems per query (needs ranx; the lane installs it)."""
import importlib.util
import random
import unittest

import scoring

HAS_RANX = importlib.util.find_spec('ranx') is not None


@unittest.skipUnless(HAS_RANX, 'pip install -r scripts/eval/requirements.txt (the Search quality lane runs this)')
class Score(unittest.TestCase):
    def test_linear_gain_ndcg_recall_and_mrr_on_a_graded_fixture(self):
        qrels = {'q1': {'d1': 2, 'd2': 1, 'd9': 0}, 'q2': {'d3': 1}, 'q3': {'d4': 1}}
        ranking = {'q1': ['d2', 'd1', 'x'], 'q3': [f'x{i}' for i in range(10)] + ['d4']}  # q2: no result
        s = scoring.score(qrels, ranking)
        # q1: DCG 1/log2(2) + 2/log2(3) over IDCG 2/log2(2) + 1/log2(3) = 0.8597 with linear gain;
        # exponential (Burges) gain would give 0.7967. q2 returned nothing, q3's hit is at rank 11.
        self.assertAlmostEqual(s['per_query']['ndcg@10']['q1'], 0.8597, places=4)
        self.assertEqual(s['per_query']['ndcg@10']['q2'], 0)
        self.assertEqual(s['per_query']['recall@10']['q3'], 0)
        self.assertAlmostEqual(s['mean']['ndcg@10'], 0.8597 / 3, places=4)
        self.assertAlmostEqual(s['mean']['recall@10'], 1 / 3)
        self.assertAlmostEqual(s['mean']['mrr@10'], 1 / 3)
        # Independent oracle guards the fixed scorer's gain, cutoff, empty
        # results, unjudged hits and duplicate-document semantics.
        from ranx import Qrels, Run, evaluate
        rng = random.Random(0)
        qrels.update({f'q{i:03}': {f'd{j}': rng.randrange(4) for j in range(25)} for i in range(200)})
        ranking.update({q: rng.choices([f'd{j}' for j in range(35)], k=rng.randrange(30))
                        for q in qrels if q not in ('q1', 'q2', 'q3')})
        qrels['q-zero'], ranking['q-zero'] = {'d0': 0}, ['d0']
        run = Run({q: {d: 1 / (i + 1) for i, d in enumerate(ranking.get(q, []))} for q in qrels})
        evaluate(Qrels(qrels), run, ['ndcg@10', 'recall@10', 'mrr@10'], threads=1)
        actual = scoring.score(qrels, ranking)
        for metric in ('ndcg@10', 'recall@10', 'mrr@10'):
            for query in qrels:
                self.assertAlmostEqual(actual['per_query'][metric][query], run.scores[metric][query], places=12)

    def test_paired_t_test_over_common_queries(self):
        c = scoring.paired({'a': 0.5, 'b': 0.6, 'c': 0.9, 'only-here': 1.0}, {'a': 0.4, 'b': 0.4, 'c': 0.4})
        self.assertEqual(c['queries'], 3)
        self.assertAlmostEqual(c['delta'], 0.26667, places=4)
        self.assertAlmostEqual(c['p_value'], 0.1567, places=3)
        self.assertFalse(c['significant'])
        same = scoring.paired({'a': 0.5, 'b': 0.2}, {'a': 0.5, 'b': 0.2})
        self.assertEqual((same['delta'], same['p_value'], same['significant']), (0.0, 1.0, False))


if __name__ == '__main__':
    unittest.main()
