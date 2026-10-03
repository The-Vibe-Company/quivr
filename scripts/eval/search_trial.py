"""Direct search measurement; immutable embedding cache and fresh search latency.

Network adapters are runner-owned so every paid attempt crosses the shared
admission ledger. Only explicit dev data is accepted by this tier.
"""
import collections
import hashlib
import json
import math
import pathlib
import re
import socket
import sys
import time
import urllib.error
import urllib.request

import direct_bakeoff as direct
import embeddings
import results
import scoring

ROOT = pathlib.Path(__file__).resolve().parents[2]


def digest(value):
    return hashlib.sha256(results.encode(value).encode()).hexdigest()


def configuration(value):
    defaults = {'model': direct.E5_MODEL, 'revision': direct.E5_REVISION, 'dimensions': 384,
                'window_chars': 1800, 'overlap_chars': 200, 'dense_weight': 1,
                'candidate_count': 30, 'reranker': 'none'}
    if not isinstance(value, dict) or set(value) - set(defaults):
        raise ValueError('unknown search configuration fields')
    cfg = {**defaults, **value}
    if cfg['model'] != direct.E5_MODEL and cfg['model'] not in direct.PRICES:
        raise ValueError('unsupported embedding model')
    if cfg['model'] != direct.E5_MODEL and 'revision' not in value:
        raise ValueError('hosted configuration requires a deployment revision')
    if not isinstance(cfg['revision'], str) or not re.fullmatch(r'[A-Za-z0-9_.-]+', cfg['revision']):
        raise ValueError('revision must be a deployment version, without secrets')
    for field in ('dimensions', 'window_chars', 'overlap_chars', 'candidate_count'):
        if type(cfg[field]) is not int:
            raise ValueError('configuration dimensions/windows/depth must be integers')
    if (not 1 <= cfg['dimensions'] <= 4096 or cfg['window_chars'] <= cfg['overlap_chars']
            or cfg['overlap_chars'] < 0 or not 10 <= cfg['candidate_count'] <= 1000):
        raise ValueError('invalid dimensions, window or candidate depth')
    if (type(cfg['dense_weight']) not in (int, float) or not math.isfinite(cfg['dense_weight'])
            or not 0 <= cfg['dense_weight'] <= 1 or cfg['reranker'] not in ('none', 'jev')):
        raise ValueError('invalid fusion weight or reranker')
    if cfg['model'] == direct.E5_MODEL and (cfg['revision'] != direct.E5_REVISION or cfg['dimensions'] != 384):
        raise ValueError('local baseline model revision and dimensions are pinned')
    return cfg


def bm25(docs, query):
    terms = [collections.Counter(re.findall(r'\w+', text.casefold())) for text in docs]
    lengths = [sum(t.values()) for t in terms]
    average = sum(lengths) / len(lengths) or 1
    scores = [0.] * len(docs)
    for word in set(re.findall(r'\w+', query.casefold())):
        df = sum(word in t for t in terms)
        idf = math.log(1 + (len(docs) - df + .5) / (df + .5))
        for i, term in enumerate(terms):
            tf = term[word]
            scores[i] += idf * tf * 2.2 / (tf + 1.2 * (.25 + .75 * lengths[i] / average))
    return scores


def rank(docs, doc_ids, query, query_vector, piece_vectors, owners, cfg):
    """Best-piece exact cosine and weighted reciprocal-rank BM25 fusion."""
    import numpy as np
    order = lambda values: sorted(range(len(doc_ids)), key=lambda i: (-values[i], doc_ids[i]))
    alpha = cfg['dense_weight']
    if alpha == 0:
        return [doc_ids[i] for i in order(bm25(docs, query))[:cfg['candidate_count']]]
    scores = direct.normalize([query_vector]) @ direct.normalize(piece_vectors).T
    dense = np.full(len(docs), -np.inf, dtype=np.float32)
    np.maximum.at(dense, owners, scores[0])
    dense_order = order(dense)
    if alpha == 1:
        selected = dense_order
    else:
        lexical_order = order(bm25(docs, query))
        fused = [0.] * len(docs)
        for weight, ranking in ((alpha, dense_order), (1 - alpha, lexical_order)):
            for position, index in enumerate(ranking, 1):
                fused[index] += weight / (60 + position)
        selected = order(fused)
    return [doc_ids[i] for i in selected[:cfg['candidate_count']]]


