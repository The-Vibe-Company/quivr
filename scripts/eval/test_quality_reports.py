"""Owner tests for aggregate dataset quality evidence."""
import json
import pathlib
import tempfile
import unittest
from unittest import mock

import quality_reports


DATA = {
    'corpus': {
        'd1': {'title': 'T', 'text': 'abcd'},
        'd2': {'title': '', 'text': 'é'},
    },
    'queries': {'q1': 'What is this?', 'q2': 'A statement.'},
    'qrels': {'q1': {'d1': 2, 'd2': 0}, 'q2': {'d2': 1}},
}
MANIFEST = {
    'name': 'tiny', 'language': 'en', 'description': 'tiny evaluation set',
    'licence': 'CC0', 'source': 'https://example.test/set', 'version': {'repo': 'abc'},
    'split': 'test', 'tier': 'default', 'promotion_eligible': True,
    'licence_checked': '2026-10-03', 'licence_sources': ['https://example.test/licence'],
    'query_type': 'source questions', 'known_issues': ['sparse judgements'],
    'source_counts': {'documents': 2, 'queries': 2, 'judgments': 3},
    'sample': {'seed': 1}, 'fingerprint': 'fp', 'files': {},
}


def run(e5=None, cohere=None, **overrides):
    results = {}
    if e5 is not None:
        results[quality_reports.BASELINE] = {
            'mean': {'ndcg@10': sum(e5) / len(e5), 'recall@10': .5, 'mrr@10': .25},
            'per_query': {'ndcg@10': dict(zip(DATA['queries'], e5))},
            'dims': 384, 'pieces': 2, 'index_s': 1.5, 'query_ms': 2.5,
        }
    if cohere is not None:
        results[quality_reports.COHERE_PRO] = {
            'mean': {'ndcg@10': sum(cohere) / len(cohere), 'recall@10': .75, 'mrr@10': .5},
            'per_query': {'ndcg@10': dict(zip(DATA['queries'], cohere))},
            'dims': 2048, 'pieces': 2, 'index_s': 2.5, 'query_ms': 3.5,
        }
    value = {'date': '2026-10-03T00:00:00+00:00', 'status': 'complete', 'set': 'tiny', 'fingerprint': 'fp',
             'settings': {'models': list(results), 'e5_model': 'intfloat/multilingual-e5-small',
                          'e5_revision': quality_reports.direct_bakeoff.E5_REVISION}, 'results': results}
    value.update(overrides)
    return value


class Inspection(unittest.TestCase):
    def test_aggregate_counts_lengths_types_saturation_and_hosted_estimate(self):
        report = quality_reports.inspect(DATA, MANIFEST, run([1.0, .5], [1.0, 1.0]))

        self.assertEqual(report['counts'], {'documents': 2, 'queries': 2, 'judgments': 3})
        self.assertEqual(report['source_counts'], MANIFEST['source_counts'])
        self.assertEqual(report['judged_depth']['histogram'], {'1': 1, '2': 1})
        self.assertEqual(report['judged_depth']['total_judgments'], 3)
        self.assertEqual(report['grades']['counts'], {'0': 1, '1': 1, '2': 1})
        self.assertEqual(report['query_lengths']['chars']['histogram'], {'12': 1, '13': 1})
        self.assertEqual(report['query_lengths']['words']['histogram'], {'2': 1, '3': 1})
        self.assertEqual(report['query_types']['counts'], {'question': 1, 'statement': 1})
        self.assertEqual(report['query_types']['source'], 'source questions')
        self.assertEqual(report['e5']['mean']['ndcg@10'], .75)
        self.assertEqual(report['e5']['mean']['recall@10'], .5)
        self.assertEqual(report['e5']['dims'], 384)
        self.assertEqual(report['cohere_pro']['mean']['mrr@10'], .5)
        self.assertEqual(report['cohere_pro']['query_ms'], 3.5)
        self.assertEqual(report['runs']['e5']['settings']['e5_revision'], quality_reports.direct_bakeoff.E5_REVISION)
        self.assertNotIn('results', report['runs']['e5'])
        self.assertEqual(report['saturation']['e5']['share'], .5)
        self.assertEqual(report['saturation']['joint_e5_cohere_pro']['share'], .5)
        # (6 + 8) + (2 + 8) document bytes, then (13 + 8) + (12 + 8) query bytes.
        self.assertEqual(report['hosted_input_estimate']['input_tokens'], 65)
        self.assertEqual(report['hosted_input_estimate']['per_set_caps'], {'max_input_tokens': 130, 'max_usd': .01})

    def test_restricted_metadata_cannot_be_promoted_and_missing_hosted_run_is_explicit(self):
        manifest = {**MANIFEST, 'tier': 'restricted', 'promotion_eligible': True}
        report = quality_reports.inspect(DATA, manifest)

        self.assertFalse(report['metadata']['promotion_eligible'])
        self.assertIsNone(report['e5']['mean'])
        self.assertIsNone(report['saturation']['joint_e5_cohere_pro']['share'])
        self.assertIn('awaiting coordinator', report['saturation']['joint_e5_cohere_pro']['reason'])


