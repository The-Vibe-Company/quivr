"""Offline dataset quality reports for the direct embedding comparison.

The report is deliberately aggregate.  It records enough information to decide
whether a set is useful for a comparison without copying query or document
text into a dated evidence file.
"""
import argparse
import datetime
import json
import math
import os
import pathlib
import statistics
import unicodedata
from collections import Counter
from collections.abc import Mapping

import direct_bakeoff
import embeddings
import public_sets
import trec

import ci_guard


PRICE_REFERENCE_DATE = '2026-10-03'
HOSTED_WINDOW_CHARS = 6000
OVERLAP_CHARS = 200
HOSTED_PRICE_USD_PER_MILLION = 0.12
COHERE_PRO = 'Cohere-Embed-V5-Pro'
BASELINE = direct_bakeoff.BASELINE
MEAN_METRICS = ('ndcg@10', 'recall@10', 'mrr@10')
RUN_AGGREGATES = ('dims', 'dimensions', 'pieces', 'index_s', 'query_ms')

# These markers make the classification inspectable.  A query is classified as
# a question when it ends in a question mark or starts with one of these lexical
# markers; every other query is reported as a statement.  This is a heuristic,
# not a manual annotation or an agreement estimate.
QUESTION_MARKERS = frozenset({
    'can', 'could', 'did', 'do', 'does', 'how', 'is', 'may', 'might', 'must',
    'what', 'when', 'where', 'which', 'who', 'whom', 'whose', 'why', 'would',
    'combien', 'comment', 'est', 'lequel', 'laquelle', 'lesquels', 'lesquelles',
    'où', 'pourquoi', 'quel', 'quelle', 'quels', 'quelles', 'qui', 'que', 'quoi',
})


def _copy_json(value):
    """Copy manifest values while keeping the report JSON-serialisable."""
    return json.loads(json.dumps(value, ensure_ascii=False))


def _summary(values):
    values = list(values)
    if not values:
        return {'count': 0, 'min': None, 'max': None, 'mean': None, 'median': None, 'histogram': {}}
    histogram = Counter(values)
    median = statistics.median(values)
    return {'count': len(values), 'min': min(values), 'max': max(values),
            'mean': sum(values) / len(values), 'median': median,
            'histogram': {str(key): histogram[key] for key in sorted(histogram)}}


def _data_parts(data):
    if not isinstance(data, Mapping):
        raise ValueError('dataset data must be a mapping')
    corpus = data.get('corpus')
    queries = data.get('queries')
    qrels = data.get('qrels')
    if not isinstance(corpus, Mapping) or not isinstance(queries, Mapping) or not isinstance(qrels, Mapping):
        raise ValueError('dataset data needs corpus, queries and qrels mappings')
    return corpus, queries, qrels


def _manifest_metadata(manifest, sample_counts):
    if not isinstance(manifest, Mapping):
        raise ValueError('manifest must be a mapping')
    tier = manifest.get('tier', 'default')
    restricted = tier == 'restricted'
    promotion = False if restricted else bool(manifest.get('promotion_eligible', False))
    known_issues = manifest.get('known_issues', [])
    if known_issues is None:
        known_issues = []
    if not isinstance(known_issues, list) or any(not isinstance(issue, str) for issue in known_issues):
        raise ValueError('manifest known_issues must be a list of strings')
    sources = manifest.get('licence_sources', [])
    if sources is None:
        sources = []
    if not isinstance(sources, list) or any(not isinstance(source, str) for source in sources):
        raise ValueError('manifest licence_sources must be a list of strings')
    source_counts = manifest.get('source_counts', sample_counts)
    if not isinstance(source_counts, Mapping):
        raise ValueError('manifest source_counts must be a mapping')
    return {
        'name': manifest.get('name'),
        'language': manifest.get('language'),
        'description': manifest.get('description'),
        'licence': manifest.get('licence'),
        'source': manifest.get('source'),
        'version': _copy_json(manifest.get('version', {})),
        'split': manifest.get('split'),
        'tier': tier,
        'promotion_eligible': promotion,
        'licence_checked': manifest.get('licence_checked'),
        'licence_sources': _copy_json(sources),
        'query_type': manifest.get('query_type'),
        'known_issues': _copy_json(known_issues),
        'source_counts': _copy_json(source_counts),
        'sample': _copy_json(manifest.get('sample', {})),
        'fingerprint': manifest.get('fingerprint'),
        'files': _copy_json(manifest.get('files', {})),
    }


