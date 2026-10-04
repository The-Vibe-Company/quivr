"""Compare direct embedding evidence, including self-hosted serving costs.

The input files are the JSON files written by ``direct_bakeoff.py``.  This
module deliberately has no provider or dataset dependencies: it validates
the evidence already on disk, pairs per-query scores when the local scoring
dependency is available, and writes a neutral JSON or Markdown table.
"""
import argparse
import json
import math
import pathlib
from collections.abc import Mapping


BASELINE = 'multilingual-e5-small (current)'
COHERE_FAST = 'Cohere-Embed-V5-Fast'
COHERE_PRO = 'Cohere-Embed-V5-Pro'
COHERE_PRO_1024 = 'Cohere-Embed-V5-Pro-1024'
QUALITY_METRICS = ('ndcg@10', 'recall@10', 'mrr@10')
HOSTED_MODELS = frozenset((COHERE_FAST, COHERE_PRO, COHERE_PRO_1024))
RECOMMENDATION = ('Provisional recommendation: keep the hosted reference until real open-source '
                  'evidence shows an acceptable quality and resource tradeoff. A non-significant '
                  'paired result is not evidence of equivalence.')


def _load(value):
    if isinstance(value, Mapping):
        return value
    return json.loads(pathlib.Path(value).read_text(encoding='utf-8'))


def _key(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)


def _number(value, field, minimum=0):
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f'{field} must be a finite number or null')
    if not math.isfinite(value) or value < minimum:
        raise ValueError(f'{field} must be a finite number or null')
    return value


def _lineage(report):
    sample = report.get('sample') or {}
    if not isinstance(sample, Mapping):
        raise ValueError('direct report sample must be an object')
    version = sample.get('version', report.get('version'))
    split = sample.get('split', report.get('split'))
    fingerprint = report.get('fingerprint')
    set_name = report.get('set')
    if not isinstance(set_name, str) or not set_name:
        raise ValueError('direct report set is required')
    if not isinstance(fingerprint, str) or not fingerprint:
        raise ValueError('direct report fingerprint is required')
    return {'set': set_name, 'fingerprint': fingerprint, 'split': split, 'version': version}


def _query_scores(label, result):
    per_query = result.get('per_query')
    if not isinstance(per_query, Mapping):
        raise ValueError(f'{label} result has no per-query scores')
    present = {}
    all_ids = set()
    for metric in QUALITY_METRICS:
        scores = per_query.get(metric)
        if scores is None:
            continue
        if not isinstance(scores, Mapping) or not scores:
            raise ValueError(f'{label} {metric} scores must be a non-empty object')
        checked = {}
        for query_id, score in scores.items():
            if not isinstance(query_id, str):
                query_id = str(query_id)
            checked[query_id] = _number(score, f'{label} {metric} score', 0)
            if checked[query_id] > 1:
                raise ValueError(f'{label} {metric} score must be between 0 and 1')
        present[metric] = checked
        all_ids.update(checked)
    if not present:
        raise ValueError(f'{label} result has no quality scores')
    for metric, scores in present.items():
        if set(scores) != all_ids:
            raise ValueError(f'{label} has mismatched query IDs across quality metrics')
    return present, all_ids


def _validate_report(report, index):
    if not isinstance(report, Mapping):
        raise ValueError(f'direct report {index} must be an object')
    if report.get('status') != 'complete':
        raise ValueError(f'direct report {index} is not complete')
    lineage = _lineage(report)
    results = report.get('results')
    if not isinstance(results, Mapping) or not results:
        raise ValueError(f'direct report {index} has no completed results')
    checked = {}
    for label, result in results.items():
        if not isinstance(label, str) or not label:
            raise ValueError(f'direct report {index} has an invalid model label')
        if not isinstance(result, Mapping):
            raise ValueError(f'{label} result must be an object')
        per_query, query_ids = _query_scores(label, result)
        means = result.get('mean') or {}
        if not isinstance(means, Mapping):
            raise ValueError(f'{label} result mean must be an object')
        checked[label] = {'result': result, 'mean': means, 'per_query': per_query,
                          'query_ids': query_ids}
    return lineage, checked


def _candidate_config(report, label):
    settings = report.get('settings') or {}
    configs = settings.get('openai_configs') if isinstance(settings, Mapping) else None
    value = configs.get(label) if isinstance(configs, Mapping) else None
    return value if isinstance(value, Mapping) else None


