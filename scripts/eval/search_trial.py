"""Direct search measurement; immutable embedding cache and fresh search latency.

Network adapters are runner-owned so every paid attempt crosses the shared
admission ledger. Only explicit dev data is accepted by this tier.
"""
import collections
import concurrent.futures
import contextlib
import copy
import hashlib
import json
import logging
import math
import network_recovery
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


def resource_class(policy):
    return f"cpu{policy['modal_cpu']}-memory{policy['modal_memory_mib']}"


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


class BM25:
    """Prepared term postings; scoring visits only the query's terms."""
    def __init__(self, docs):
        self.postings = collections.defaultdict(list)
        lengths = []
        for i, text in enumerate(docs):
            terms = collections.Counter(re.findall(r'\w+', text.casefold()))
            lengths.append(sum(terms.values()))
            for word, tf in terms.items():
                self.postings[word].append((i, tf))
        self.count = len(docs)
        average = sum(lengths) / self.count or 1
        self.norms = [1.2 * (.25 + .75 * length / average) for length in lengths]
        self.idfs = {word: math.log(1 + (self.count - len(postings) + .5) / (len(postings) + .5))
                     for word, postings in self.postings.items()}

    def score(self, query):
        scores = [0.] * self.count
        for word in set(re.findall(r'\w+', query.casefold())):
            for i, tf in self.postings.get(word, ()):
                scores[i] += self.idfs[word] * tf * 2.2 / (tf + self.norms[i])
        return scores


class SearchIndex:
    """One trial set's prepared BM25 and best-piece exact cosine retrieval."""
    def __init__(self, docs, doc_ids, piece_vectors, owners, cfg):
        self.doc_ids, self.cfg, self.owners = doc_ids, cfg, owners
        self.lexical = BM25(docs) if cfg['dense_weight'] < 1 else None
        self.pieces = direct.normalize(piece_vectors) if cfg['dense_weight'] > 0 else None

    def dense_scores(self, query_vectors):
        import numpy as np
        scores = direct.normalize(query_vectors) @ self.pieces.T
        dense = np.full((len(scores), len(self.doc_ids)), -np.inf, dtype=np.float32)
        np.maximum.at(dense.T, self.owners, scores.T)
        return dense

    def rank(self, query, query_vector=None, dense=None):
        order = lambda values: sorted(range(len(self.doc_ids)), key=lambda i: (-values[i], self.doc_ids[i]))
        alpha = self.cfg['dense_weight']
        if alpha == 0:
            selected = order(self.lexical.score(query))
        else:
            if dense is None:
                dense = self.dense_scores([query_vector])[0]
            dense_order = order(dense)
            if alpha == 1:
                selected = dense_order
            else:
                lexical_order = order(self.lexical.score(query))
                fused = [0.] * len(self.doc_ids)
                for weight, ranking in ((alpha, dense_order), (1 - alpha, lexical_order)):
                    for position, index in enumerate(ranking, 1):
                        fused[index] += weight / (60 + position)
                selected = order(fused)
        return [self.doc_ids[i] for i in selected[:self.cfg['candidate_count']]]


def rerank(query, passages, budget, key, price, timing=None):
    """One bounded Jev attempt, no hidden retry/background transport."""
    sys.path.insert(0, str(ROOT / 'plugins/jev-rerank'))
    from jev_rerank import client as jev
    raw = json.dumps(jev.payload(query, passages), ensure_ascii=False, separators=(',', ':')).encode()
    if len(raw) > jev.MAX_BYTES:
        raise ValueError('reranker request exceeds its supported bound')
    timing = {} if timing is None else timing
    started = time.monotonic()
    call = budget.reserve(jev.MODEL, 'rerank', 'query', jev.MAX_TOKENS, price)
    timing['blocked'] = time.monotonic() - started
    timing['ledger'] = timing['blocked']
    request = urllib.request.Request(jev.URL, data=raw, headers={'Content-Type': 'application/json',
                                     'Authorization': 'Bearer ' + key}, method='POST')
    try:
        started = time.monotonic()
        with urllib.request.build_opener(embeddings.NoRedirect()).open(request, timeout=60) as response:
            answer = response.read(jev.MAX_RESPONSE_BYTES + 1)
        timing['provider'] = time.monotonic() - started
        timing['blocked'] += timing['provider']
        if len(answer) > jev.MAX_RESPONSE_BYTES:
            raise ValueError()
        body = json.loads(answer)
        tokens = body['usage']['input_tokens']
        started = time.monotonic()
        budget.settle(call, tokens)
        ledger = time.monotonic() - started
        timing['ledger'] += ledger
        timing['blocked'] += ledger
        scores = {name: item['noul'] for name, item in body['answers'].items()}
        if (type(tokens) is not int or not 0 <= tokens <= jev.MAX_TOKENS or body['model'] != jev.MODEL
                or set(scores) != set(passages) or any(type(v) not in (int, float) or not math.isfinite(v) or not 0 <= v <= 1 for v in scores.values())):
            raise ValueError()
        return sorted(scores, key=lambda name: (-scores[name], name))
    except (embeddings.BudgetExceeded, network_recovery.Outage, control_store.LeaseLost,
            control_store.Unavailable, control_store.Contention):
        raise
    except Exception:
        raise RuntimeError('reranker attempt failed; uncertain charge retained') from None