def _query_type(text):
    normalized = unicodedata.normalize('NFKC', text).strip().lower()
    if normalized.endswith('?'):
        return 'question'
    first = normalized.split(None, 1)[0] if normalized else ''
    first = first.strip('"\'“”«»¿¡.,;:()[]{}')
    return 'question' if first in QUESTION_MARKERS else 'statement'


def _query_types(queries, source):
    counts = Counter(_query_type(text) for text in queries.values())
    return {
        'source': source,
        'heuristic': "question if text ends '?' or starts with a recognized lexical question marker; statement otherwise",
        'counts': {'question': counts.get('question', 0), 'statement': counts.get('statement', 0)},
        'manual_labels': None,
        'judge_agreement': None,
        'notes': 'No manual query-type labels or judge-agreement information supplied.',
    }


def _docs_for_hosted(corpus):
    docs = []
    for key in sorted(corpus):
        value = corpus[key]
        if not isinstance(value, Mapping) or not isinstance(value.get('text'), str):
            raise ValueError('each corpus entry needs a text string')
        title = value.get('title') or ''
        if not isinstance(title, str):
            raise ValueError('each corpus title must be a string')
        docs.append((title + '\n' if title else '') + value['text'])
    return docs


def _hosted_estimate(corpus, queries):
    pieces, _ = direct_bakeoff.split_documents(_docs_for_hosted(corpus), HOSTED_WINDOW_CHARS, OVERLAP_CHARS)
    inputs = pieces + [queries[key] for key in sorted(queries)]
    input_tokens = embeddings.estimate_tokens(inputs)
    estimated_usd = input_tokens * HOSTED_PRICE_USD_PER_MILLION / 1_000_000
    # The byte-plus-special-token estimate is conservative for one pass.  The
    # recommended per-set cap covers two passes (a retry or paired hosted run)
    # and rounds upward so it can be copied into direct_bakeoff.
    cap_usd = max(0.01, math.ceil(estimated_usd * 2 * 100) / 100)
    caps = {'max_input_tokens': math.ceil(input_tokens * 2), 'max_usd': cap_usd}
    return {
        'window_chars': HOSTED_WINDOW_CHARS,
        'overlap_chars': OVERLAP_CHARS,
        'document_windows': len(pieces),
        'query_inputs': len(queries),
        'input_tokens': input_tokens,
        'price_usd_per_million': HOSTED_PRICE_USD_PER_MILLION,
        'estimated_usd': estimated_usd,
        'estimate_only': True,
        'per_set_caps': _copy_json(caps),
    }


def _run_mapping(run):
    if run is None:
        return None
    if isinstance(run, (str, os.PathLike)):
        return json.loads(pathlib.Path(run).read_text(encoding='utf-8'))
    if not isinstance(run, Mapping):
        raise ValueError('run must be a mapping or JSON path')
    return run