def _family(label, config, result):
    lower = label.lower()
    if label == BASELINE or lower in ('e5-small', 'multilingual-e5-small') or ('e5' in lower and 'current' in lower):
        return 'e5-small'
    if label == COHERE_FAST or 'cohere' in lower and 'fast' in lower:
        return 'Cohere Fast'
    if label == COHERE_PRO or 'cohere' in lower and 'pro' in lower:
        return 'Cohere Pro'
    serving = result.get('serving')
    if isinstance(config, Mapping) and (config.get('format') == 'openai' or config.get('auth') == 'none'):
        return 'OSS'
    if isinstance(serving, Mapping):
        return 'OSS'
    return 'Hosted' if label.startswith('text-embedding-') else 'Unknown'


def _settings_price(report, label):
    settings = report.get('settings') or {}
    prices = settings.get('prices_usd_per_million') if isinstance(settings, Mapping) else None
    if isinstance(prices, Mapping):
        value = prices.get(label)
        if value is None and label == COHERE_PRO_1024:
            value = prices.get(COHERE_PRO)
        return _number(value, f'{label} hosted price')
    return None


def _observation(report, label, checked, diagnostic_only):
    result = checked['result']
    config = _candidate_config(report, label)
    serving = result.get('serving')
    serving = serving if isinstance(serving, Mapping) else {}
    latency = result.get('latency_ms')
    latency = latency if isinstance(latency, Mapping) else {}
    hardware = serving.get('hardware') or (config or {}).get('hardware')
    revision = serving.get('model_revision') or (config or {}).get('model_revision')
    input_tokens = serving.get('input_tokens')
    if input_tokens is not None:
        input_tokens = _number(input_tokens, f'{label} serving input_tokens')
        if not float(input_tokens).is_integer():
            raise ValueError(f'{label} serving input_tokens must be an integer')
        input_tokens = int(input_tokens)
    hourly = _number(serving.get('hourly_usd'), f'{label} serving hourly_usd')
    seconds = _number(serving.get('seconds'), f'{label} serving seconds')
    estimated = _number(serving.get('estimated_usd'), f'{label} serving estimated_usd')
    if estimated is None and hourly is not None and seconds is not None:
        estimated = hourly * seconds / 3600
    per_million = _number(serving.get('usd_per_million_tokens'),
                          f'{label} serving usd_per_million_tokens')
    if per_million is None and estimated is not None and input_tokens:
        per_million = estimated * 1_000_000 / input_tokens
    if per_million is None and label in HOSTED_MODELS:
        per_million = _settings_price(report, label)
    latency_output = {
        'p50': _number(latency.get('p50'), f'{label} latency p50'),
        'p95': _number(latency.get('p95'), f'{label} latency p95'),
        'samples': latency.get('samples'),
        'scope': latency.get('scope'),
    }
    if latency_output['samples'] is not None:
        latency_output['samples'] = int(_number(latency_output['samples'], f'{label} latency samples'))
    means = {metric: checked['mean'].get(metric) for metric in QUALITY_METRICS}
    for metric, value in means.items():
        means[metric] = _number(value, f'{label} mean {metric}', 0)
        if means[metric] is not None and means[metric] > 1:
            raise ValueError(f'{label} mean {metric} must be between 0 and 1')
    gaps = []
    if latency_output['p50'] is None or latency_output['p95'] is None:
        gaps.append('query p50/p95 unavailable')
    if _family(label, config, result) == 'OSS':
        if hardware is None:
            gaps.append('hardware unavailable')
        if hourly is None:
            gaps.append('hourly serving cost unavailable')
        if per_million is None:
            gaps.append('per-million-token serving cost unavailable')
    for metric in QUALITY_METRICS:
        if means[metric] is None or metric not in checked['per_query']:
            gaps.append(f'{metric} unavailable')
    dimensions = result.get('dims', result.get('dimensions'))
    if dimensions is None and isinstance(config, Mapping):
        dimensions = config.get('dimensions')
    dimensions = _number(dimensions, f'{label} dimensions', 1)
    return {
        'id': label,
        'model': label,
        'family': _family(label, config, result),
        'dimensions': dimensions,
        'hardware': hardware,
        'model_revision': revision,
        'quality': means,
        'latency_ms': latency_output,
        'index_seconds': _number(result.get('index_s', result.get('index_seconds')),
                                 f'{label} index seconds'),
        'hourly_usd': hourly,
        'usd_per_million_tokens': per_million,
        'estimated_serving_usd': estimated,
        'input_tokens': input_tokens,
        'serving': dict(serving) if serving else None,
        'diagnostic_only': diagnostic_only,
        'evidence_gaps': gaps,
        '_per_query': checked['per_query'],
    }


