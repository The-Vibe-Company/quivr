"""Four-gate owner: correction, diagnostic sets and cost/latency rejection.

Credible regressions: uncorrected significance, excluding a loss after seeing it,
or allowing a good score to override price. Existing scoring tests own only the
paired test; these exercise the verdict on complete measurement families.
"""
import hashlib
import importlib.util
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
