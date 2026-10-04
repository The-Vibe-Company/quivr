"""Pure helpers for the frozen retrieval measurement protocol (THE-661)."""
import hashlib
import json
import math
import pathlib
import random


def percentile(values, p):
    """Nearest-rank percentile; None when there is no successful sample."""
    if not values:
        return None
    ordered = sorted(values)
    return ordered[max(1, math.ceil(p / 100 * len(ordered))) - 1]


def rank_of(hits, record_id):
    """1-based rank of the first hit belonging to the relevant Record, 0 when absent."""
    for i, hit in enumerate(hits):
        if hit['record_id'] == record_id:
            return i + 1
    return 0


def quality(ranks):
    """MRR@10 and Recall@3/@10 over one binary judgment per query."""
    n = len(ranks)
    within = [r for r in ranks if 0 < r <= 10]
    return {
        'queries': n,
        'mrr_at_10': sum(1 / r for r in within) / n,
        'recall_at_3': sum(1 for r in within if r <= 3) / n,
        'recall_at_10': len(within) / n,
    }


def latency_summary(samples, target_ms):
    """Summarize samples; failures are counted, excluded from percentiles, and fail the target."""
    ok = [s['ms'] for s in samples if s['ok']]
    failures = len(samples) - len(ok)
    p95 = percentile(ok, 95)
    return {
        'samples': len(samples),
        'failures': failures,
        'errors': sorted({s.get('error', '') for s in samples if not s['ok']}),
        'p50_ms': _round(percentile(ok, 50)),
        'p95_ms': _round(p95),
        'max_ms': _round(max(ok) if ok else None),
        'target_p95_ms_below': target_ms,
        'target_met': bool(ok) and failures == 0 and p95 < target_ms,
    }


def _round(v):
    return None if v is None else round(v, 2)


def schedule(query_ids, modes, passes, seed):
    """Seeded order: queries shuffled per pass, then all modes for each query in shuffled order."""
    rng = random.Random(seed)
    out = []
    for _ in range(passes):
        queries = list(query_ids)
        rng.shuffle(queries)
        for q in queries:
            order = list(modes)
            rng.shuffle(order)
            out.extend((q, mode) for mode in order)
    return out


def load_workload(path, root):
    """Load the frozen workload and its fixture; refuse any fixture that differs from the frozen hash."""
    path, root = pathlib.Path(path), pathlib.Path(root)
    workload = json.loads(path.read_text())
    raw = (root / workload['fixture']['path']).read_bytes()
    digest = hashlib.sha256(raw).hexdigest()
    if digest != workload['fixture']['sha256']:
        raise RuntimeError(f'fixture sha256 {digest} differs from frozen workload')
    rows = json.loads(raw)
    if len(rows) != workload['fixture']['queries']:
        raise RuntimeError(f"fixture has {len(rows)} queries, workload declares {workload['fixture']['queries']}")
    return workload, rows
