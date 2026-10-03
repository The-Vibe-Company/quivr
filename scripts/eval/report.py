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


def change(before, after):
    """'-99 (-54%)' from base to branch, in ms."""
    if before is None or after is None:
        return '—'
    return f"{after - before:+.0f}" + (f" ({100 * (after - before) / before:+.0f}%)" if before else '')


def comparison(r):
    """The latency table of a --compare-to run: base and branch measured in one job, on one CPU."""
    c, run = r.get('compare'), r['run']
    if not c:
        return []
    h = run.get('host', {})
    lines = ['## Before and after on this machine', '',
             f"Base `{c['ref']}` (`{c['source_revision'][:12]}`) against this checkout (`{run.get('source_revision', '')[:12]}`), "
             f"both on {h.get('cpu_model', 'an unknown CPU')} in one job. Each query is searched on both, alternating which goes first.", '',
             '| Set | System | Base p50 ms | Branch p50 ms | Δ p50 | Base p95 ms | Branch p95 ms | Δ p95 |', '| --- ' * 8 + '|']
    for name, s in r['sets'].items():
        base = c['sets'].get(name, {}).get('systems', {})
        for system, v in s['systems'].items():
            if system in base:
                b, a = base[system]['latency_ms'], v['latency_ms']
                lines.append(f"| {name} | {system} | {number(b['p50'], 0)} | {number(a['p50'], 0)} | {change(b['p50'], a['p50'])} "
                             f"| {number(b['p95'], 0)} | {number(a['p95'], 0)} | {change(b['p95'], a['p95'])} |")
    return lines + ['']


def time_table(systems):
    """Where each system's search time goes, per limit; empty for a run before THE-873 timed phases."""
    rows = [(system, limit, t) for system, v in systems.items() for limit, t in v.get('time_by_limit', {}).items()]
    rows += [(system + ' (bounded-cache replay)', v.get('scored_limit', 10), v['warm_cache_timing'])
             for system, v in systems.items() if 'warm_cache_timing' in v]
    if not rows:
        return []
    lines = ['Where search time goes, in ms, p50 / p95. Engine is the time the engine reports (`usage.elapsed_ms`); '
             'the phases are its `usage.phases`. Encoding share is the part of the engine\'s time spent encoding queries, '
             'what a query-vector cache would save if every query hit it; a lower bound, since each phase is whole milliseconds rounded down. '
             'Limits other than the scored one are timed in a second pass.', '',
             '| System | Limit | Client | Engine | Query encoding | Index | Hydration | Plugin rounds | Encoding share | Failures |', '| --- ' * 10 + '|']
    for system, limit, t in rows:
        both = [f"{number(t[k]['p50'], 0)} / {number(t[k]['p95'], 0)}" for k in ['client_ms', 'elapsed_ms', 'query_encoding_ms', 'index_query_ms', 'hydration_ms', 'plugin_rounds_ms']]
        share = '—' if t['query_encoding_share'] is None else f"{t['query_encoding_share']:.0%}"
        lines.append(f'| {system} | {limit} | ' + ' | '.join(both) + f" | {share} | {t['failures']}/{t['searches'] + t['failures']} |")
    return lines + ['']


