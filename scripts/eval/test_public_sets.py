"""Seeded sampling of the public sets: reproducible, complete for the chosen queries, within limits."""
import unittest
import json
import pathlib
import tempfile
from unittest import mock

import trec

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


class Preparation(unittest.TestCase):
    def test_source_id_variants_and_tsv_grades_survive_preparation(self):
        # Fake only the parquet/network boundaries; real conversion, sampling and TREC load.
        for id_field, tsv in [('id', False), ('_id', False), ('_id', True)]:
            with self.subTest(id_field=id_field, tsv=tsv), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp)
                rows = {'corpus': [{id_field: 7, 'title': None, 'text': 'generic passage'},
                                   {id_field: 8, 'title': 'title', 'text': 'distractor'}],
                        'queries': [{id_field: 3, 'text': 'What is a passage?'},
                                    {id_field: 4, 'text': 'An unjudged query'}],
                        'qrels': [{'query-id': 3, 'corpus-id': 7, 'score': 2},
                                  {'query-id': 3, 'corpus-id': 8, 'score': 0}]}
                files = {role: root / (role + ('.tsv' if role == 'qrels' and tsv else '.parquet')) for role in rows}
                files['qrels'].write_text('query-id\tcorpus-id\tscore\n3\t7\t2\n3\t8\t0\n')
                spec = {**public_sets.SETS['xpqa-fr'], 'files': {role: (str(path), '0' * 64) for role, path in files.items()},
                        'sample': {'queries': 1, 'documents': 2, 'seed': 991}}
                with mock.patch.dict(public_sets.SETS, {'xpqa-fr': spec}), \
                     mock.patch.object(trec, 'fetch', side_effect=lambda url, *args: pathlib.Path(url)), \
                     mock.patch.object(public_sets, 'read_parquet', side_effect=lambda path: rows[path.stem]):
                    directory = public_sets.prepare('xpqa-fr', root)
                    again = public_sets.prepare('xpqa-fr', root)
                    revised = {**spec, 'tier': 'restricted', 'promotion_eligible': False,
                               'licence': 'academic-only', 'licence_sources': ['https://example.test/terms']}
                    with mock.patch.dict(public_sets.SETS, {'xpqa-fr': revised}):
                        updated = public_sets.prepare('xpqa-fr', root, include_restricted=True)
                self.assertEqual(again, directory)
                loaded = trec.load(directory)
                self.assertEqual(loaded['queries'], {'3': 'What is a passage?'})
                self.assertEqual(loaded['qrels'], {'3': {'7': 2, '8': 0}})
                self.assertEqual(loaded['corpus']['7'], {'title': '', 'text': 'generic passage'})
                manifest = json.loads((directory / 'manifest.json').read_text())
                self.assertEqual(manifest['source_counts'], {'documents': 2, 'queries': 2, 'judgments': 2})
                self.assertEqual(manifest['fingerprint'], trec.fingerprint(directory))
                updated_manifest = json.loads((updated / 'manifest.json').read_text())
                self.assertEqual(updated_manifest['tier'], 'restricted')
                self.assertFalse(updated_manifest['promotion_eligible'])
                self.assertEqual(updated_manifest['licence'], 'academic-only')
                self.assertEqual(updated_manifest['licence_sources'], ['https://example.test/terms'])
                self.assertEqual(updated_manifest['fingerprint'], manifest['fingerprint'])

    def test_restricted_cached_set_still_requires_opt_in(self):
        # Admission happens before any download or cached-set reuse.
        with tempfile.TemporaryDirectory() as cache, mock.patch.object(trec, 'fetch') as fetch:
            for name in public_sets.RESTRICTED_SETS:
                with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'include-restricted'):
                    public_sets.prepare(name, cache)
            fetch.assert_not_called()
        self.assertFalse(set(public_sets.RESTRICTED_SETS) & set(public_sets.names()))
        self.assertTrue(set(public_sets.RESTRICTED_SETS) <= set(public_sets.names(True)))


if __name__ == '__main__':
    unittest.main()
