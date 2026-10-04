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
    samples, comparable_samples = {}, {}
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
        if candidate['dataset'].get('private') and candidate.get('provenance', {}).get('private_pair'):
            import results
            import search_trial
            evidence = candidate['provenance']['private_pair']
            paired = evidence.get('statistics', {})
            if (evidence.get('baseline_result_key') != results.record(baseline)['result_key']
                    or evidence.get('baseline_payload_digest') != search_trial.digest(baseline)
                    or evidence.get('candidate_result_key') != results.record(candidate)['result_key']
                    or evidence.get('candidate_payload_digest') != search_trial.digest(
                        {key: value for key, value in candidate.items() if key != 'provenance'})
                    or set(paired) != {'queries', 'delta', 'p_value'}
                    or type(paired.get('queries')) is not int or paired['queries'] < 1
                    or type(paired.get('delta')) not in (int, float) or not math.isfinite(paired['delta'])
                    or not -1 <= paired['delta'] <= 1
                    or paired['p_value'] is not None and (not finite(paired['p_value']) or paired['p_value'] > 1)
                    or type(evidence.get('latency_comparable')) is not bool):
                incomplete.add(name)
                continue
            comparable_samples[name] = evidence['latency_comparable']
            stats[name] = {**paired, 'significant': paired['p_value'] is not None and paired['p_value'] < .05,
                           'role': 'diagnostic' if names[name]['diagnostic'] else 'gate',
                           'reason': names[name].get('reason')}
            continue
        a, b = (pairs[name][side].get('per_query', {}).get('ndcg@10', {})
                for side in ('candidate', 'baseline'))
        if not a or set(a) != set(b) or any(not finite(v) or v > 1 for v in [*a.values(), *b.values()]):
            incomplete.add(name)
            continue
        expected = {'policy': 'sha256-query-id-v1; max=50',
                    'query_ids': sorted(a, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q))[:50],
                    'warmup_query_ids': [sorted(a)[0]]}
        comparable_samples[name] = all(sample == expected for sample in samples[name].values())
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
    latency, price, cheaper = not incomplete and all(comparable_samples.values()), not incomplete, True
    latency_details, price_details = {}, {}
    for name in names:
        pair = pairs.get(name, {})
        a, b = (pair.get(side, {}).get('metrics', {}) for side in ('candidate', 'baseline'))
        ap, bp = a.get('latency_p95_ms'), b.get('latency_p95_ms')
        ratio = ap / bp if finite(ap) and finite(bp) and bp > 0 else None
        latency_details[name] = {'candidate_p95_ms': ap if finite(ap) else None,
                                 'baseline_p95_ms': bp if finite(bp) else None,
                                 'ratio': ratio if finite(ratio) else None,
                                 'max_ratio': limits['latency_ratio'],
                                 'comparable': comparable_samples.get(name, False)}
        latency &= finite(ap) and finite(bp) and ap <= limits['latency_ratio'] * bp
        search, index = a.get('cost_per_search_usd'), a.get('cost_per_1000_documents_usd')
        price_details[name] = {'cost_per_search_usd': search if finite(search) else None,
                               'cost_per_1000_documents_usd': index if finite(index) else None,
                               'max_search_usd': limits['search_usd'],
                               'max_index_usd_per_1000_documents': limits['index_usd']}
        price &= finite(search) and finite(index) and search <= limits['search_usd'] and index <= limits['index_usd']
        base_price = b.get('cost_per_search_usd')
        cheaper &= finite(search) and finite(base_price) and search < base_price
    quality_details = {name: {'delta': stats.get(name, {}).get('delta'),
                              'adjusted_p': stats.get(name, {}).get('adjusted_p'),
                              'role': 'diagnostic' if names[name]['diagnostic'] else 'gate',
                              'significance_level': .05} for name in names}
    outcome = {
        'quality': {'passed': quality, 'reason': 'requires corrected gain >= threshold', 'min_gain': limits['min_gain'],
                    'details': {name: {**value, 'min_gain': limits['min_gain']} for name, value in quality_details.items()}},
        'no_loss': {'passed': no_loss, 'reason': 'no corrected significant loss on eligible sets', 'details': quality_details},
        'latency': {'passed': latency, 'reason': 'requires comparable measured p95', 'max_ratio': limits['latency_ratio'],
                    'samples': samples, 'details': latency_details},
        'price': {'passed': price, 'reason': 'unknown or excessive serving/indexing price rejects',
                  'max_search_usd': limits['search_usd'], 'max_index_usd_per_1000_documents': limits['index_usd'],
                  'details': price_details}}
    passed = all(g['passed'] for g in outcome.values())
    return {'verdict': ('cheaper' if cheaper else 'better') if passed else 'rejected',
            'status': 'exploration_finalist' if passed else 'rejected', 'confirmation_available': False,
            'gates': outcome, 'sets': stats, 'missing_or_incompatible_sets': sorted(incomplete)}
