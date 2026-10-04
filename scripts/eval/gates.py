"""Conservative four-gate decisions for complete, paired measurement families."""
import math
import hashlib

import scoring


def holm(values):
    """Holm adjusted two-sided p-values, including diagnostic comparisons."""
    ordered = sorted(values, key=lambda key: values[key])
    adjusted, previous = {}, 0
    for i, key in enumerate(ordered):
        previous = max(previous, min(1, values[key] * (len(ordered) - i)))
        adjusted[key] = previous
    return adjusted


def finite(value):
    return type(value) in (int, float) and math.isfinite(value) and value >= 0


def evaluate(pairs, policy):
    """Pairs are candidate/baseline records; policy freezes sets and thresholds.

    This is a dev decision only. Full-engine/heldout confirmation is separate.
    Unknown evidence fails closed rather than qualifying from partial results.
    """
    names = policy['sets']
    limits = {'min_gain': .01, 'latency_ratio': 1.2, 'index_usd': 10,
              'search_usd': .05 if policy['profile'] == 'deep' else .0005,
              **policy.get('gates', {})}
    stats, incomplete = {}, set(names) - set(pairs)
    samples, comparable_samples = {}, True
    for name in names:
        if name not in pairs:
            continue
        candidate, baseline = (pairs[name][side] for side in ('candidate', 'baseline'))
        samples[name] = {side: pairs[name][side].get('cost', {}).get('latency_sample')
                         for side in ('candidate', 'baseline')}
        if (candidate.get('dataset') != baseline.get('dataset')
                or not candidate.get('dataset') or candidate.get('tier') != baseline.get('tier')
                or not candidate.get('tier') or not candidate.get('git_sha')
                or candidate.get('git_sha') != baseline.get('git_sha')
                or not candidate.get('cost', {}).get('resource_class')
                or not candidate.get('cost', {}).get('latency_method')
                or candidate.get('cost', {}).get('resource_class') != baseline.get('cost', {}).get('resource_class')
                or candidate.get('cost', {}).get('latency_method') != baseline.get('cost', {}).get('latency_method')):
            incomplete.add(name)
            continue
        a, b = (pairs[name][side].get('per_query', {}).get('ndcg@10', {})
                for side in ('candidate', 'baseline'))
        if not a or set(a) != set(b) or any(not finite(v) or v > 1 for v in [*a.values(), *b.values()]):
            incomplete.add(name)
            continue
        expected = {'policy': 'sha256-query-id-v1; max=50',
                    'query_ids': sorted(a, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q))[:50],
                    'warmup_query_ids': [sorted(a)[0]]}
        comparable_samples &= all(sample == expected for sample in samples[name].values())
        stats[name] = {**scoring.paired(a, b),
                       'role': 'diagnostic' if names[name]['diagnostic'] else 'gate',
                       'reason': names[name].get('reason')}
    corrected = holm({name: value['p_value'] if value['p_value'] is not None else 1
                      for name, value in stats.items()})
    for name, value in stats.items():
        value['adjusted_p'] = corrected[name]
    eligible = [s for s in stats.values() if s['role'] == 'gate']
    quality = not incomplete and any(s['delta'] >= limits['min_gain'] and s['adjusted_p'] < .05 for s in eligible)
    no_loss = not incomplete and bool(eligible) and not any(s['delta'] < 0 and s['adjusted_p'] < .05 for s in eligible)
    latency, price, cheaper = not incomplete and comparable_samples, not incomplete, True
    for name in names:
        if name not in pairs:
            continue
        a, b = (pairs[name][side].get('metrics', {}) for side in ('candidate', 'baseline'))
        ap, bp = a.get('latency_p95_ms'), b.get('latency_p95_ms')
        latency &= finite(ap) and finite(bp) and ap <= limits['latency_ratio'] * bp
        search, index = a.get('cost_per_search_usd'), a.get('cost_per_1000_documents_usd')
        price &= finite(search) and finite(index) and search <= limits['search_usd'] and index <= limits['index_usd']
        base_price = b.get('cost_per_search_usd')
        cheaper &= finite(search) and finite(base_price) and search < base_price
    outcome = {
        'quality': {'passed': quality, 'reason': 'requires corrected gain >= threshold', 'min_gain': limits['min_gain']},
        'no_loss': {'passed': no_loss, 'reason': 'no corrected significant loss on eligible sets'},
        'latency': {'passed': latency, 'reason': 'requires comparable measured p95', 'max_ratio': limits['latency_ratio'], 'samples': samples},
        'price': {'passed': price, 'reason': 'unknown or excessive serving/indexing price rejects',
                  'max_search_usd': limits['search_usd'], 'max_index_usd_per_1000_documents': limits['index_usd']}}
    passed = all(g['passed'] for g in outcome.values())
    return {'verdict': ('cheaper' if cheaper else 'better') if passed else 'rejected',
            'status': 'exploration_finalist' if passed else 'rejected', 'confirmation_available': False,
            'gates': outcome, 'sets': stats, 'missing_or_incompatible_sets': sorted(incomplete)}