def _measure(cfg, data, dataset, cache, budget, hosted, prices, compute_rate,
            fresh_latency=True, rerank_key='', flush=lambda: None, private_vectors=None, quality_concurrency=8, refresh=lambda: None):
    if dataset['split'] != 'dev':
        raise PermissionError('tier 1 accepts campaign-dev data only')
    cfg = configuration(cfg)
    if type(quality_concurrency) is not int or not 1 <= quality_concurrency <= 32:
        raise ValueError('quality concurrency must be 1..32')
    phase_usage = {}
    def phase_start():
        return time.monotonic(), time.process_time()
    def phase_end(name, started):
        elapsed, cpu = time.monotonic() - started[0], time.process_time() - started[1]
        usage = phase_usage.setdefault(name, {'elapsed_seconds': 0., 'cpu_seconds': 0.})
        usage['elapsed_seconds'] += max(0, elapsed)
        usage['cpu_seconds'] += max(0, cpu)
    indexing_phase = phase_start()
    LOG.info('indexing started')
    indexing_started = time.monotonic()
    doc_ids, query_ids = sorted(data['corpus']), sorted(data['qrels'])
    docs = [(data['corpus'][d].get('title', '') + '\n' if data['corpus'][d].get('title') else '')
            + data['corpus'][d]['text'] for d in doc_ids]
    if not docs or not query_ids:
        raise ValueError('empty measurement set')
    cache = pathlib.Path(cache)
    if not dataset['private']:
        cache.mkdir(parents=True, exist_ok=True)
    local = direct.E5()
    def embed(texts, mode, client=hosted):
        started = time.monotonic()
        if cfg['model'] == direct.E5_MODEL:
            vectors = direct.normalize(local.embed(texts, mode)).tolist()
            return vectors, time.monotonic() - started, 0
        blocked, service = client.blocked_seconds, client.service_seconds
        vectors = client.embed(cfg['model'], texts, mode, dimensions=cfg['dimensions'])
        vectors = direct.normalize(vectors).tolist()
        return (vectors, max(0, time.monotonic() - started - (client.blocked_seconds - blocked)),
                client.service_seconds - service)

    identity = {k: cfg[k] for k in ('model', 'revision', 'dimensions', 'window_chars', 'overlap_chars')}
    identity['timing'] = 'local-compute-v2'
    # The protected caller owns this map for one dataset invocation only.
    # No private text identities, vectors or cache keys cross a durable boundary.
    entries = (private_vectors.setdefault(tuple(identity.values()), {})
               if dataset['private'] and private_vectors is not None else {})
    cache_hits, cache_fills = 0, 0
    semantic = cfg['dense_weight'] > 0
    document_embedding_seconds = 0
    index_overhead_seconds = time.monotonic() - indexing_started
    for mode, texts in (('document', docs if semantic else []),
                        ('query', [data['queries'][q] for q in query_ids] if semantic else [])):
        mode_started = time.monotonic()
        unique = list(dict.fromkeys(texts))
        LOG.info('embedding started mode=%s total=%d', mode, len(unique))
        # Local inference stays serial; hosted requests overlap across a bounded
        # wave. Claims, file persistence, Volume commits and publication stay on
        # this thread. Never enqueue the entire dataset.
        concurrency = CACHE_CONCURRENCY if mode == 'document' and cfg['model'] != direct.E5_MODEL else 1
        wave_size = concurrency * control_store.LEASE_BATCH_SIZE
        for wave_start in range(0, len(unique), wave_size):
            hits_before = cache_hits
            while True:
                try:
                    wave = []
                    with contextlib.ExitStack() as validation:
                        for start in range(wave_start, min(wave_start + wave_size, len(unique)), control_store.LEASE_BATCH_SIZE):
                            chunk = unique[start:start + control_store.LEASE_BATCH_SIZE]
                            if dataset['private']:
                                pending = []
                                for text in chunk:
                                    if (mode, text) in entries:
                                        cache_hits += 1
                                        continue
                                    pieces = (direct.split_documents([text], cfg['window_chars'], cfg['overlap_chars'])[0]
                                              if mode == 'document' else [text])
                                    pending.append((text, pieces, None, None))
                            else:
                                keyed = {'embedding/' + digest({'config': identity, 'mode': mode,
                                         'text_hash': hashlib.sha256(text.encode()).hexdigest()}): text for text in chunk}
                                started = time.monotonic()
                                pending = []
                                # Claims commit before volume I/O; validation failure
                                # releases claims across the unstarted wave.
                                claims = validation.enter_context(budget.store.claim_batch(
                                    budget.campaign, keyed, ttl=control_store.VALIDATION_LEASE_TTL, require_available=True))
                                for cache_key, text in keyed.items():
                                    claim = claims[cache_key]
                                    if claim['status'] == 'done':
                                        meta = claim['payload']
                                        path = cache / meta['filename']
                                        if not path.exists():
                                            refresh()  # Import another container's committed Volume files.
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
                                LOG.info('cache claims entries=%d elapsed_seconds=%.3f', len(keyed), time.monotonic() - started)
                            if pending:
                                if not dataset['private']:
                                    budget.store.renew_many(budget.campaign, {k: o for _, _, k, o in pending}, ttl=control_store.VALIDATION_LEASE_TTL)
                                budget.store.renew(budget.campaign, *budget.lease)
                                wave.append(pending)
                    break
                except control_store.LeaseBusy:
                    cache_hits = hits_before
                    budget.store.renew(budget.campaign, *budget.lease, ttl=budget.ttl)
                    LOG.info('cache fill busy; waiting for committed evidence')
                    time.sleep(1)
            if wave:
                task_budgets = [control_store.Budget(budget.store, budget.campaign, budget.lease, ttl=budget.ttl) for _ in wave]
                def fill(item):
                    pending, task_budget = item
                    if not dataset['private']:
                        # Extend only validated work, immediately before admission.
                        budget.store.renew_many(budget.campaign, {k: o for _, _, k, o in pending}, ttl=86400)
                    texts = [piece for _, pieces, _, _ in pending for piece in pieces]
                    if cfg['model'] == direct.E5_MODEL:
                        vectors, compute_seconds, _ = embed(texts, mode)
                    else:
                        # Usage and unknown reservations belong to this task,
                        # never a delta of a concurrently changing global sum.
                        client = copy.copy(hosted)
                        client.budget = task_budget
                        vectors, compute_seconds, _ = embed(texts, mode, client)
                    usage = task_budget.summary()
                    return vectors, usage['confirmed_input_tokens'] + usage['reserved_input_tokens'], compute_seconds
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
                # Price local work only. Bound summed task work by wave wall
                # time so parallel execution is never charged multiple times.
                total_compute = sum(item[2] for item in filled)
                scale = min(1, seconds / total_compute) if total_compute else 0
                for pending, (vectors, tokens, compute_seconds) in zip(wave, filled):
                    texts = [piece for _, pieces, _, _ in pending for piece in pieces]
                    bound = embeddings.estimate_tokens(texts)
                    attributed_seconds = compute_seconds * scale
                    offset, publication = 0, {}
                    for text, pieces, cache_key, owner in pending:
                        fraction = embeddings.estimate_tokens(pieces) / bound
                        entry = {'vectors': vectors[offset:offset + len(pieces)], 'tokens': tokens * fraction,
                                 'embedding_seconds': attributed_seconds * fraction}
                        if not dataset['private']:
                            filename = digest([cache_key, owner]) + '.json'
                            results.save(cache / filename, entry)
                            publication[cache_key] = (owner, {'filename': filename, 'digest': digest(entry)})
                        entries[(mode, text)] = entry
                        offset += len(pieces)
                    # SQL references files only after this chunk is durable.
                    if not dataset['private']:
                        flush()
                        budget.store.publish_many(budget.campaign, publication)
                    cache_fills += len(pending)
            LOG.info('embedding progress processed=%d total=%d hits=%d filled=%d',
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
    index = SearchIndex(docs, doc_ids, piece_vectors, owners, cfg)
    # Include document preparation/cache processing, excluding quality queries.
    index_seconds += index_overhead_seconds + max(0, time.monotonic() - assembly_started)
    phase_end('indexing', indexing_phase)
    LOG.info('indexing complete documents=%d queries=%d', len(docs), len(query_ids))
    ranking, latencies, provider_latencies, local_latencies = {}, [], [], []
    timing_samples = collections.defaultdict(list)
    retried_samples = 0
    provider_prices, compute_prices = [], []
    provider_rate = prices.get(cfg['model'], 0) / 1_000_000

    def search(qid, fresh, dense=None, batch_seconds=0, search_budget=budget):
        renewal_started = time.monotonic()
        if fresh:
            budget.store.renew(budget.campaign, *budget.lease)
        renewal = time.monotonic() - renewal_started
        query = data['queries'][qid]
        before = search_budget.summary()['cost_upper_bound_usd']
        started = time.monotonic()
        provider_seconds = 0
        phases = {'ledger': renewal, 'retry_http': 0., 'backoff': 0., 'admission': 0., 'retries': 0}
        if fresh and semantic and hosted is not None:
            counters = {key: getattr(hosted, key) for key in (
                'ledger_seconds', 'backoff_seconds', 'admission_seconds', 'http_seconds', 'service_seconds', 'retry_attempts')}
        if not semantic:
            vector, query_tokens, embedding_seconds, local_embedding = None, 0, 0, 0
        elif fresh:
            vectors, local_embedding, provider_seconds = embed([query], 'query')
            vector = vectors[0]
            query_tokens, embedding_seconds = 0, 0
        else:
            entry = entries[('query', query)]
            vector = entry['vectors'][0]
            query_tokens, embedding_seconds = entry['tokens'], entry['embedding_seconds']
            local_embedding = 0
        if fresh and semantic and hosted is not None:
            delta = {key: getattr(hosted, key) - value for key, value in counters.items()}
            phases.update(ledger=phases['ledger'] + delta['ledger_seconds'],
                          backoff=delta['backoff_seconds'], admission=delta['admission_seconds'],
                          retry_http=max(0, delta['http_seconds'] - delta['service_seconds']),
                          retries=delta['retry_attempts'])
        phases['embedding'] = local_embedding + provider_seconds
        local_started = time.monotonic()
        selected = index.rank(query, vector, dense)
        phases['retrieval'] = time.monotonic() - local_started
        rerank_started = time.monotonic()
        if cfg['reranker'] == 'jev':
            if not rerank_key:
                raise ValueError('reranker secret is absent')
            timing = {}
            selected = rerank(query, {d: docs[doc_ids.index(d)] for d in selected}, search_budget, rerank_key, prices['jev-1.13.0'], timing)
            provider_seconds += timing['provider']
            phases['ledger'] += timing['ledger']
        else:
            timing = {'blocked': 0}
        local_seconds = local_embedding + max(0, time.monotonic() - local_started - timing['blocked'])
        phases['rerank'] = max(0, time.monotonic() - rerank_started - (timing['blocked'] - timing.get('provider', 0)))
        phases['wall'] = time.monotonic() - started + renewal
        elapsed = local_seconds + provider_seconds
        spend = search_budget.summary()['cost_upper_bound_usd'] - before
        return (selected[:10], elapsed, spend + query_tokens * provider_rate,
                (local_seconds + embedding_seconds + batch_seconds) * compute_rate,
                provider_seconds, local_seconds, phases)

    # Bound temporary piece-score memory while batching quality-only work.
    # Fresh serving below always computes its own per-query dense scores.
    quality_phase = phase_start()
    for start in range(0, len(query_ids), 32):
        # Quality work uses prepared vectors/indexes: renew once per bounded
        # batch, not one remote SQL round trip for every cached query. Paid
        # reranking attempts still renew and reserve independently.
        budget.store.renew(budget.campaign, *budget.lease)
        batch_ids = query_ids[start:start + 32]
        batch_started = time.monotonic()
        dense_batch = index.dense_scores([entries[('query', data['queries'][q])]['vectors'][0]
                                          for q in batch_ids]) if semantic else [None] * len(batch_ids)
        batch_seconds = (time.monotonic() - batch_started) / len(batch_ids)
        concurrency = quality_concurrency if cfg['reranker'] == 'jev' else 1
        for offset in range(0, len(batch_ids), concurrency):
            wave_ids = batch_ids[offset:offset + concurrency]
            wave_dense = dense_batch[offset:offset + concurrency]
            # Independent views prevent overlapping paid attempts from inflating
            # a search's price. SQL still owns campaign-wide admission.
            task_budgets = [control_store.Budget(budget.store, budget.campaign, budget.lease, ttl=budget.ttl) for _ in wave_ids]
            try:
                if concurrency == 1:
                    searched = [search(wave_ids[0], False, wave_dense[0], batch_seconds, task_budgets[0])]
                else:
                    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
                        futures = [pool.submit(search, qid, False, dense, batch_seconds, task_budget)
                                   for qid, dense, task_budget in zip(wave_ids, wave_dense, task_budgets)]
                        searched = [future.result() for future in futures]
            finally:
                # Drain admitted calls before propagating failure; retain both
                # confirmed and uncertain usage even if another task is refused.
                for task_budget in task_budgets:
                    budget.calls.extend(task_budget.calls)
            for position, (qid, found) in enumerate(zip(wave_ids, searched), start + offset + 1):
                selected, _, provider_price, compute_price, _, _, _ = found
                ranking[qid] = selected
                if not fresh_latency:
                    provider_prices.append(provider_price)
                    compute_prices.append(compute_price)
                if position % 10 == 0 or position == len(query_ids):
                    LOG.info('search progress completed=%d total=%d', position, len(query_ids))
    phase_end('quality', quality_phase)
    scoring_phase = phase_start()
    LOG.info('scoring started queries=%d', len(query_ids))
    scores = scoring.score(data['qrels'], ranking)
    phase_end('scoring', scoring_phase)
    LOG.info('scoring complete queries=%d', len(query_ids))
    # Preparation barrier: paired callers prepare both indexes before warmup.
    yield
    sample = None
    if fresh_latency:
        timed_ids = sorted(query_ids, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q))[:LATENCY_SAMPLE_SIZE]
        sample = {'policy': LATENCY_SAMPLE_POLICY, 'query_ids': timed_ids, 'warmup_query_ids': [query_ids[0]]}
        # Warmup exercises the same fresh serving path on both configurations.
        # Its charges stay in the ledger, outside per-search metrics.
        latency_phase = phase_start()
        search(query_ids[0], True)
        phase_end('latency', latency_phase)
        yield
        for position, qid in enumerate(timed_ids, 1):
            latency_phase = phase_start()
            _, elapsed, provider_price, compute_price, provider_seconds, local_seconds, phases = search(qid, True)
            phase_end('latency', latency_phase)
            retried_samples += int(phases.pop('retries') > 0)
            for phase, seconds in phases.items():
                timing_samples[phase].append(1000 * seconds)
            latencies.append(1000 * elapsed)
            provider_latencies.append(1000 * provider_seconds)
            local_latencies.append(1000 * local_seconds)
            provider_prices.append(provider_price)
            compute_prices.append(compute_price)
            if position % 10 == 0 or position == len(timed_ids):
                LOG.info('latency progress completed=%d total=%d', position, len(timed_ids))
            yield
    def percentile(values, fraction):
        return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)] if values else None
    provider_price = sum(provider_prices) / len(provider_prices)
    compute_price = sum(compute_prices) / len(compute_prices)
    return {'dataset': dataset, 'metrics': {**scores['mean'],
                'latency_p50_ms': percentile(latencies, .5),
                'latency_p95_ms': percentile(latencies, .95),
                'cost_per_search_usd': provider_price + compute_price,
                'cost_per_1000_documents_usd': (index_tokens * provider_rate + index_seconds * compute_rate) * 1000 / len(docs)},
            'per_query': scores['per_query'], 'cost': {'provider': budget.summary(),
                'search_provider_usd': provider_price, 'search_compute_usd': compute_price,
                'search_timing_ms': {'samples': len(latencies), 'retried_samples': retried_samples,
                    **{phase + suffix: percentile(values, fraction) for phase, values in timing_samples.items()
                       for suffix, fraction in (('_p50', .5), ('_p95', .95))},
                    'provider_p50': percentile(provider_latencies, .5), 'provider_p95': percentile(provider_latencies, .95),
                    'local_p50': percentile(local_latencies, .5), 'local_p95': percentile(local_latencies, .95)},
                'index_tokens_attributed': index_tokens, 'index_embedding_seconds_attributed': index_seconds,
                'cache_hits': cache_hits, 'phase_usage': phase_usage, 'quality_concurrency': quality_concurrency, 'latency_sample': sample, 'latency_method': 'serial fresh service embedding+retrieval+rerank; excludes retry/admission/ledger; fixed hash sample up to 50; one fixed first-query warmup' if fresh_latency else 'cached exploration; p95 unavailable',
                'price_basis': 'frozen rates, provider usage upper bound plus local compute; excludes HTTP, retry and ledger waits; local-compute-v2'},
            'machine': socket.gethostname()}


