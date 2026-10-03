"""Owner contracts for offline OSS direct-bakeoff comparisons."""
import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest

import oss_report


def _result(scores, *, latency=True, serving=None, index_s=4):
    metrics = ('ndcg@10', 'recall@10', 'mrr@10')
    means = {metric: sum(scores[metric].values()) / len(scores[metric]) for metric in metrics}
    value = {'mean': means, 'per_query': scores, 'dims': 4, 'index_s': index_s}
    if latency:
        value['latency_ms'] = {'p50': 12, 'p95': 20, 'samples': 2,
                               'scope': 'single query encoding, warm'}
    if serving is not None:
        value['serving'] = serving
    return value


def report(*, fingerprint='fixture-v1', results=None, sample=None, promotion=True, campaign=None):
    sample = sample or {'version': 'release-1', 'split': 'dev', 'tier': 'default'}
    return {'status': 'complete', 'set': 'tiny', 'fingerprint': fingerprint,
            'sample': sample, 'promotion_eligible': promotion,
            'settings': {'prices_usd_per_million': {'Cohere-Embed-V5-Fast': .08,
                                                     'Cohere-Embed-V5-Pro': .12},
                         'openai_configs': {'qwen3': {'format': 'openai', 'auth': 'none',
                                                      'base_url': 'http://127.0.0.1:8080/v1',
                                                      'model': 'Qwen/Qwen3-Embedding-0.6B',
                                                      'dimensions': 4, 'model_revision': 'q1',
                                                      'query_prefix': '', 'document_prefix': '',
                                                      'max_tokens_per_segment': 2048}}},
            'serving_campaign': campaign, 'results': results or {}}


def scores(ndcg=(.5, .7), recall=(.4, .6), mrr=(.3, .5)):
    return {'ndcg@10': dict(zip(('q1', 'q2'), ndcg)),
            'recall@10': dict(zip(('q1', 'q2'), recall)),
            'mrr@10': dict(zip(('q1', 'q2'), mrr))}