def rerank(query, passages, budget, key, price):
    """One bounded Jev attempt, no hidden retry/background transport."""
    sys.path.insert(0, str(ROOT / 'plugins/jev-rerank'))
    from jev_rerank import client as jev
    raw = json.dumps(jev.payload(query, passages), ensure_ascii=False, separators=(',', ':')).encode()
    if len(raw) > jev.MAX_BYTES:
        raise ValueError('reranker request exceeds its supported bound')
    call = budget.reserve(jev.MODEL, 'rerank', 'query', jev.MAX_TOKENS, price)
    request = urllib.request.Request(jev.URL, data=raw, headers={'Content-Type': 'application/json',
                                     'Authorization': 'Bearer ' + key}, method='POST')
    try:
        with urllib.request.build_opener(embeddings.NoRedirect()).open(request, timeout=60) as response:
            answer = response.read(jev.MAX_RESPONSE_BYTES + 1)
        if len(answer) > jev.MAX_RESPONSE_BYTES:
            raise ValueError()
        body = json.loads(answer)
        tokens = body['usage']['input_tokens']
        budget.settle(call, tokens)
        scores = {name: item['noul'] for name, item in body['answers'].items()}
        if (type(tokens) is not int or not 0 <= tokens <= jev.MAX_TOKENS or body['model'] != jev.MODEL
                or set(scores) != set(passages) or any(type(v) not in (int, float) or not math.isfinite(v) or not 0 <= v <= 1 for v in scores.values())):
            raise ValueError()
        return sorted(scores, key=lambda name: (-scores[name], name))
    except embeddings.BudgetExceeded:
        raise
    except Exception:
        raise RuntimeError('reranker attempt failed; uncertain charge retained') from None