class Validation(unittest.TestCase):
    def test_supplied_runs_fail_closed_for_status_identity_query_coverage_and_scores(self):
        cases = [
            ({'status': 'running'}, 'not complete'),
            ({'set': 'other'}, 'set does not match'),
            ({'fingerprint': 'other'}, 'fingerprint does not match'),
            ({'settings': None}, 'model settings'),
            ({'settings': {'models': []}}, 'model identity'),
            ({'settings': {'models': [quality_reports.BASELINE], 'e5_model': 'other',
                          'e5_revision': quality_reports.direct_bakeoff.E5_REVISION}}, 'pinned E5'),
            ({'settings': {'models': [quality_reports.BASELINE], 'e5_model': 'intfloat/multilingual-e5-small',
                          'e5_revision': 'unverified'}}, 'pinned E5'),
            ({'results': {quality_reports.BASELINE: {'mean': {'ndcg@10': .5},
                                                       'per_query': {'ndcg@10': {'q1': .5}}}}}, 'exactly all'),
            ({'results': {quality_reports.BASELINE: {'mean': {'ndcg@10': .5},
                                                       'per_query': {'ndcg@10': {'q1': .5, 'q2': 1.1}}}}}, 'outside'),
        ]
        for change, message in cases:
            with self.subTest(message=message):
                invalid = run([.5, .5])
                invalid.update(change)
                with self.assertRaisesRegex(ValueError, message):
                    quality_reports.inspect(DATA, MANIFEST, invalid)
        hosted = run(cohere=[1, 1])
        hosted['settings']['models'] = ['Cohere-Embed-V5-Fast']
        with self.assertRaisesRegex(ValueError, 'model identity'):
            quality_reports.inspect(DATA, MANIFEST, run([1, 1]), hosted)


class Output(unittest.TestCase):
    def test_write_creates_only_exclusive_json_and_markdown_without_raw_content(self):
        report = quality_reports.inspect(DATA, MANIFEST)
        with tempfile.TemporaryDirectory() as temp:
            paths = quality_reports.write(report, temp)
            self.assertEqual({pathlib.Path(path).suffix for path in paths}, {'.json', '.md'})
            self.assertEqual(sorted(pathlib.Path(temp).iterdir()), sorted(map(pathlib.Path, paths)))
            markdown = (pathlib.Path(temp) / 'tiny.md').read_text()
            self.assertIn(f"Date: {report['date']}", markdown)
            self.assertIn('Status: evidence', markdown)
            self.assertNotIn('What is this?', markdown)
            self.assertNotIn('d1', markdown)
            with self.assertRaises(FileExistsError):
                quality_reports.write(report, temp)
            loaded = json.loads((pathlib.Path(temp) / 'tiny.json').read_text())
            self.assertEqual(loaded['set'], 'tiny')

    def test_cli_uses_the_direct_comparison_cache_by_default(self):
        with mock.patch.dict('os.environ', {'CI': '', 'GITHUB_ACTIONS': ''}), \
                mock.patch.object(quality_reports.public_sets, 'prepare', side_effect=RuntimeError('stop')) as prepare:
            with self.assertRaisesRegex(RuntimeError, 'stop'):
                quality_reports.main(['--set', 'tiny', '--out', 'out'])
        prepare.assert_called_once_with('tiny', quality_reports.public_sets.ROOT / '.scratch/eval/cache',
                                         include_restricted=False)


if __name__ == '__main__':
    unittest.main()