def _run_result(run, manifest, query_ids, model):
    run = _run_mapping(run)
    if run is None:
        return None
    if run.get('status') != 'complete':
        raise ValueError(f'{model} run is not complete')
    name = manifest.get('name')
    if run.get('set') != name:
        raise ValueError(f'{model} run set does not match the manifest')
    fingerprint = manifest.get('fingerprint')
    if not fingerprint or run.get('fingerprint') != fingerprint:
        raise ValueError(f'{model} run fingerprint does not match the manifest')
    settings = run.get('settings')
    if not isinstance(settings, Mapping):
        raise ValueError(f'{model} run has no model settings')
    if not isinstance(settings.get('models'), list) or model not in settings['models']:
        raise ValueError(f'{model} run model identity does not match its settings')
    if model == BASELINE and (settings.get('e5_model') != direct_bakeoff.E5_MODEL
                              or settings.get('e5_revision') != direct_bakeoff.E5_REVISION):
        raise ValueError(f'{model} run does not match the pinned E5 model and revision')
    results = run.get('results')
    if not isinstance(results, Mapping) or model not in results:
        raise ValueError(f'{model} run has no result for the requested model')
    result = results[model]
    if not isinstance(result, Mapping):
        raise ValueError(f'{model} result is not a mapping')
    per_query = result.get('per_query')
    if not isinstance(per_query, Mapping) or not isinstance(per_query.get('ndcg@10'), Mapping):
        raise ValueError(f'{model} run has no per-query nDCG@10 scores')
    scores = per_query['ndcg@10']
    expected = {str(query_id) for query_id in query_ids}
    actual = {str(query_id) for query_id in scores}
    if actual != expected:
        raise ValueError(f'{model} run does not contain exactly all sampled query scores')
    values = {}
    for query_id, score in scores.items():
        if isinstance(score, bool):
            raise ValueError(f'{model} run contains a non-finite nDCG@10 score')
        try:
            score = float(score)
        except (TypeError, ValueError):
            raise ValueError(f'{model} run contains a non-finite nDCG@10 score') from None
        if not math.isfinite(score) or not 0 <= score <= 1:
            raise ValueError(f'{model} run contains an nDCG@10 score outside [0, 1]')
        values[str(query_id)] = score
    mean = result.get('mean')
    if not isinstance(mean, Mapping):
        raise ValueError(f'{model} result has no mean scores')
    aggregate_mean = {}
    for metric in MEAN_METRICS:
        if metric not in mean:
            if metric == 'ndcg@10':
                raise ValueError(f'{model} result has no finite mean nDCG@10')
            continue
        value = mean[metric]
        if isinstance(value, bool):
            raise ValueError(f'{model} result has a non-finite mean {metric}')
        try:
            value = float(value)
        except (TypeError, ValueError):
            raise ValueError(f'{model} result has a non-finite mean {metric}') from None
        if not math.isfinite(value) or not 0 <= value <= 1:
            raise ValueError(f'{model} result has a mean {metric} outside [0, 1]')
        aggregate_mean[metric] = value
    aggregate = {'mean': aggregate_mean, 'per_query': values}
    for field in RUN_AGGREGATES:
        if field not in result:
            continue
        value = result[field]
        if isinstance(value, bool):
            raise ValueError(f'{model} result has an invalid aggregate {field}')
        try:
            numeric = float(value)
        except (TypeError, ValueError):
            raise ValueError(f'{model} result has an invalid aggregate {field}') from None
        if not math.isfinite(numeric) or numeric < 0:
            raise ValueError(f'{model} result has an invalid aggregate {field}')
        aggregate[field] = value
    return aggregate


def _quality_run(result, model):
    if result is None:
        return {'model': model, 'mean': None, 'saturation': None,
                'reason': 'awaiting local E5 run' if model == BASELINE else 'awaiting coordinator hosted Cohere-Embed-V5-Pro run'}
    scores = result['per_query']
    saturated = sum(score == 1.0 for score in scores.values())
    total = len(scores)
    report = {'model': model, 'mean': result['mean'],
              'saturation': {'saturated_queries': saturated, 'queries': total,
                             'share': saturated / total if total else None}}
    report.update({field: result[field] for field in RUN_AGGREGATES if field in result})
    return report


def _run_provenance(run):
    """Keep run identity/settings while excluding result and per-query payloads."""
    if run is None:
        return None
    return {key: _copy_json(run[key]) for key in ('date', 'status', 'set', 'fingerprint', 'settings') if key in run}


