"""Seeded sampling of the public sets: reproducible, complete for the chosen queries, within limits."""
import unittest

import public_sets


class Sample(unittest.TestCase):
    qrels = {f'q{i}': {f'pos{i}': 1, f'neg{i}': 0} for i in range(20)}
    qrels['q-big'] = {'huge': 1}
    sizes = {**{f'pos{i}': 100 for i in range(20)}, **{f'neg{i}': 100 for i in range(20)},
             **{f'other{i}': 100 for i in range(200)}, 'huge': 10_000, 'too-big-distractor': 10_000}

    def draw(self, seed=775, priority=None):
        return public_sets.sample(self.qrels, self.sizes, 5, 30, seed, 1000, priority)

    def test_same_seed_same_sample(self):
        self.assertEqual(self.draw(), self.draw())
        self.assertNotEqual(self.draw()[0], self.draw(seed=776)[0])

    def test_keeps_every_judgement_of_chosen_queries_and_fills_to_size(self):
        queries, docs, qrels, stats = self.draw()
        self.assertEqual(len(queries), 5)
        self.assertEqual(len(docs), 30)
        self.assertTrue({d for q in queries for d in qrels[q]} <= set(docs))
        self.assertEqual(qrels, {q: self.qrels[q] for q in queries})
        self.assertEqual(stats['judged_documents'], 10)

    def test_documents_over_the_engine_limit_are_never_used(self):
        queries, docs, _, stats = public_sets.sample(self.qrels, self.sizes, 50, 300, 775, 1000)
        self.assertNotIn('q-big', queries)  # its only positive cannot be ingested
        self.assertFalse({'huge', 'too-big-distractor'} & set(docs))
        self.assertEqual(stats['eligible_queries'], 20)

    def test_listed_negatives_come_before_random_distractors(self):
        priority = {q: [f'other{i}' for i in range(100, 110)] for q in self.qrels}
        _, docs, _, stats = self.draw(priority=priority)
        self.assertTrue({f'other{i}' for i in range(100, 110)} <= set(docs))
        self.assertEqual((stats['listed_negatives'], stats['random_distractors']), (10, 10))

    def test_registry_pins_every_download(self):
        for name, spec in public_sets.SETS.items():
            for role, (url, digest) in spec['files'].items():
                self.assertTrue(url.startswith('https://'), (name, role))
                self.assertRegex(digest, '^[0-9a-f]{64}$', (name, role))


if __name__ == '__main__':
    unittest.main()