def _fallback_pair(candidate, reference):
    """Return the deterministic part of a paired result when scipy is absent."""
    queries = sorted(set(candidate) & set(reference))
    delta = (sum(candidate[q] - reference[q] for q in queries) / len(queries)
             if queries else None)
    return {'queries': len(queries), 'delta': delta, 'p_value': None,
            'significant': None, 'status': 'available: p-value unavailable (scipy not installed)'}


def _pair(candidate, reference):
    if set(candidate) != set(reference):
        raise ValueError('paired comparison requires identical query IDs')
    try:
        import scoring
        paired = scoring.paired(candidate, reference)
        return dict(paired, status='available')
    except ImportError:
        return _fallback_pair(candidate, reference)


def _comparison(observation, reference):
    if reference is None:
        return {'status': 'unavailable', 'reason': 'hosted reference unavailable'}
    paired = {}
    for metric in QUALITY_METRICS:
        candidate = observation['_per_query'].get(metric)
        baseline = reference['_per_query'].get(metric)
        if candidate is None or baseline is None:
            paired[metric] = {'status': 'unavailable', 'reason': 'per-query scores unavailable'}
        else:
            paired[metric] = _pair(candidate, baseline)
    return {'against': reference['id'], 'metrics': paired, 'status': 'available'}


def _campaign(reports):
    campaigns = [report.get('serving_campaign') for report in reports
                 if report.get('serving_campaign') is not None]
    if not campaigns:
        return None
    if all(_key(value) == _key(campaigns[0]) for value in campaigns[1:]):
        return campaigns[0]
    return {'observations': campaigns, 'status': 'multiple campaign records'}


def build(reports):
    """Validate and compare loaded direct reports."""
    loaded = [_load(report) for report in reports]
    if not loaded:
        raise ValueError('at least one direct report is required')
    validated = [_validate_report(report, index) for index, report in enumerate(loaded)]
    lineage = validated[0][0]
    for other, _ in validated[1:]:
        if _key(other) != _key(lineage):
            raise ValueError('comparison requires matching set, fingerprint, split and version lineage')
    query_ids = set()
    for _, values in validated:
        for checked in values.values():
            if not query_ids:
                query_ids = set(checked['query_ids'])
            elif query_ids != checked['query_ids']:
                raise ValueError('comparison requires identical query IDs')
    restricted = any((isinstance(report.get('sample'), Mapping)
                      and report['sample'].get('tier') == 'restricted')
                     or not report.get('promotion_eligible', False)
                     for report in loaded)
    observations = []
    baseline_identity = {}
    for report_index, (report, (_, values)) in enumerate(zip(loaded, validated)):
        for label, checked in values.items():
            observation = _observation(report, label, checked, restricted)
            observation['measurement_identity'] = {
                'model': observation['model'], 'hardware': observation['hardware'],
                'model_revision': observation['model_revision']}
            if label == BASELINE:
                identity = _key(observation['measurement_identity'])
                if identity in baseline_identity:
                    existing = baseline_identity[identity]
                    existing.setdefault('measurement_sources', []).append(report_index)
                    existing['duplicate_measurements'] = existing.get('duplicate_measurements', 1) + 1
                    continue
                baseline_identity[identity] = observation
                observation['measurement_sources'] = [report_index]
            if any(item['id'] == observation['id'] for item in observations):
                suffix = observation.get('hardware') or f'observation {len(observations) + 1}'
                observation['id'] = f"{observation['id']} ({suffix})"
                if any(item['id'] == observation['id'] for item in observations):
                    observation['id'] += f" #{len(observations) + 1}"
            observations.append(observation)
    reference = next((item for item in observations if item['model'] == COHERE_PRO_1024), None)
    if reference is None:
        reference = next((item for item in observations if item['model'] == COHERE_PRO), None)
    comparisons = {}
    for observation in observations:
        comparisons[observation['id']] = _comparison(observation, reference)
    families = {item['family'] for item in observations}
    gaps = []
    for family, text in (('e5-small', 'no e5-small observation supplied'),
                         ('OSS', 'no OSS observation supplied'),
                         ('Cohere Fast', 'no hosted Cohere Fast observation supplied'),
                         ('Cohere Pro', 'no hosted Cohere Pro reference supplied')):
        if family not in families:
            gaps.append(text)
    gaps.extend(f'{item["id"]}: {gap}' for item in observations for gap in item['evidence_gaps'])
    output_observations = [{key: value for key, value in item.items() if key != '_per_query'}
                           for item in observations]
    return {
        'schema_version': 1,
        'status': 'comparison',
        'lineage': dict(lineage, query_count=len(query_ids)),
        'diagnostic_only': restricted,
        'observations': output_observations,
        'comparisons': comparisons,
        'reference': reference['id'] if reference else None,
        'serving_campaign': _campaign(loaded),
        'evidence_gaps': gaps,
        'recommendation': RECOMMENDATION,
        'scoring': {'metrics': list(QUALITY_METRICS),
                    'note': 'Paired deltas are candidate minus the hosted Cohere Pro reference; non-significance is not equivalence.'},
    }