def accounting_table(systems):
    rows = [(system, value, phase, usage) for system, value in systems.items()
            for phase, usage in value.get('accounting', {}).items() if value.get('reranker')]
    if not rows:
        return []
    lines = ['### Reranker accounting', '',
             'Scored searches exclude profile probes and startup warmups. Paid live runs have no paid probes, warmups or replay. '
             'Each variant restarts the sidecar before its scored pass. Historical/offline replay rows reuse that same sidecar. '
             'The bounded-cache replay is not an all-hit warm measurement: the default 4096-entry cache can evict '
             'pairs during the full scored pass and sequential replay, so its hit rate and spend are measured, not assumed. '
             'Spend includes paid work recorded for failed searches. Missing telemetry is unknown, not zero. '
             'Actual tokens are provider-reported input tokens; reserved tokens are failed or unconfirmed attempts. '
             'Cents/search uses pinned client pricing, not a billing receipt; ≤ marks an upper bound including reservations. '
             'Cache hits are divided by candidate pairs. Fallback searches measure hybrid order, not Jev quality; '
             'a payment refusal never supplies a Jev score.', '',
             '| System | Pass | K | Trim | Fusion | nDCG@10 | Recall@10 | p50 ms | p95 ms | Actual tokens/search | Reserved tokens/search | Cents/search | Paid calls/search | Fallback rate | Cache hit rate | Log records/searches |',
             '| --- ' * 16 + '|']
    reasons = []
    for system, value, phase, usage in rows:
        config = value['reranker']
        scored = phase == 'scored'
        timing = value['latency_ms'] if scored else value['warm_cache_timing']['client_ms']
        quality = ' | '.join(number(value['mean'][metric]) if scored else '—' for metric in ['ndcg@10', 'recall@10'])
        rates = ['—' if usage.get(field) is None else f"{usage[field]:.1%}" for field in ['fallback_rate', 'cache_hit_rate']]
        cost = ('≤ ' if usage.get('cost_is_upper_bound') else '') + number(usage.get('cost_cents_per_search'), 4)
        lines.append(f"| {system} | {'cold/scored' if scored else 'bounded-cache replay'} | {config['k']} | {config['trim']} | {config['ranking']} | {quality} | "
                     f"{number(timing['p50'], 0)} | {number(timing['p95'], 0)} | {number(usage.get('tokens_per_search'), 1)} | "
                     f"{number(usage.get('estimated_tokens_per_search'), 1)} | {cost} | {number(usage.get('paid_calls_per_search'), 2)} | "
                     f"{rates[0]} | {rates[1]} | {usage.get('log_records', 0)}/{usage['searches']} |")
        if usage.get('fallback_reasons'):
            reasons.append(f"Fallback reasons ({system}, {phase}): " + ', '.join(
                f"{'provider refused (payment)' if reason == 'HTTP 402' else reason} {count}"
                for reason, count in usage['fallback_reasons'].items()) + '.')
    return lines + [''] + reasons + ([''] if reasons else [])


def resources(r):
    """Disk and memory at each step of a local-stack run, and Weaviate's own disk and memory lines (THE-878)."""
    res = r.get('resources')
    if not res:
        return []
    lines = ['## Machine resources', '', 'Weaviate refuses every write once the disk under its data passes 90%. '
             'Memory is each container\'s usage, and the resident memory of the engine\'s api and worker processes.', '',
             '| Step | Docker disk used | Free | Images | Volumes | Build cache | Memory available | Memory |', '| --- ' * 8 + '|']
    for s in res['snapshots']:
        d, st = s.get('docker_disk', {}), s.get('docker_storage', {})
        used = '—' if 'used_percent' not in d else f"{d['used_percent']}%"
        free = '—' if 'free_gb' not in d else f"{d['free_gb']} GB"
        memory = ', '.join(f'{k} {v}' for k, v in s.get('memory', {}).items()) or s.get('error', '—')
        available = '—' if s.get('memory_available_mb') is None else f"{s['memory_available_mb']} MB"
        lines.append(f"| {s['label']} | {used} | {free} | {st.get('Images', '—')} | {st.get('Local Volumes', '—')} "
                     f"| {st.get('Build Cache', '—')} | {available} | {memory} |")
    lines.append('')
    for stack, entries in res.get('weaviate', {}).items():
        shown = [e for e in entries if e['action'] == 'set_shard_read_only']
        shown += [e for e in entries if e['action'] in ('read_disk_use', 'read_memory_use')][-1:]
        lines += [f"Weaviate of `{stack}`: {e.get('time')} {e.get('msg')}" for e in shown] + ([''] if shown else [])
    return lines


