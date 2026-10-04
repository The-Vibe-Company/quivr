"""Scores rankings with ranx and compares two systems with a paired test (THE-775).

Needs ranx (and scipy, one of its dependencies): pip install -r scripts/eval/requirements.txt.
"""
import math

METRICS = ['ndcg@10', 'recall@10', 'mrr@10']
ALPHA = 0.05
CONVENTION = ('nDCG@10 uses linear gain (gain = relevance grade) and a log2(rank + 1) discount, the '
              'Järvelin-Kekäläinen form of trec_eval ndcg_cut_10 and ranx "ndcg"; the ideal ranking holds '
              'every judged document. Recall@10 and MRR@10 count grades above 0 as relevant. A query '
              'with no result scores 0.')
TEST = ('two-sided paired Student t-test on per-query scores, alpha 0.05, no correction for multiple '
        'comparisons')


def score(qrels, ranking):
    """{'mean': {metric: x}, 'per_query': {metric: {qid: x}}} for one system on one set.

    qrels: {qid: {doc: grade}}; ranking: {qid: [doc, ...]} best first. Every judged query is
    scored, including those the system returned nothing for."""
    from ranx import Qrels, Run, evaluate
    run = Run({q: {d: 1 / (i + 1) for i, d in enumerate(ranking.get(q, []))} for q in qrels}, name='system')
    evaluate(Qrels(qrels), run, METRICS)
    per_query = {m: {q: float(run.scores[m][q]) for q in sorted(qrels)} for m in METRICS}
    return {'mean': {m: sum(v.values()) / len(v) for m, v in per_query.items()}, 'per_query': per_query}


def paired(candidate, baseline):
    """Mean difference and p-value of candidate minus baseline over their common queries."""
    from scipy.stats import ttest_rel
    queries = sorted(set(candidate) & set(baseline))
    a, b = [candidate[q] for q in queries], [baseline[q] for q in queries]
    delta = sum(x - y for x, y in zip(a, b)) / len(queries) if queries else None
    p = float(ttest_rel(a, b).pvalue) if len(queries) > 1 else None
    if p is not None and math.isnan(p):
        p = 1.0  # identical per-query scores: no difference to detect
    return {'queries': len(queries), 'delta': delta, 'p_value': p, 'significant': p is not None and p < ALPHA}
