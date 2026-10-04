"""Direct search measurement; immutable embedding cache and fresh search latency.

Network adapters are runner-owned so every paid attempt crosses the shared
admission ledger. Only explicit dev data is accepted by this tier.
"""
import collections
import concurrent.futures
import copy
import hashlib
import json
import logging
import math
import pathlib
import re
import socket
import sys
import time
import urllib.error
import urllib.request

import control_store
import direct_bakeoff as direct
import embeddings
import results
import scoring

ROOT = pathlib.Path(__file__).resolve().parents[2]
LOG = logging.getLogger(__name__)
CACHE_CONCURRENCY = 4
LATENCY_SAMPLE_SIZE = 50
LATENCY_SAMPLE_POLICY = 'sha256-query-id-v1; max=50'


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
    doc_ids, query_ids = sorted(data['corpus']), sorted(data['qrels'])
    docs = [(data['corpus'][d].get('title', '') + '\n' if data['corpus'][d].get('title') else '')
            + data['corpus'][d]['text'] for d in doc_ids]
    if not docs or not query_ids:
        raise ValueError('empty measurement set')
    cache = pathlib.Path(cache)
    cache.mkdir(parents=True, exist_ok=True)
    local = direct.E5()
    def embed(texts, mode, task_budget=budget, client=hosted):
        if cfg['model'] == direct.E5_MODEL:
            return direct.normalize(local.embed(texts, mode)).tolist()
        vectors = client.embed(cfg['model'], texts, mode, dimensions=cfg['dimensions'])
        if task_budget.summary()['reserved_input_tokens']:
            raise RuntimeError('provider omitted confirmed usage; measurement rejected')
        return direct.normalize(vectors).tolist()

    identity = {k: cfg[k] for k in ('model', 'revision', 'dimensions', 'window_chars', 'overlap_chars')}
    entries, cache_hits, cache_fills = {}, 0, 0
    semantic = cfg['dense_weight'] > 0
    document_embedding_seconds = 0
    index_overhead_seconds = time.monotonic() - indexing_started
    for mode, texts in (('document', docs if semantic else []),
                        ('query', [data['queries'][q] for q in query_ids] if semantic else [])):
        mode_started = time.monotonic()
        unique = list(dict.fromkeys(texts))
        # Local inference stays serial; hosted requests overlap across a bounded
        # wave. Claims, file persistence, Volume commits and publication stay on
        # this thread. Never enqueue the entire dataset.
        concurrency = CACHE_CONCURRENCY if mode == 'document' and cfg['model'] != direct.E5_MODEL else 1
        wave_size = concurrency * control_store.LEASE_BATCH_SIZE
        for wave_start in range(0, len(unique), wave_size):
            wave = []
            for start in range(wave_start, min(wave_start + wave_size, len(unique)), control_store.LEASE_BATCH_SIZE):
                chunk = unique[start:start + control_store.LEASE_BATCH_SIZE]
                keyed = {'embedding/' + digest({'config': identity, 'mode': mode,
                         'text_hash': hashlib.sha256(text.encode()).hexdigest()}): text for text in chunk}
                started = time.monotonic()
                pending = []
                try:
                    # A cache validation failure also rolls back new claims in
                    # this chunk. No paid work starts before the wave validates.
                    with budget.store.claim_batch(budget.campaign, keyed, ttl=86400, require_available=True) as claims:
                        for cache_key, text in keyed.items():
                            claim = claims[cache_key]
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
                                pending.append((text, pieces, cache_key, claim['owner']))
                except control_store.LeaseBusy:
                    raise RuntimeError('embedding cache fill already leased; retry after completion') from None
                LOG.info('cache claims entries=%d elapsed_seconds=%.3f', len(keyed), time.monotonic() - started)
                if pending:
                    budget.store.renew_many(budget.campaign, {k: o for _, _, k, o in pending}, ttl=86400)
                    budget.store.renew(budget.campaign, *budget.lease)
                    wave.append(pending)
            if wave:
                task_budgets = [control_store.Budget(budget.store, budget.campaign, budget.lease) for _ in wave]
                def fill(item):
                    pending, task_budget = item
                    texts = [piece for _, pieces, _, _ in pending for piece in pieces]
                    started = time.monotonic()
                    if cfg['model'] == direct.E5_MODEL:
                        vectors = embed(texts, mode)
                    else:
                        # Usage and unknown reservations belong to this task,
                        # never a delta of a concurrently changing global sum.
                        client = copy.copy(hosted)
                        client.budget = task_budget
                        vectors = embed(texts, mode, task_budget, client)
                    return vectors, task_budget.summary()['confirmed_input_tokens'], time.monotonic() - started
                started = time.monotonic()
                try:
                    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
                        futures = [pool.submit(fill, item) for item in zip(wave, task_budgets)]
                        filled = [future.result() for future in futures]
                finally:
                    # Executor exit drains admitted attempts even on failure.
                    # Include their confirmed and uncertain charges in evidence.
                    for task_budget in task_budgets:
                        budget.calls.extend(task_budget.calls)
                seconds = time.monotonic() - started
                if mode == 'document':
                    document_embedding_seconds += seconds
                # Attribute overlapping provider time as wall time, without
                # charging parallel durations multiple times. Cached metadata
                # retains this original attribution for later repricing.
                total_seconds = sum(item[2] for item in filled)
                for pending, (vectors, tokens, task_seconds) in zip(wave, filled):
                    texts = [piece for _, pieces, _, _ in pending for piece in pieces]
                    bound = embeddings.estimate_tokens(texts)
                    attributed_seconds = seconds * task_seconds / total_seconds if total_seconds else 0
                    offset, publication = 0, {}
                    for text, pieces, cache_key, owner in pending:
                        fraction = embeddings.estimate_tokens(pieces) / bound
                        entry = {'vectors': vectors[offset:offset + len(pieces)], 'tokens': tokens * fraction,
                                 'embedding_seconds': attributed_seconds * fraction}
                        filename = digest([cache_key, owner]) + '.json'
                        results.save(cache / filename, entry)
                        publication[cache_key] = (owner, {'filename': filename, 'digest': digest(entry)})
                        entries[(mode, text)] = entry
                        offset += len(pieces)
                    # SQL references files only after this chunk is durable.
                    flush()
                    budget.store.publish_many(budget.campaign, publication)
                    cache_fills += len(pending)
            LOG.info('cache progress processed=%d total=%d hits=%d filled=%d',
                     min(wave_start + wave_size, len(unique)), len(unique), cache_hits, cache_fills)
        if mode == 'document':
            index_overhead_seconds += max(0, time.monotonic() - mode_started - document_embedding_seconds)
    assembly_started = time.monotonic()
    piece_vectors, owners, index_tokens, index_seconds = [], [], 0., 0.
    for owner, text in enumerate(docs):
        if not semantic:
            break
        entry = entries[('document', text)]
        piece_vectors.extend(entry['vectors'])
        owners.extend([owner] * len(entry['vectors']))
        index_tokens += entry['tokens']
        index_seconds += entry['embedding_seconds']
    # Include document preparation/cache processing, excluding quality queries.
    index_seconds += index_overhead_seconds + max(0, time.monotonic() - assembly_started)
    ranking, latencies, query_prices = {}, [], []
    provider_rate = prices.get(cfg['model'], 0) / 1_000_000

    def search(qid, fresh):
        budget.store.renew(budget.campaign, *budget.lease)
        query = data['queries'][qid]
        before = budget.summary()['confirmed_cost_usd']
        started = time.monotonic()
        if not semantic:
            vector, query_tokens, embedding_seconds = None, 0, 0
        elif fresh:
            vector = embed([query], 'query')[0]
            query_tokens, embedding_seconds = 0, 0
        else:
            entry = entries[('query', query)]
            vector = entry['vectors'][0]
            query_tokens, embedding_seconds = entry['tokens'], entry['embedding_seconds']
        selected = rank(docs, doc_ids, query, vector, piece_vectors, owners, cfg)
        if cfg['reranker'] == 'jev':
            if not rerank_key:
                raise ValueError('reranker secret is absent')
            selected = rerank(query, {d: docs[doc_ids.index(d)] for d in selected}, budget, rerank_key, prices['jev-1.13.0'])
        elapsed = time.monotonic() - started
        spend = budget.summary()['confirmed_cost_usd'] - before
        return selected[:10], elapsed, spend + query_tokens * provider_rate + (elapsed + embedding_seconds) * compute_rate

    # Quality always covers every judged query, using batched query vectors.
    for position, qid in enumerate(query_ids, 1):
        selected, elapsed, price = search(qid, False)
        ranking[qid] = selected
        if not fresh_latency:
            query_prices.append(price)
        if position % 10 == 0 or position == len(query_ids):
            LOG.info('search progress completed=%d total=%d elapsed_seconds=%.3f',
                     position, len(query_ids), time.monotonic() - indexing_started)
    sample = None
    if fresh_latency:
        timed_ids = sorted(query_ids, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q))[:LATENCY_SAMPLE_SIZE]
        sample = {'policy': LATENCY_SAMPLE_POLICY, 'query_ids': timed_ids, 'warmup_query_ids': [query_ids[0]]}
        # Warmup exercises the same fresh serving path on both configurations.
        # Its charges stay in the ledger, outside per-search metrics.
        search(query_ids[0], True)
        for position, qid in enumerate(timed_ids, 1):
            _, elapsed, price = search(qid, True)
            latencies.append(1000 * elapsed)
            query_prices.append(price)
            if position % 10 == 0 or position == len(timed_ids):
                LOG.info('latency progress completed=%d total=%d elapsed_seconds=%.3f',
                         position, len(timed_ids), time.monotonic() - indexing_started)
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
                'cache_hits': cache_hits, 'latency_sample': sample, 'latency_method': 'serial fresh query embedding+retrieval+rerank; fixed hash sample up to 50; one fixed first-query warmup' if fresh_latency else 'cached exploration; p95 unavailable',
                'price_basis': 'frozen rates, original embedding usage; includes attributable compute'},
            'machine': socket.gethostname()}


def record(measured, cfg, experiment, sha, scorer_digest):
    return {'schema_version': 1, 'experiment': experiment, 'git_sha': sha,
            'plugin_digest': scorer_digest, 'config': cfg, 'tier': 'direct',
            'machine': measured['machine'], 'duration_seconds': measured.get('duration_seconds'),
            'dataset': measured['dataset'], 'metrics': measured['metrics'],
            'per_query': measured['per_query'], 'cost': measured['cost']}