def measure(*args, **kwargs):
    """Prepare and measure one standalone configuration."""
    run = _measure(*args, **kwargs)
    while True:
        try:
            next(run)
        except StopIteration as done:
            return done.value


def measure_pair(configs, data, dataset, cache, budgets, clients, prices, compute_rate,
                 fresh_latency=True, rerank_key='', private_vectors=None, quality_concurrency=8, flush=lambda: None, latency_scope=contextlib.nullcontext, refresh=lambda: None):
    """Prepare both sides, then alternate warmups and identical query samples."""
    runs = {side: _measure(cfg, data, dataset, cache, budgets[side], clients[side], prices,
                          compute_rate, fresh_latency, rerank_key, flush=flush, private_vectors=private_vectors, quality_concurrency=quality_concurrency, refresh=refresh)
            for side, cfg in configs.items()}
    measured = {}
    try:
        for run in runs.values():
            next(run)  # Complete preparation before either timed serving path.
        waiting = time.monotonic(), time.process_time()
        with latency_scope() if fresh_latency else contextlib.nullcontext() as slot:
            window_wait = {'elapsed_seconds': max(0, time.monotonic() - waiting[0]),
                           'cpu_seconds': max(0, time.process_time() - waiting[1])}
            if slot:
                for budget in budgets.values():
                    budget.extra_leases.append(slot)
            try:
                while runs:
                    for side, run in list(runs.items()):
                        if slot:
                            budget = budgets[side]
                            budget.store.renew_many(budget.campaign, dict([budget.lease, slot]), ttl=budget.ttl)
                        try:
                            next(run)
                        except StopIteration as done:
                            measured[side] = done.value
                            del runs[side]
            finally:
                if slot:
                    for budget in budgets.values():
                        budget.extra_leases.remove(slot)
    finally:
        for run in runs.values():
            run.close()
    if fresh_latency:
        for row in measured.values():
            row['cost']['phase_usage']['latency_wait'] = window_wait
            row['cost']['latency_method'] = 'paired A/B fresh service embedding+retrieval+rerank; excludes retry/admission/ledger; fixed hash sample up to 50; one fixed first-query warmup per side'
    return measured


def record(measured, cfg, experiment, sha, scorer_digest):
    return {'schema_version': 1, 'experiment': experiment, 'git_sha': sha,
            'plugin_digest': scorer_digest, 'config': cfg, 'tier': 'direct',
            'machine': measured['machine'], 'duration_seconds': measured.get('duration_seconds'),
            'dataset': measured['dataset'], 'metrics': measured['metrics'],
            'per_query': measured['per_query'], 'cost': measured['cost']}


def publish_pair(store, request, rows):
    """Fence both records with this invocation; each trial has its own baseline."""
    baseline_key = request['lease_key'] + '/' + request['owner'] + '/baseline'
    claim = store.claim(request['campaign'], baseline_key, request['policy']['max_seconds'])
    if claim['status'] != 'claimed':
        raise RuntimeError('paired baseline evidence unavailable')
    store.publish_many(request['campaign'], {baseline_key: (claim['owner'], rows['baseline']),
        request['lease_key']: (request['owner'], rows['candidate'])})
    return rows['candidate']