def inspect(data, manifest, local_run=None, cohere_run=None):
    """Build an aggregate quality report from a loaded TREC set and manifest.

    Supplied runs are validated before any score is included.  The optional
    Cohere argument may be omitted for an inventory report; a hosted result in
    the local run is used when present so one direct_bakeoff file is convenient.
    """
    corpus, queries, qrels = _data_parts(data)
    sample_counts = {'documents': len(corpus), 'queries': len(queries),
                     'judgments': sum(len(docs) for docs in qrels.values())}
    metadata = _manifest_metadata(manifest, sample_counts)
    if not metadata['name']:
        raise ValueError('manifest name is required')
    depths = [len(qrels.get(query_id, {})) for query_id in sorted(queries)]
    grade_counts = Counter(grade for docs in qrels.values() for grade in docs.values())
    query_lengths = {
        'chars': _summary(len(text) for text in queries.values()),
        'words': _summary(len(text.split()) for text in queries.values()),
    }
    local = _run_mapping(local_run)
    query_ids = queries.keys()
    e5_result = _run_result(local, manifest, query_ids, BASELINE)
    if cohere_run is None and isinstance(local, Mapping) and COHERE_PRO in (local.get('results') or {}):
        hosted = local
    else:
        hosted = _run_mapping(cohere_run)
    cohere_result = _run_result(hosted, manifest, query_ids, COHERE_PRO) if hosted is not None else None
    e5 = _quality_run(e5_result, BASELINE)
    cohere = _quality_run(cohere_result, COHERE_PRO)
    if e5_result is not None and cohere_result is not None:
        joint_count = sum(e5_result['per_query'][query_id] == 1.0 and cohere_result['per_query'][query_id] == 1.0
                          for query_id in e5_result['per_query'])
        joint = {'saturated_queries': joint_count, 'queries': len(e5_result['per_query']),
                 'share': joint_count / len(e5_result['per_query']) if e5_result['per_query'] else None}
    else:
        joint = {'saturated_queries': None, 'queries': len(queries), 'share': None,
                 'reason': ('awaiting coordinator hosted Cohere-Embed-V5-Pro run'
                            if cohere_result is None else 'awaiting local E5 run')}

    grades = {'counts': {str(grade): grade_counts[grade] for grade in sorted(grade_counts)},
              'positive_judgments': sum(count for grade, count in grade_counts.items() if grade > 0),
              'max_grade': max(grade_counts) if grade_counts else None}
    judged_depth = _summary(depths)
    judged_depth['total_judgments'] = sample_counts['judgments']
    judged_depth['queries'] = len(depths)
    report = {
        'date': datetime.datetime.now(datetime.timezone.utc).date().isoformat(),
        'status': 'evidence',
        'set': metadata['name'],
        'metadata': metadata,
        'counts': sample_counts,
        'source_counts': metadata['source_counts'],
        'fingerprint': metadata['fingerprint'],
        'sample': metadata['sample'],
        'judged_depth': judged_depth,
        'grades': grades,
        'query_lengths': query_lengths,
        'query_types': _query_types(queries, metadata['query_type']),
        'e5': e5,
        'cohere_pro': cohere,
        'runs': {'e5': _run_provenance(local), 'cohere_pro': _run_provenance(hosted)},
        'saturation': {'e5': e5['saturation'], 'joint_e5_cohere_pro': joint},
        'hosted_input_estimate': _hosted_estimate(corpus, queries),
    }
    return report


def _number(value, digits=3):
    return '—' if value is None else f'{value:.{digits}f}'


