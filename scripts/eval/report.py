"""Markdown rendering of an evaluation report.json (THE-775). No third-party package."""

METRIC_NAMES = [('ndcg@10', 'nDCG@10'), ('recall@10', 'Recall@10'), ('mrr@10', 'MRR@10')]


def number(v, digits=4):
    return '—' if v is None else f'{v:.{digits}f}'


def delta(c):
    """'+0.0123 (p 0.004) *' for one paired comparison; * marks p < alpha."""
    if not c or c.get('delta') is None:
        return '—'
    p = '—' if c['p_value'] is None else f"{c['p_value']:.3f}"
    return f"{c['delta']:+.4f} (p {p}){' *' if c['significant'] else ''}"


def markdown(r):
    run = r['run']
    lines = ['# Search quality evaluation', '',
             f"Status: **{r['status']}** · source `{run.get('source_revision', '')}`{' (dirty)' if run.get('source_dirty') else ''} · "
             f"{run.get('finished_at', '')} · {run.get('duration_seconds', 0):.0f} s · {run.get('target', '')}", '']
    if r['status'] != 'completed':
        lines += ['Error: `' + r.get('error', '') + '`', '', 'Partial results: ' + (', '.join(r['sets']) or 'none'), '']
    h = run.get('host', {})
    lines += [f"Host: {h.get('cpu_model', h.get('machine', ''))}, {h.get('logical_cpus')} logical CPUs. "
              f"Every query runs in each mode and each served profile, limit {r['limit']}, hits deduplicated by Record.", '']
    if r.get('convention'):
        lines += [f"Scoring: {r['convention']} Significance: {r['test']}; `*` marks p < 0.05.", '']
    if r['sets']:
        lines += ['## Sets', '', '| Set | Queries | Documents | Sample | Licence | Ingestion to vectors (s) |', '| --- | --- | --- | --- | --- | --- |']
        for name, s in r['sets'].items():
            m, sample = s['manifest'], s['manifest'].get('sample', {})
            how = f"seed {sample['seed']}, {sample.get('eligible_queries')} eligible queries" if sample else 'as given'
            lines.append(f"| {name} | {s['queries']} | {s['documents']} | {how} | {m.get('licence', 'private')} | {s['ingestion']['searchable_with_vectors_seconds']:.0f} |")
        lines.append('')
    for name, s in r['sets'].items():
        p = s.get('profiles', {})
        lines += [f'## {name}', '']
        if p.get('refused'):
            lines += ['Profiles not served: ' + ', '.join(f'`{k}` ({v})' for k, v in p['refused'].items()) + '.', '']
        lines += [f"Against `{r['baseline_system']}` in this run:", '',
                  '| System | ' + ' | '.join(n for _, n in METRIC_NAMES) + ' | Δ nDCG@10 | Δ Recall@10 | p50 ms | p95 ms | Failures | Paid calls / query |',
                  '| --- ' * (len(METRIC_NAMES) + 7) + '|']
        against = s.get('against_baseline_system', {})
        for system, v in s['systems'].items():
            c = against.get(system, {})
            paid = '—' if v['paid_calls_per_query'] is None else v['paid_calls_per_query']
            deltas = ['baseline'] * 2 if system == r['baseline_system'] else [delta(c.get(k)) for k in ['ndcg@10', 'recall@10']]
            lines.append(f"| {system} | " + ' | '.join(number(v['mean'][k]) for k, _ in METRIC_NAMES)
                         + f" | {deltas[0]} | {deltas[1]}"
                         + f" | {number(v['latency_ms']['p50'], 0)} | {number(v['latency_ms']['p95'], 0)} | {v['failures']}/{s['queries']} | {paid} |")
        lines.append('')
        if 'against_baseline_run' in s:
            if s['against_baseline_run'] is None:
                lines += ['Not compared with the baseline run: that run did not measure this exact sample.', '']
            else:
                b = r.get('baseline_run') or {}
                lines += [f"Against the same system in run {b.get('github', {}).get('GITHUB_RUN_ID') or b.get('id')} (source `{b.get('source_revision', '')}`):", '',
                          '| System | ' + ' | '.join('Δ ' + n for _, n in METRIC_NAMES) + ' |', '| --- ' * (len(METRIC_NAMES) + 1) + '|']
                for system, c in s['against_baseline_run'].items():
                    lines.append(f'| {system} | ' + ' | '.join(delta(c[k]) for k, _ in METRIC_NAMES) + ' |')
                lines.append('')
    phases = run.get('phases_seconds')
    if phases:
        lines += ['## Stack phases (seconds)', '', ' · '.join(f'{k} {v:.0f}' for k, v in phases.items()), '']
    return '\n'.join(lines) + '\n'
