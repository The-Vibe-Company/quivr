"""Four-gate owner: correction, diagnostic sets and cost/latency rejection.

Credible regressions: uncorrected significance, excluding a loss after seeing it,
or allowing a good score to override price. Existing scoring tests own only the
paired test; these exercise the verdict on complete measurement families.
"""
import hashlib
import copy
import importlib.util
import json
import unittest

import gates


class Correction(unittest.TestCase):
    def test_holm_accounts_for_the_full_family_and_keeps_adjusted_values_monotone(self):
        self.assertEqual(gates.holm({'a': .02, 'b': .03, 'c': .9}), {'a': .06, 'b': .06, 'c': .9})


@unittest.skipUnless(importlib.util.find_spec('scipy'), 'paired gates need scipy')
class Verdict(unittest.TestCase):
    def pair(self, delta=.1, **metrics):
        base = {str(i): .4 + i / 100 for i in range(20)}
        candidate = {q: v + delta + int(q) / 10000 for q, v in base.items()}
        def row(scores, price):
            return {'per_query': {'ndcg@10': scores}, 'dataset': {'name': 'tiny', 'split': 'dev', 'fingerprint': 'v1'},
                    'tier': 'direct', 'git_sha': 'a' * 40,
                    'cost': {'resource_class': 'fixed', 'latency_method': 'fresh serial',
                             'latency_sample': {'policy': 'sha256-query-id-v1; max=50', 'query_ids': sorted(scores, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q)),
                                                'warmup_query_ids': ['0']}},
                    'metrics': {'latency_p95_ms': 10, 'cost_per_search_usd': price,
                                'cost_per_1000_documents_usd': 2}}
        a, b = row(candidate, .0002), row(base, .0003)
        a['metrics'].update(metrics)
        return {'candidate': a, 'baseline': b}

    def test_gate_details_explain_each_set_and_unknown_measurements(self):
        # The gate owner exposes evidence in JSON; boolean-only tests cannot
        # catch missing fields, swapped operands, or unknown values shown as zero.
        pairs = {'gain': self.pair(latency_p95_ms=15, cost_per_search_usd=.002),
                 'loss': self.pair(-.1)}
        policy = {'sets': {'gain': {'diagnostic': False},
                           'loss': {'diagnostic': True, 'reason': 'proxy'},
                           'missing': {'diagnostic': False}}, 'profile': 'default',
                  'gates': {'min_gain': .02, 'latency_ratio': 1.4,
                            'search_usd': .001, 'index_usd': 3}}
        report = json.loads(json.dumps(gates.evaluate(pairs, policy), allow_nan=False))
        for gate in report['gates'].values():
            self.assertEqual(set(gate['details']), {'gain', 'loss', 'missing'})
        for key in ('quality', 'no_loss'):
            detail = report['gates'][key]['details']['gain']
            self.assertAlmostEqual(detail['delta'], .10095)
            self.assertGreater(detail['adjusted_p'], 0)
            self.assertLess(detail['adjusted_p'], .05)
            self.assertEqual(detail['role'], 'gate')
            self.assertEqual(detail['significance_level'], .05)
            loss = report['gates'][key]['details']['loss']
            self.assertAlmostEqual(loss['delta'], -.09905)
            self.assertEqual(loss['role'], 'diagnostic')
            self.assertIsNone(report['gates'][key]['details']['missing']['delta'])
            self.assertIsNone(report['gates'][key]['details']['missing']['adjusted_p'])
        self.assertEqual(report['gates']['quality']['details']['gain']['min_gain'], .02)
        self.assertEqual(report['gates']['latency']['details']['gain'], {
            'candidate_p95_ms': 15, 'baseline_p95_ms': 10, 'ratio': 1.5,
            'max_ratio': 1.4, 'comparable': True})
        self.assertEqual(report['gates']['price']['details']['gain'], {
            'cost_per_search_usd': .002, 'cost_per_1000_documents_usd': 2,
            'max_search_usd': .001, 'max_index_usd_per_1000_documents': 3})
        self.assertIsNone(report['gates']['price']['details']['missing']['cost_per_search_usd'])
        self.assertIsNone(report['gates']['latency']['details']['missing']['ratio'])
        self.assertFalse(report['gates']['latency']['details']['missing']['comparable'])

        for value in (None, float('nan'), float('inf'), -1):
            with self.subTest(unknown=value):
                row = self.pair(latency_p95_ms=value, cost_per_search_usd=value)
                unknown = json.loads(json.dumps(gates.evaluate({'gain': row}, policy), allow_nan=False))
                self.assertIsNone(unknown['gates']['latency']['details']['gain']['candidate_p95_ms'])
                self.assertIsNone(unknown['gates']['latency']['details']['gain']['ratio'])
                self.assertIsNone(unknown['gates']['price']['details']['gain']['cost_per_search_usd'])
        row = self.pair(latency_p95_ms=0)
        row['baseline']['metrics']['latency_p95_ms'] = 0
        zero = gates.evaluate({'gain': row}, dict(policy, sets={'gain': {'diagnostic': False}}))
        self.assertTrue(zero['gates']['latency']['passed'])
        self.assertIsNone(zero['gates']['latency']['details']['gain']['ratio'])
        row['candidate']['cost']['latency_sample'] = None
        incompatible = gates.evaluate({'gain': row}, policy)
        self.assertFalse(incompatible['gates']['latency']['details']['gain']['comparable'])
        row['baseline']['dataset']['fingerprint'] = 'different'
        incompatible = gates.evaluate({'gain': row}, policy)
        self.assertIsNone(incompatible['gates']['quality']['details']['gain']['delta'])

    def test_price_latency_and_missing_evidence_cannot_be_overridden_by_quality(self):
        policy = {'sets': {'a': {'diagnostic': False}}, 'profile': 'default'}
        good = gates.evaluate({'a': self.pair()}, policy)
        self.assertEqual(good['verdict'], 'cheaper')
        self.assertEqual(good['status'], 'exploration_finalist')
        for change, gate in [({'cost_per_search_usd': .000501}, 'price'),
                             ({'cost_per_1000_documents_usd': 10.01}, 'price'),
                             ({'latency_p95_ms': 12.01}, 'latency'),
                             ({'cost_per_search_usd': None}, 'price')]:
            with self.subTest(change=change):
                result = gates.evaluate({'a': self.pair(**change)}, policy)
                self.assertEqual(result['verdict'], 'rejected')
                self.assertFalse(result['gates'][gate]['passed'])
        self.assertEqual(gates.evaluate({}, policy)['verdict'], 'rejected')
        deep = gates.evaluate({'a': self.pair(cost_per_search_usd=.05)}, dict(policy, profile='deep'))
        self.assertEqual(deep['verdict'], 'better')
        self.assertEqual(set(good['gates']['latency']['samples']['a']['candidate']['query_ids']), set(map(str, range(20))))
        for sample in (None, {'policy': 'other', 'query_ids': ['0', '1'], 'warmup_query_ids': ['0']},
                       {'policy': 'sha256-query-id-v1; max=50', 'query_ids': ['1'], 'warmup_query_ids': ['0']}):
            mismatch = self.pair()
            mismatch['candidate']['cost']['latency_sample'] = sample
            self.assertFalse(gates.evaluate({'a': mismatch}, policy)['gates']['latency']['passed'])
        mismatch = self.pair()
        mismatch['candidate']['per_query']['ndcg@10'].pop('0')
        self.assertEqual(gates.evaluate({'a': mismatch}, policy)['verdict'], 'rejected')

    def test_mixed_private_aggregates_share_holm_and_all_four_gates(self):
        # Own cross-format family decisions: public samples and trusted private
        # sufficient statistics must enter one correction and all four gates.
        # No existing public-only or private-only owner sees that combination.
        import results
        import search_trial
        public = self.pair()
        public['candidate']['per_query'] = copy.deepcopy(public['baseline']['per_query'])
        private = self.pair()
        for side, row in private.items():
            row.update(schema_version=1, experiment='public/example', plugin_digest='sha256:fixture',
                       config={'side': side}, machine='fixture', duration_seconds=1)
            row['dataset'].update(version='v1', private=True)
            row['per_query'] = {}
            row['cost'].pop('latency_sample')
        private['candidate']['provenance'] = {'private_pair': {
            'statistics': {'queries': 20, 'delta': .02, 'p_value': .01}, 'latency_comparable': True}}
        def seal(pair):
            baseline, candidate = pair['baseline'], pair['candidate']
            candidate['provenance']['private_pair'].update(
                baseline_result_key=results.record(baseline)['result_key'],
                baseline_payload_digest=search_trial.digest(baseline),
                candidate_result_key=results.record(candidate)['result_key'],
                candidate_payload_digest=search_trial.digest({k: v for k, v in candidate.items() if k != 'provenance'}))
        pairs = {'public': public, 'private': private}
        policy = {'sets': {name: {'diagnostic': False} for name in pairs}, 'profile': 'default'}
        seal(private)
        good = gates.evaluate(pairs, policy)
        self.assertEqual(good['status'], 'exploration_finalist')
        self.assertTrue(all(gate['passed'] for gate in good['gates'].values()))
        self.assertAlmostEqual(good['sets']['private']['adjusted_p'], .02)
        for failed in ('quality', 'no_loss', 'latency', 'price'):
            changed = copy.deepcopy(pairs)
            if failed == 'quality':
                changed['private']['candidate']['provenance']['private_pair']['statistics']['p_value'] = .03
            elif failed == 'no_loss':
                scores = changed['public']['candidate']['per_query']['ndcg@10']
                scores.update({q: value - .1 for q, value in scores.items()})
            elif failed == 'latency':
                changed['private']['candidate']['metrics']['latency_p95_ms'] = 15
            else:
                changed['private']['candidate']['metrics']['cost_per_search_usd'] = .002
            seal(changed['private'])
            with self.subTest(failed=failed):
                verdict = gates.evaluate(changed, policy)
                self.assertEqual(verdict['status'], 'rejected')
                self.assertFalse(verdict['gates'][failed]['passed'])
                if failed == 'quality':
                    self.assertAlmostEqual(verdict['sets']['private']['adjusted_p'], .06)
                    self.assertTrue(verdict['gates']['no_loss']['passed'])
        self.assertIsNone(good['gates']['latency']['samples']['private']['candidate'])

    def test_corrected_loss_blocks_but_frozen_diagnostic_loss_is_reported(self):
        pairs = {'gain': self.pair(), 'loss': self.pair(-.1)}
        policy = {'sets': {name: {'diagnostic': False} for name in pairs}, 'profile': 'default'}
        self.assertFalse(gates.evaluate(pairs, policy)['gates']['no_loss']['passed'])
        policy['sets']['loss'] = {'diagnostic': True, 'reason': 'short-answer proxy'}
        report = gates.evaluate(pairs, policy)
        self.assertEqual(report['status'], 'exploration_finalist')
        self.assertEqual(report['sets']['loss']['role'], 'diagnostic')
        self.assertLess(report['sets']['loss']['delta'], 0)


if __name__ == '__main__':
    unittest.main()