def embedding_budget(r):
    campaign = r.get('embedding_campaign')
    if not campaign:
        return []
    b = campaign['budget']
    prior = campaign.get('prior_cost_upper_bound_usd', 0)
    lines = ['## Embedding campaign budget', '',
             f"Admission cap: {b['max_input_tokens']:,} input tokens and ${b['max_usd']:.2f} in this dispatch. "
             f"Confirmed input: {b['confirmed_input_tokens']:,}; reserved/unconfirmed: {b['reserved_input_tokens']:,}. "
             f"Confirmed priced cost: ${b['confirmed_cost_usd']:.6f}; cost upper bound: ${b['cost_upper_bound_usd']:.6f}. "
             f"Prior dispatch: ${prior:.6f}; combined upper bound: ${prior + b['cost_upper_bound_usd']:.6f} "
             f"of campaign ${campaign['campaign_cap_usd']:.2f}. Blocked calls: {b['blocked_calls']}.", '',
             'Every provider attempt, including retries and query calls, reserves a UTF-8 byte token bound before forwarding. '
             'Missing or invalid usage retains the reservation; cap exhaustion stops new calls and preserves completed cuts. '
             'Unattempted cuts carry no scores. Prices are dated provider list prices, estimates rather than Azure billing receipts. '
             'Hybrid fusion uses the unchanged default configuration.', '']
    for candidate in campaign['candidates']:
        p = candidate['price']
        lines.append(f"Price for `{candidate['model']}`: ${p['usd_per_million_tokens']:.2f}/million input tokens, "
                     f"checked [{p['date']}]({p['source']}).")
    return lines + ['']


def embedding_table(sets):
    rows = [(name, s) for name, s in sets.items() if 'embedding_model' in s]
    if not rows:
        return []
    lines = ['## Embedding indexing and cost', '',
             'Indexing time runs from first submission until both core.ingest and the evaluation owner cover the Corpus; '
             'unfinished indexing shows elapsed time only. Indexing USD / 1,000 documents excludes query calls; '
             'total USD includes queries and failed attempts. ≤ includes unconfirmed reservations. '
             'The local core.ingest baseline has no provider charge. Each variant uses a new Corpus with the same sample.', '',
             '| Set | Deployment | Dimensions | Segment limit | State | Indexing s | Index input tokens | Reserved tokens | Index USD / 1,000 documents | Total USD |',
             '| --- ' * 10 + '|']
    for name, s in rows:
        c = s['embedding_model']
        index = s.get('embedding_indexing', {})
        total = s.get('embedding_total', index)
        cost = index.get('cost_upper_bound_usd')
        normalized = cost * 1000 / s['documents'] if cost is not None and s['documents'] and s.get('status') != 'indexing' else None
        elapsed = s['ingestion'].get('paired_vectors_seconds', s['ingestion'].get('partial_seconds'))
        lines.append(f"| {name} | {c['model']} | {c['dimensions']} | {c['segment_tokens']} | {s.get('status', 'completed')} | "
                     f"{number(elapsed, 1)} | {index.get('confirmed_input_tokens', '—')} | {index.get('reserved_input_tokens', '—')} | "
                     f"≤ {number(normalized, 6)} | ≤ {number(total.get('cost_upper_bound_usd'), 6)} |")
    return lines + ['']


def embedding_comparisons(s):
    comparisons = s.get('against_served_mode')
    if not comparisons:
        return []
    lines = ['Paired against core.ingest in the same mode, on the same queries and documents:', '',
             '| Evaluation system | Served baseline | Δ nDCG@10 | Δ Recall@10 | Δ MRR@10 |', '| --- ' * 5 + '|']
    for system, metrics in comparisons.items():
        baseline = system.split('/', 1)[0] + '/default'
        lines.append(f'| {system} | {baseline} | ' + ' | '.join(delta(metrics.get(k)) for k, _ in METRIC_NAMES) + ' |')
    return lines + ['']