def markdown(report):
    """Render a short dated Markdown report without query/document contents."""
    metadata = report['metadata']
    lines = [f"# Dataset quality report: {report['set']}", '',
             f"Date: {report['date']}", 'Status: evidence', '',
             f"Language: {metadata.get('language') or 'unspecified'}  ",
             f"Description: {metadata.get('description') or 'unspecified'}  ",
             f"Licence: {metadata.get('licence') or 'unspecified'}  ",
             f"Source: {metadata.get('source') or 'unspecified'}  ",
             f"Split: {metadata.get('split') or 'unspecified'}  ",
             f"Tier: {metadata.get('tier', 'default')}; promotion eligible: {metadata.get('promotion_eligible', False)}", '']
    if metadata.get('version'):
        lines += ['Version: ' + ', '.join(f'`{repo}` `{revision}`' for repo, revision in metadata['version'].items()), '']
    lines += ['## Counts', '',
              '| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |',
              '| ---: | ---: | ---: | ---: | ---: | ---: |']
    counts, source_counts = report['counts'], report['source_counts']
    lines.append('| ' + ' | '.join(str((counts if key == 'sample' else source_counts)[field])
                                      for key, field in [('sample', 'documents'), ('sample', 'queries'), ('sample', 'judgments'),
                                                         ('source', 'documents'), ('source', 'queries'), ('source', 'judgments')]) + ' |')
    lines += ['', '## Judgements and queries', '',
              f"Judged depth: {report['judged_depth']['min']}–{report['judged_depth']['max']} documents per query; "
              f"mean {_number(report['judged_depth']['mean'])}; total {report['judged_depth']['total_judgments']}.",
              'Grades: ' + ', '.join(f'{grade}: {count}' for grade, count in report['grades']['counts'].items()) + '.',
              '', '| Length | Count | Min | Median | Mean | Max |', '| --- | ---: | ---: | ---: | ---: | ---: |']
    for name in ('chars', 'words'):
        value = report['query_lengths'][name]
        lines.append(f"| {name} | {value['count']} | {value['min']} | {value['median']} | {_number(value['mean'])} | {value['max']} |")
    query_types = report['query_types']
    lines += ['', 'Query type source: ' + (query_types['source'] or 'unspecified') + '.',
              'Heuristic: ' + query_types['heuristic'] + '.',
              'Heuristic counts: ' + ', '.join(f'{name} {count}' for name, count in query_types['counts'].items()) + '.',
              'Manual query-type labels: unavailable; judge agreement: unavailable.', '']
    lines += ['## Embedding quality', '', '| System | Mean nDCG@10 | Saturated queries | Saturation share |',
              '| --- | ---: | ---: | ---: |']
    for name, value in (('E5', report['e5']), ('Cohere Pro', report['cohere_pro'])):
        saturation = value['saturation'] or {}
        mean = None if value['mean'] is None else value['mean'].get('ndcg@10')
        lines.append(f"| {name} | {_number(mean, 4)} | {saturation.get('saturated_queries', '—')} / {saturation.get('queries', '—')} | {_number(saturation.get('share'), 4)} |")
    joint = report['saturation']['joint_e5_cohere_pro']
    lines += [f"", f"Joint E5/Cohere Pro saturation: {_number(joint.get('share'), 4)} "
              f"({joint.get('saturated_queries', '—')} / {joint.get('queries', '—')})."]
    if joint.get('reason'):
        lines.append(joint['reason'] + '.')
    estimate = report['hosted_input_estimate']
    lines += ['', '## Hosted input estimate', '',
              f"One hosted pass uses {estimate['document_windows']} document windows and {estimate['query_inputs']} query inputs "
              f"for an estimated {estimate['input_tokens']:,} input tokens and ${estimate['estimated_usd']:.4f} "
              f"at ${estimate['price_usd_per_million']:.2f}/million (dated {PRICE_REFERENCE_DATE}; estimate only).",
              f"Per-set cap recommendation: {estimate['per_set_caps']['max_input_tokens']:,} input tokens and "
              f"${estimate['per_set_caps']['max_usd']:.2f}.", '']
    lines += ['## Known issues', '']
    lines += [f'- {issue}' for issue in metadata['known_issues']] or ['None recorded.']
    lines += ['', f"Sample fingerprint: `{metadata.get('fingerprint') or 'unspecified'}`", '']
    return '\n'.join(lines) + '\n'


def write(report, output_dir):
    """Write exactly one JSON and one Markdown file, refusing both overwrites."""
    output_dir = pathlib.Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    name = report.get('set')
    if not isinstance(name, str) or not name or pathlib.PurePath(name).name != name:
        raise ValueError('report set must be a simple file name')
    json_path, markdown_path = output_dir / f'{name}.json', output_dir / f'{name}.md'
    if json_path.exists() or markdown_path.exists():
        raise FileExistsError('quality report output exists; choose a new directory')
    json_text = json.dumps(report, indent=2, ensure_ascii=False, allow_nan=False) + '\n'
    markdown_text = markdown(report)
    with json_path.open('x', encoding='utf-8') as output:
        output.write(json_text)
    with markdown_path.open('x', encoding='utf-8') as output:
        output.write(markdown_text)
    return json_path, markdown_path


def _load_json(path):
    return json.loads(pathlib.Path(path).read_text(encoding='utf-8'))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--set', required=True, help='registered evaluation set name')
    parser.add_argument('--cache', type=pathlib.Path, default=public_sets.ROOT / '.scratch/eval/cache')
    parser.add_argument('--e5-run', type=pathlib.Path, help='completed direct_bakeoff JSON; optional for inventory reports')
    parser.add_argument('--cohere-run', type=pathlib.Path, help='completed hosted Cohere Pro direct_bakeoff JSON')
    parser.add_argument('--out', required=True, type=pathlib.Path)
    parser.add_argument('--include-restricted', action='store_true', help='include a restricted diagnostic set')
    args = parser.parse_args(argv)
    if ci_guard.in_ci():
        raise SystemExit('quality reports run locally only; CI/GITHUB_ACTIONS preparation is refused')
    directory = public_sets.prepare(args.set, args.cache, include_restricted=args.include_restricted)
    data = trec.load(directory)
    manifest = _load_json(directory / 'manifest.json')
    report = inspect(data, manifest,
                     _load_json(args.e5_run) if args.e5_run else None,
                     _load_json(args.cohere_run) if args.cohere_run else None)
    paths = write(report, args.out)
    print('\n'.join(str(path) for path in paths))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