def measure(cfg, data, dataset, cache, budget, hosted, prices, compute_rate,
            fresh_latency=True, rerank_key='', flush=lambda: None):
    if dataset['split'] != 'dev' or dataset['private']:
        raise PermissionError('tier 1 accepts public campaign-dev data only')
    cfg = configuration(cfg)
    indexing_started = time.monotonic()
    document_embedding_seconds = 0
    doc_ids, query_ids = sorted(data['corpus']), sorted(data['qrels'])
    docs = [(data['corpus'][d].get('title', '') + '\n' if data['corpus'][d].get('title') else '')
            + data['corpus'][d]['text'] for d in doc_ids]
    if not docs or not query_ids:
        raise ValueError('empty measurement set')
    cache = pathlib.Path(cache)
    cache.mkdir(parents=True, exist_ok=True)
    local = direct.E5()
    def embed(texts, mode):
        if cfg['model'] == direct.E5_MODEL:
            return direct.normalize(local.embed(texts, mode)).tolist()
        vectors = hosted.embed(cfg['model'], texts, mode, dimensions=cfg['dimensions'])
        if budget.summary()['reserved_input_tokens']:
            raise RuntimeError('provider omitted confirmed usage; measurement rejected')
        return direct.normalize(vectors).tolist()

    identity = {k: cfg[k] for k in ('model', 'revision', 'dimensions', 'window_chars', 'overlap_chars')}
    entries, misses, cache_hits = {}, [], 0
    semantic = cfg['dense_weight'] > 0
    for mode, texts in (('document', docs if semantic else []),
                        ('query', [data['queries'][q] for q in query_ids] if semantic and not fresh_latency else [])):
        for text in dict.fromkeys(texts):
            cache_key = 'embedding/' + digest({'config': identity, 'mode': mode, 'text_hash': hashlib.sha256(text.encode()).hexdigest()})
            # Outlive the maximum bounded Modal invocation, including all batches.
            claim = budget.store.claim(budget.campaign, cache_key, ttl=86400)
            if claim['status'] == 'leased':
                raise RuntimeError('embedding cache fill already leased; retry after completion')
            if claim['status'] == 'done':
                meta = claim['payload']
                path = cache / meta['filename']
                if not path.exists():
                    raise RuntimeError('committed embedding cache unavailable; refusing duplicate work')
                entry = json.loads(path.read_text())
                if digest(entry) != meta['digest']:
                    raise RuntimeError('embedding cache digest mismatch')
                entries[(mode, text)] = entry
                cache_hits += 1
            else:
                pieces = direct.split_documents([text], cfg['window_chars'], cfg['overlap_chars'])[0] if mode == 'document' else [text]
                misses.append((mode, text, pieces, cache_key, claim['owner']))
    for mode in ('document', 'query'):
        pending = [item for item in misses if item[0] == mode]
        if not pending:
            continue
        texts = [piece for item in pending for piece in item[2]]
        before = budget.summary()['confirmed_input_tokens']
        started = time.monotonic()
        vectors = embed(texts, mode)
        seconds = time.monotonic() - started
        if mode == 'document':
            document_embedding_seconds += seconds
        tokens = budget.summary()['confirmed_input_tokens'] - before
        bound = embeddings.estimate_tokens(texts)
        offset = 0
        for _, text, pieces, cache_key, owner in pending:
            fraction = embeddings.estimate_tokens(pieces) / bound
            entry = {'vectors': vectors[offset:offset + len(pieces)], 'tokens': tokens * fraction,
                     'embedding_seconds': seconds * fraction}
            filename = digest([cache_key, owner]) + '.json'
            results.save(cache / filename, entry)
            flush()
            budget.store.publish(budget.campaign, cache_key, owner, {'filename': filename, 'digest': digest(entry)})
            entries[(mode, text)] = entry
            offset += len(pieces)
    piece_vectors, owners, index_tokens, index_seconds = [], [], 0., 0.
    for owner, text in enumerate(docs):
        if not semantic:
            break
        entry = entries[('document', text)]
        piece_vectors.extend(entry['vectors'])
        owners.extend([owner] * len(entry['vectors']))
        index_tokens += entry['tokens']
        index_seconds += entry['embedding_seconds']
    # Include chunking/cache processing; do not charge fresh embedding time twice.
    index_seconds += max(0, time.monotonic() - indexing_started - document_embedding_seconds)
    ranking, latencies, query_prices = {}, [], []
    provider_rate = prices.get(cfg['model'], 0) / 1_000_000
    # One deterministic, budgeted warmup exercises the same serving path on both
    # configurations. Its charges are in the ledger, outside per-search metrics.
    warmup = [query_ids[0]] if fresh_latency else []
    for position, qid in enumerate(warmup + query_ids):
        budget.store.renew(budget.campaign, *budget.lease)
        query = data['queries'][qid]
        before = budget.summary()['confirmed_cost_usd']
        started = time.monotonic()
        if not semantic:
            vector, query_tokens = None, 0
        elif fresh_latency:
            vector = embed([query], 'query')[0]
        else:
            entry = entries[('query', query)]
            vector = entry['vectors'][0]
            query_tokens = entry['tokens']
        selected = rank(docs, doc_ids, query, vector, piece_vectors, owners, cfg)
        if cfg['reranker'] == 'jev':
            if not rerank_key:
                raise ValueError('reranker secret is absent')
            selected = rerank(query, {d: docs[doc_ids.index(d)] for d in selected}, budget, rerank_key, prices['jev-1.13.0'])
        elapsed = time.monotonic() - started
        if position < len(warmup):
            continue
        ranking[qid] = selected[:10]
        latencies.append(1000 * elapsed)
        spend = budget.summary()['confirmed_cost_usd'] - before
        # Cached query accounting is repriced, never zeroed by avoiding a call.
        query_prices.append(spend + (0 if fresh_latency or not semantic else query_tokens * provider_rate)
                            + (elapsed + (0 if fresh_latency or not semantic else entry['embedding_seconds'])) * compute_rate)
    scores = scoring.score(data['qrels'], ranking)
    ordered = sorted(latencies)
    percentile = lambda fraction: ordered[max(0, math.ceil(len(ordered) * fraction) - 1)]
    return {'dataset': dataset, 'metrics': {**scores['mean'],
                'latency_p50_ms': percentile(.5) if fresh_latency else None,
                'latency_p95_ms': percentile(.95) if fresh_latency else None,
                'cost_per_search_usd': sum(query_prices) / len(query_prices),
                'cost_per_1000_documents_usd': (index_tokens * provider_rate + index_seconds * compute_rate) * 1000 / len(docs)},
            'per_query': scores['per_query'], 'cost': {'provider': budget.summary(),
                'index_tokens_attributed': index_tokens, 'index_embedding_seconds_attributed': index_seconds,
                'cache_hits': cache_hits, 'latency_method': 'serial fresh query embedding+retrieval+rerank; one fixed first-query warmup' if fresh_latency else 'cached exploration; p95 unavailable',
                'price_basis': 'frozen rates, original embedding usage; includes attributable compute'},
            'machine': socket.gethostname()}


def record(measured, cfg, experiment, sha, scorer_digest):
    return {'schema_version': 1, 'experiment': experiment, 'git_sha': sha,
            'plugin_digest': scorer_digest, 'config': cfg, 'tier': 'direct',
            'machine': measured['machine'], 'duration_seconds': measured.get('duration_seconds'),
            'dataset': measured['dataset'], 'metrics': measured['metrics'],
            'per_query': measured['per_query'], 'cost': measured['cost']}