def _display(value, digits=4):
    if value is None:
        return '—'
    if isinstance(value, float):
        return f'{value:.{digits}f}'
    return str(value)


def markdown(report):
    """Render a neutral comparison table without query or document text."""
    lineage = report['lineage']
    lines = ['# Open-source embedding comparison', '',
             f"Set: `{lineage['set']}`  ",
             f"Fingerprint: `{lineage['fingerprint']}`  ",
             f"Split: `{lineage.get('split') or 'unavailable'}`; version: `{lineage.get('version') or 'unavailable'}`  ",
             f"Diagnostic only: `{report['diagnostic_only']}`", '',
             RECOMMENDATION, '',
             '| Observation | Family | Dimensions | Hardware | nDCG@10 | Recall@10 | MRR@10 | Query p50 ms | Query p95 ms | Index s | Hourly USD | USD / 1M tokens | Serving USD |',
             '| --- | --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
    for item in report['observations']:
        latency = item['latency_ms']
        lines.append('| ' + ' | '.join((_display(item['id']), _display(item['family']), _display(item['dimensions']),
                                         _display(item['hardware']),
                                         _display(item['quality']['ndcg@10']), _display(item['quality']['recall@10']),
                                         _display(item['quality']['mrr@10']), _display(latency['p50'], 1),
                                         _display(latency['p95'], 1), _display(item['index_seconds'], 1),
                                         _display(item['hourly_usd'], 6), _display(item['usd_per_million_tokens'], 6),
                                         _display(item['estimated_serving_usd'], 6))) + ' |')
    lines += ['', '## Paired comparisons', '',
              '| Observation | Against | nDCG delta | nDCG p | Recall delta | Recall p | MRR delta | MRR p |',
              '| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |']
    for item in report['observations']:
        comparison = report['comparisons'][item['id']]
        metrics = comparison.get('metrics', {})
        lines.append('| ' + ' | '.join((item['id'], comparison.get('against', '—'),
                                         _display((metrics.get('ndcg@10') or {}).get('delta')),
                                         _display((metrics.get('ndcg@10') or {}).get('p_value')),
                                         _display((metrics.get('recall@10') or {}).get('delta')),
                                         _display((metrics.get('recall@10') or {}).get('p_value')),
                                         _display((metrics.get('mrr@10') or {}).get('delta')),
                                         _display((metrics.get('mrr@10') or {}).get('p_value')))) + ' |')
    lines += ['', '## Evidence gaps', '']
    lines += [f'- {gap}' for gap in report['evidence_gaps']] or ['- None recorded.']
    if report.get('serving_campaign') is not None:
        lines += ['', '## Serving campaign', '', 'The campaign record is reported separately from candidate embedding cost.',
                  '', '```json', json.dumps(report['serving_campaign'], indent=2, sort_keys=True), '```']
    return '\n'.join(lines) + '\n'


def write(report, path, output_format=None):
    path = pathlib.Path(path)
    if path.exists():
        raise FileExistsError('OSS comparison output exists; choose a new path')
    path.parent.mkdir(parents=True, exist_ok=True)
    output_format = output_format or ('markdown' if path.suffix.lower() in ('.md', '.markdown') else 'json')
    if output_format == 'markdown':
        text = markdown(report)
    elif output_format == 'json':
        text = json.dumps(report, indent=2, ensure_ascii=False, allow_nan=False) + '\n'
    else:
        raise ValueError('output format must be json or markdown')
    with path.open('x', encoding='utf-8') as output:
        output.write(text)
    return path


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('reports', nargs='+', type=pathlib.Path, help='completed direct_bakeoff JSON files')
    parser.add_argument('--out', required=True, type=pathlib.Path, help='new JSON or Markdown output path')
    parser.add_argument('--format', choices=('json', 'markdown'), help='override the output suffix')
    args = parser.parse_args(argv)
    report = build(args.reports)
    path = write(report, args.out, args.format)
    print(path)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