def markdown(r):
    run = r['run']
    lines = ['# Search quality evaluation', '',
             f"Status: **{r['status']}** · source `{run.get('source_revision', '')}`{' (dirty)' if run.get('source_dirty') else ''} · "
             f"{run.get('finished_at', '')} · {run.get('duration_seconds', 0):.0f} s · {run.get('target', '')}", '']
    if r['status'] != 'completed':
        lines += ['Error: `' + r.get('error', '') + '`', '']
        if (r.get('resources') or {}).get('cause'):
            lines += ['Cause: ' + r['resources']['cause'] + '.', '']
        lines += [ 'Partial results: ' + (', '.join(r['sets']) or 'none'), '']
    budget = run.get('jev_budget')
    if budget:
        lines += ['## Paid run budget', '',
                  f"Admission cap: {budget['max_input_tokens']:,} input tokens and {budget['max_paid_searches']} searches. "
                  f"Actual input: {budget['actual_input_tokens']:,}; reserved/unconfirmed: {budget['reserved_input_tokens']:,}; "
                  f"actual priced cost: {budget['actual_cost_cents']:.4f} cents; cost upper bound: {budget['cost_upper_bound_cents']:.4f} cents. "
                  f"Blocked searches: {budget['blocked_searches']}. Reservations include all three possible attempts; missing telemetry is not zero.", '']
    lines += embedding_budget(r)
    h = run.get('host', {})
    lines += [f"Host: {h.get('cpu_model', h.get('machine', ''))}, {h.get('logical_cpus')} logical CPUs. "
              'Default runs in every mode; paid deep profiles run only hybrid. Paid matrix runs score both profiles '
              'at API limit 10 before Record deduplication; no-key runs retain the historical default scored limit 50. '
              'Optional private sets are excluded from paid probes and matrices and retain unpaid default measurement. '
              'Hits are deduplicated by Record. All systems are scored at shared cutoffs @10: nDCG@10, Recall@10, '
              'and MRR@10. Limit 50 is a retrieval page size, not a Recall@50 score. '
              'Paired comparisons with past runs omit systems whose scored API page sizes differ.', '']
    lines += comparison(r)
    if r.get('convention'):
        lines += [f"Scoring: {r['convention']} Significance: {r['test']}; `*` marks p < 0.05.", '']
    if r['sets']:
        lines += ['## Sets', '', '| Set | Queries | Documents | Sample | Licence | Ingestion to vectors (s) |', '| --- | --- | --- | --- | --- | --- |']
        for name, s in r['sets'].items():
            m, sample = s['manifest'], s['manifest'].get('sample', {})
            how = f"seed {sample['seed']}, {sample.get('eligible_queries')} eligible queries" if sample else 'as given'
            elapsed = s['ingestion'].get('searchable_with_vectors_seconds')
            lines.append(f"| {name} | {s['queries']} | {s['documents']} | {how} | {m.get('licence', 'private')} | {number(elapsed, 0)} |")
        lines.append('')
    lines += embedding_table(r['sets'])
    for name, s in r['sets'].items():
        p = s.get('profiles', {})
        lines += [f'## {name}', '']
        if s.get('status') and s['status'] != 'completed':
            lines += [f"Unfinished set: {s['status']}. Only fully completed query cuts are scored.", '']
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
        lines += time_table(s['systems'])
        lines += accounting_table(s['systems'])
        lines += embedding_comparisons(s)
        if 'against_baseline_run' in s:
            if s['against_baseline_run'] is None:
                lines += ['Not compared with the baseline run: that run did not measure this exact sample.', '']
            else:
                b = r.get('baseline_run') or {}
                where = f"at `{b['ref']}`" if b.get('ref') else f"in run {(b.get('github') or {}).get('GITHUB_RUN_ID') or b.get('id')}"
                lines += [f"Against the same system {where} (source `{b.get('source_revision', '')}`){', measured in this run' if b.get('ref') else ''}:", '',
                          '| System | ' + ' | '.join('Δ ' + n for _, n in METRIC_NAMES) + ' |', '| --- ' * (len(METRIC_NAMES) + 1) + '|']
                for system, c in s['against_baseline_run'].items():
                    lines.append(f'| {system} | ' + ' | '.join(delta(c[k]) for k, _ in METRIC_NAMES) + ' |')
                lines.append('')
    lines += resources(r)
    phases = run.get('phases_seconds')
    if phases:
        lines += ['## Stack phases (seconds)', '', ' · '.join(f'{k} {v:.0f}' for k, v in phases.items()), '']
    return '\n'.join(lines) + '\n'
