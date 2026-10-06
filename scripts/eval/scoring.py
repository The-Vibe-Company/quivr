"""Scores the pinned top-ten metrics and pairs systems per query (THE-775).

Paired significance needs scipy: pip install -r scripts/eval/requirements.txt.
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
    # These three fixed top-ten metrics need no JIT or worker pool. Avoid
    # compiling ranx/Numba in every single-use measurement container.
    per_query = {m: {} for m in METRICS}
    discounts = [math.log2(i + 2) for i in range(10)]
    for query in sorted(qrels):
        judged = qrels[query]
        # Match Run's doc -> reciprocal-rank mapping, including duplicates:
        # the last occurrence supplies a document's score.
        positions = {doc: i for i, doc in enumerate(ranking.get(query, []))}
        docs = sorted(positions, key=positions.get)[:10]
        gains = [judged.get(doc, 0) for doc in docs]
        ideal = sorted(judged.values(), reverse=True)[:10]
        dcg = sum(gain / discount for gain, discount in zip(gains, discounts))
        idcg = sum(gain / discount for gain, discount in zip(ideal, discounts))
        relevant = sum(grade > 0 for grade in judged.values())
        per_query['ndcg@10'][query] = dcg / idcg if idcg else 0.
        per_query['recall@10'][query] = sum(gain > 0 for gain in gains) / relevant if relevant else 0.
        per_query['mrr@10'][query] = next((1 / (i + 1) for i, gain in enumerate(gains) if gain > 0), 0.)
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
