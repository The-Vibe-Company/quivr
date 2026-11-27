"""Scores the pinned top-ten metrics and pairs systems per query (THE-775).

Paired significance needs scipy: pip install -r scripts/eval/requirements.txt.
"""
import math
from collections import Counter

METRICS = ['ndcg@10', 'recall@10', 'mrr@10']
DIAGNOSTIC_METRICS = ['recall@50', 'judged@10']
ALPHA = 0.05
CONVENTION = ('nDCG@10 uses linear gain (gain = relevance grade) and a log2(rank + 1) discount, the '
              'Järvelin-Kekäläinen form of trec_eval ndcg_cut_10 and ranx "ndcg"; the ideal ranking holds '
              'every judged document. Recall@10 and MRR@10 count grades above 0 as relevant. A query '
              'with no result scores 0.')
TEST = ('two-sided paired Student t-test on per-query scores, alpha 0.05, no correction for multiple '
        'comparisons')


def ranked_docs(hits):
    # Match Run's doc -> reciprocal-rank mapping, including duplicates:
    # the last occurrence supplies a document's score.
    positions = {doc: i for i, doc in enumerate(hits)}
    return sorted(positions, key=positions.get)


def score(qrels, ranking, *, diagnostics=False):
    """{'mean': {metric: x}, 'per_query': {metric: {qid: x}}} for one system on one set.

    qrels: {qid: {doc: grade}}; ranking: {qid: [doc, ...]} best first. Every judged query is
    scored, including those the system returned nothing for. With diagnostics enabled,
    Judged@10 is the share of ten positions occupied by explicitly judged documents (any grade).
    Means are unavailable when there are no scored queries."""
    metrics = METRICS + DIAGNOSTIC_METRICS if diagnostics else METRICS
    per_query = {m: {} for m in metrics}
    discounts = [math.log2(i + 2) for i in range(10)]
    for query in sorted(qrels):
        judged = qrels[query]
        top50 = ranked_docs(ranking.get(query, []))[:50]
        docs = top50[:10]
        gains = [judged.get(doc, 0) for doc in docs]
        ideal = sorted(judged.values(), reverse=True)[:10]
        dcg = sum(gain / discount for gain, discount in zip(gains, discounts))
        idcg = sum(gain / discount for gain, discount in zip(ideal, discounts))
        relevant = sum(grade > 0 for grade in judged.values())
        per_query['ndcg@10'][query] = dcg / idcg if idcg else 0.
        per_query['recall@10'][query] = sum(gain > 0 for gain in gains) / relevant if relevant else 0.
        per_query['mrr@10'][query] = next((1 / (i + 1) for i, gain in enumerate(gains) if gain > 0), 0.)
        if diagnostics:
            per_query['recall@50'][query] = sum(judged.get(doc, 0) > 0 for doc in top50) / relevant if relevant else 0.
            per_query['judged@10'][query] = sum(doc in judged for doc in docs) / 10
    return {'mean': {m: sum(v.values()) / len(v) if v else None for m, v in per_query.items()},
            'per_query': per_query}


def diagnose_errors(qrels, ranking, metadata, k=10):
    """Count ranked hits for the primary queries and attribute categorized non-relevant hits.

    Grades >= 2 are relevant, grade 1 is partial, and grades <= 0 are irrelevant.
    An absent judgment is unjudged. Only irrelevant and unjudged hits contribute
    to categories, and only when an explicit category is available.
    """
    if k < 0:
        raise ValueError('k must be non-negative')
    statuses = dict.fromkeys(('relevant', 'partial', 'irrelevant', 'unjudged'), 0)
    categories = Counter()
    for query, judged in qrels.items():
        entries = metadata.get(query, {})
        for doc in ranked_docs(ranking.get(query, []))[:k]:
            grade = judged.get(doc)
            if grade is None:
                status = 'unjudged'
            elif grade <= 0:
                status = 'irrelevant'
            else:
                status = 'partial' if grade == 1 else 'relevant'
            statuses[status] += 1
            category = entries.get(doc, {}).get('category')
            if status in ('irrelevant', 'unjudged') and category:
                categories[category] += 1
    return {'cutoff': k, 'statuses': statuses, 'categories': dict(sorted(categories.items()))}


def score_set(data, ranking):
    """Score a loaded TREC set into JSON-ready system fields for the report.

    Auxiliary tiers score primary evaluation queries with a positive tier judgment.
    Their negative grades count as judged non-relevant with zero gain.
    """
    qrels = data['qrels']
    result = score(qrels, ranking, diagnostics=True)
    if 'auxiliary_qrels' in data:
        tiers = {}
        for tier, judgments in data['auxiliary_qrels'].items():
            judged = {query: {doc: max(grade, 0) for doc, grade in docs.items()}
                      for query, docs in judgments.items()
                      if query in qrels and any(grade > 0 for grade in docs.values())}
            if judged:
                tiers[tier] = {'queries': len(judged), **score(judged, ranking, diagnostics=True)}
        result['auxiliary_qrels'] = tiers
    if 'judgment_metadata' in data:
        result['error_diagnostics'] = diagnose_errors(qrels, ranking, data['judgment_metadata'])
    return result


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