class Comparison(unittest.TestCase):
    def test_pairs_lineage_and_costs_without_provider_or_wall_clock(self):
        hosted = {
            oss_report.BASELINE: _result(scores((.4, .6), (.3, .5), (.2, .4))),
            oss_report.COHERE_FAST: _result(scores((.45, .65), (.35, .55), (.25, .45))),
            oss_report.COHERE_PRO: _result(scores((.5, .6), (.4, .6), (.3, .5))),
            'qwen3': _result(scores((.5, .7), (.4, .6), (.3, .5)),
                             serving={'hardware': 'cpu', 'image': 'image@sha256:q',
                                      'model_revision': 'q1', 'hourly_usd': .6, 'seconds': 120,
                                      'input_tokens': 200000, 'estimated_usd': None,
                                      'usd_per_million_tokens': None,
                                      'scope': 'candidate encoding and scoring; excludes baseline and preparation',
                                      'estimate_only': True})}
        second = report(results={'qwen3': _result(scores((.55, .75), (.45, .65), (.35, .55)),
                                                   serving={'hardware': 'L4', 'image': 'image@sha256:q',
                                                            'model_revision': 'q1', 'hourly_usd': .72,
                                                            'seconds': 90, 'input_tokens': 300000,
                                                            'estimated_usd': None,
                                                            'usd_per_million_tokens': None,
                                                            'scope': 'candidate encoding and scoring; excludes baseline and preparation',
                                                            'estimate_only': True})},
                       campaign={'model': 'qwen3', 'hardware': 'L4', 'elapsed_seconds': 100,
                                 'estimated_usd': .02, 'estimate_only': True,
                                 'scope': 'whole function body, including downloads, baseline and scoring; excludes image build and scheduling',
                                 'requested_sets': ['tiny'], 'completed_sets': ['tiny'], 'status': 'complete'})
        output = oss_report.build([report(results=hosted), second])
        self.assertEqual(output['lineage']['query_count'], 2)
        self.assertEqual(output['reference'], oss_report.COHERE_PRO)
        observations = {item['id']: item for item in output['observations']}
        self.assertEqual(observations['qwen3']['hardware'], 'cpu')
        self.assertEqual(observations['qwen3']['dimensions'], 4)
        self.assertEqual(observations['qwen3 (L4)']['hardware'], 'L4')
        self.assertAlmostEqual(observations['qwen3']['estimated_serving_usd'], .02)
        self.assertAlmostEqual(observations['qwen3']['usd_per_million_tokens'], .1)
        paired = output['comparisons']['qwen3']['metrics']['ndcg@10']
        self.assertEqual(paired['queries'], 2)
        self.assertAlmostEqual(paired['delta'], .05)
        if importlib.util.find_spec('scipy'):
            self.assertAlmostEqual(paired['p_value'], .5)
        else:
            self.assertIsNone(paired['p_value'])
        self.assertEqual(output['serving_campaign']['hardware'], 'L4')
        self.assertIn('not evidence of equivalence', output['recommendation'])
        self.assertFalse(output['diagnostic_only'])

    def test_reduced_hosted_pro_is_preferred_reference(self):
        value = report(results={oss_report.COHERE_PRO_1024: _result(scores())})
        output = oss_report.build([value])
        self.assertEqual(output['reference'], oss_report.COHERE_PRO_1024)
        self.assertEqual(output['observations'][0]['dimensions'], 4)

    def test_hosted_openai_does_not_count_as_an_open_source_candidate(self):
        value = report(results={'text-embedding-3-large': _result(scores())})
        output = oss_report.build([value])
        self.assertEqual(output['observations'][0]['family'], 'Hosted')
        self.assertIn('no OSS observation supplied', output['evidence_gaps'])

    def test_rejects_mixed_lineage_and_query_ids_before_pairing(self):
        base = report(results={oss_report.COHERE_PRO: _result(scores())})
        fingerprint = copy.deepcopy(base)
        fingerprint['fingerprint'] = 'other-fingerprint'
        with self.assertRaisesRegex(ValueError, 'matching set, fingerprint'):
            oss_report.build([base, fingerprint])
        queries = copy.deepcopy(base)
        for metric in oss_report.QUALITY_METRICS:
            queries['results'][oss_report.COHERE_PRO]['per_query'][metric]['q3'] = .4
        with self.assertRaisesRegex(ValueError, 'identical query IDs'):
            oss_report.build([base, queries])

    def test_restricted_or_ineligible_evidence_is_diagnostic_only(self):
        value = report(results={oss_report.COHERE_PRO: _result(scores())},
                       sample={'version': 'restricted-1', 'split': 'test', 'tier': 'restricted'},
                       promotion=True)
        output = oss_report.build([value])
        self.assertTrue(output['diagnostic_only'])
        self.assertTrue(output['observations'][0]['diagnostic_only'])
        self.assertIn('no OSS observation supplied', output['evidence_gaps'])

    def test_repeated_local_baseline_is_deduplicated_with_measurement_identity(self):
        value = report(results={oss_report.BASELINE: _result(scores())})
        output = oss_report.build([value, copy.deepcopy(value)])
        baselines = [item for item in output['observations'] if item['model'] == oss_report.BASELINE]
        self.assertEqual(len(baselines), 1)
        self.assertEqual(baselines[0]['duplicate_measurements'], 2)
        self.assertEqual(baselines[0]['measurement_sources'], [0, 1])

    def test_complete_report_required_and_writer_supports_json_and_markdown(self):
        value = report(results={oss_report.COHERE_PRO: _result(scores())})
        value['status'] = 'capped'
        with self.assertRaisesRegex(ValueError, 'not complete'):
            oss_report.build([value])
        value['status'] = 'complete'
        output = oss_report.build([value])
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            json_path = oss_report.write(output, root / 'comparison.json')
            self.assertEqual(json.loads(json_path.read_text())['status'], 'comparison')
            markdown_path = oss_report.write(output, root / 'comparison.md')
            self.assertIn('| Observation | Family |', markdown_path.read_text())
            self.assertIn('nDCG p', markdown_path.read_text())
            with self.assertRaises(FileExistsError):
                oss_report.write(output, json_path)


if __name__ == '__main__':
    unittest.main()
