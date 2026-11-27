#!/usr/bin/env python3
"""Measure how well Quivr search ranks evaluation sets (THE-775): `make eval`.

For each set (public ones from public_sets.py, or a private one in the TREC layout of trec.py),
ingest its documents into a new Corpus through the public API, wait until every Record has its
vectors, run every query in each search mode and each profile the API serves, and score the
rankings (scoring.py). Writes report.json and report.md; see docs/agents/evaluation.md.

By default it starts an isolated local stack (Linux x86_64 or Apple silicon macOS). With
--api-url it measures an existing installation instead, using the key in QUIVR_EVAL_API_KEY.
With --compare-to <ref> it also starts a stack built from that revision, in the same process
and on the same CPU, and searches the two stacks alternately to compare their latency (THE-874).
Harness and dependency errors exit nonzero; scores are findings, never a pass/fail threshold.
"""
import argparse
import datetime
import json
import os
import pathlib
import platform
import re
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))

import public_sets  # noqa: E402
import jev  # noqa: E402
import report as render  # noqa: E402
import resources  # noqa: E402
import embeddings  # noqa: E402
import trec  # noqa: E402
from measure_metrics import percentile  # noqa: E402

import ci_guard

MODES = ['lexical', 'semantic', 'hybrid']
BASELINE_PROFILE = 'core.retrieve/default'
JEV_PLUGIN = 'jev.rerank'
BASELINE_SYSTEM = 'hybrid/default'  # the API's default mode and profile
LIMIT = 50  # the API maximum; hits are deduplicated by Record before scoring
TIMING_LIMITS = [LIMIT, 10]  # limit 10, the API default, is timed only, in a second pass
PHASES = ['query_encoding_ms', 'index_query_ms', 'hydration_ms', 'plugin_rounds_ms']  # of usage.phases
BATCH_ITEMS, BATCH_BYTES = 100, 8 << 20  # under the batch bounds of 100 entries and 10 MiB
WARMUP_QUERIES = 3
DEEP_LIMIT = 10
_USE_CLIENT_EVALUATION = object()


class Client:
    def __init__(self, base, key, timeout=60):
        self.base, self.key, self.timeout = base.rstrip('/'), key, timeout
        # An optional evaluation selection is kept on the client so callers
        # that issue individual searches can opt into the same request shape
        # as a measured evaluation cut.  measure_set passes its selection
        # explicitly, which keeps the served and evaluation cuts independent.
        self.evaluation = None
        self.jev_log = None
        self.stack = None
        self.budget = None
        self.embedding_gates = []
        self.deep_enabled = bool(os.environ.get('TYPESAFE_API_KEY'))
        self.deep_skip_reason = 'TYPESAFE_API_KEY absent; paid deep evaluation skipped'

    def call(self, method, path, body=None, expected=(200,), attempts=5, deadline=None):
        for gate in self.embedding_gates:
            gate.budget.check()
        paid = method == 'POST' and path == '/v0/search' and body is not None and is_paid_profile(body.get('profile', ''))
        if not paid:
            try:
                return self._call(method, path, body, expected, attempts, deadline)
            finally:
                for gate in self.embedding_gates:
                    gate.budget.check()
        if self.budget is None:
            raise RuntimeError('paid deep evaluation requires a run input token budget')
        with self.budget.request_lock:
            self.budget.admit()
            begin = jev.offset(self.jev_log)
            try:
                return self._call(method, path, body, expected, 1)
            finally:
                self.budget.settle(self.jev_log, begin)

    def gate_for(self, selection):
        return next((gate for gate in self.embedding_gates if gate.plugin == evaluation_selection(selection)['evaluation_plugin']), None) if selection else None

    def poll(self, path, deadline):
        """Retry transient dependency responses within the owning wait's deadline."""
        return self.call('GET', path, attempts=None, deadline=deadline)

    def _call(self, method, path, body, expected, attempts, deadline=None):
        """Retry only explicit retryable 429/503 errors, with capped backoff."""
        data = json.dumps(body).encode() if body is not None else None
        attempt = 0
        while True:
            remaining = deadline - time.monotonic() if deadline is not None else self.timeout
            if remaining <= 0:
                raise RuntimeError(f'{method} {path} polling deadline exceeded')
            request = urllib.request.Request(self.base + path, data=data, method=method,
                                             headers={'Authorization': 'Bearer ' + self.key, 'Content-Type': 'application/json'})
            try:
                response = urllib.request.urlopen(request, timeout=min(self.timeout, remaining))
            except urllib.error.HTTPError as error:
                response = error
            with response:
                raw = response.read()
            result = json.loads(raw) if raw else {}
            if response.status in expected:
                return response.status, result
            if (response.status not in (429, 503) or result.get('retryable') is not True
                    or (attempts is not None and attempt >= attempts - 1)):
                raise RuntimeError(f'{method} {path} returned {response.status}: {str(result)[:300]}')
            delay = min(2 ** min(attempt, 4), 10)
            if deadline is not None:
                delay = min(delay, max(0, deadline - time.monotonic()))
            time.sleep(delay)
            attempt += 1


def advertised_profiles(client):
    """Index advertised profiles by the unambiguous name accepted by search."""
    _, listed = client.call('GET', '/v0/search/profiles')
    profiles = {}
    for profile in listed['items']:
        name = profile['name']
        fallback = BASELINE_PROFILE if name == 'default' else name
        profiles[profile.get('full_name', fallback)] = profile
    return profiles


def select_profiles(advertised, requested):
    if not requested:
        return advertised
    selected = {}
    for name in requested:
        matches = [name] if name in advertised else [
            full_name for full_name, profile in advertised.items() if name in profile.get('aliases', [profile['name']])]
        if len(matches) != 1:
            choices = ', '.join(matches or advertised)
            reason = 'ambiguous' if matches else 'unknown'
            raise ValueError(f'{reason} search profile {name!r}; choose a full name from: {choices}')
        full_name = matches[0]
        selected[full_name] = advertised[full_name]
    return selected


def is_paid_profile(profile):
    return profile.startswith(f'{JEV_PLUGIN}/')


def serving_profiles(client, corpus, advertised, probe_query='profile probe', allow_paid=True, modes=MODES):
    """Probe unpaid profiles; use provider metadata for paid profiles."""
    served, refused = {}, {}
    for full_name, profile in advertised.items():
        provider = profile['provider']
        if is_paid_profile(full_name):
            if not allow_paid:
                refused[full_name] = 'paid evaluation is restricted to the public miracl-fr set'
            elif not client.deep_enabled:
                refused[full_name] = client.deep_skip_reason
            else:
                served[full_name] = f"plugin:{provider['plugin_id']}@{provider['plugin_version']}/{profile['name']}"
            continue
        reasons = []
        for mode in modes:
            status, body = client.call('POST', '/v0/search', {
                'query': probe_query, 'corpus_ids': [corpus], 'mode': mode,
                'profile': profile.get('full_name', profile['name']), 'limit': 1,
            }, expected=(200, 422))
            if status == 200:
                served[full_name] = body['retrieval_profile']['version']
                break
            reasons.append(f"{mode}: 422 {body.get('code', '')}".strip())
        else:
            refused[full_name] = '; '.join(reasons)
    return served, refused


def measurements(sides, evaluation=None):
    profiles = list(dict.fromkeys(profile for side in sides for profile in side['served']))
    profiles.sort(key=lambda profile: (profile != BASELINE_PROFILE, is_paid_profile(profile)))
    for profile in profiles:
        if profile == f'{JEV_PLUGIN}/deep' and any(
                side['client'].stack is not None and 'jev_pin' in side['client'].stack.state for side in sides):
            for label, configuration in jev.matrix():
                yield profile, label, configuration
        else:
            yield profile, 'default' if profile == BASELINE_PROFILE else profile, None
    for selection in evaluation_selections(evaluation):
        defaults = dict.fromkeys(side['evaluation_default'] for side in sides if side['evaluation_default'] in side['served'])
        for profile in defaults:
            yield profile, evaluation_label(selection), None


def deep_warmups(queries, run_id):
    result = []
    for index in range(WARMUP_QUERIES):
        query = f'evaluation warmup {run_id} {index}'
        while query in queries:
            query += ' warmup'
        result.append(query)
    return result


def evaluation_selections(selection):
    return selection if isinstance(selection, list) else [selection] if selection is not None else []


def observe_evaluation_spaces(client, corpus, evaluations, records, deadline, started, timings):
    """One coverage snapshot for every owner; preserve its first complete observation."""
    if len(timings) == len(evaluations):
        return
    _, result = client.poll(f'/v0/corpora/{urllib.parse.quote(corpus)}/vector-spaces', deadline=deadline)
    elapsed = round(time.monotonic() - started, 3)
    for selection in evaluations:
        label = evaluation_label(selection)
        if label in timings:
            continue
        fields = evaluation_selection(selection)
        for item in result.get('items', []):
            owner = item.get('owner') or {}
            owner_plugin = owner.get('plugin_id') or item.get('owner_plugin_id')
            item_spaces = {item.get(name) for name in ('name', 'vector_space_id', 'space')} - {None, ''}
            coverage = item.get('coverage') or {}
            if (owner_plugin == fields['evaluation_plugin'] and fields['evaluation_space'] in item_spaces
                    and coverage.get('versions_covered') == records):
                timings[label] = {'owner_vectors_seconds': elapsed}
                print(f'[eval] {label}: {records} versions covered after {elapsed} s', flush=True)
                break


def wait_evaluation_spaces(client, corpus, evaluations, records, deadline, started, timings):
    while len(timings) < len(evaluations):
        if time.monotonic() >= deadline:
            missing = [evaluation_label(e) for e in evaluations if evaluation_label(e) not in timings]
            raise RuntimeError(f'evaluation coverage deadline exceeded: {missing}')
        observe_evaluation_spaces(client, corpus, evaluations, records, deadline, started, timings)
        if len(timings) < len(evaluations):
            time.sleep(min(5, max(0, deadline - time.monotonic())))



def batches(commands):
    batch, size = [], 0
    for command in commands:
        n = len(json.dumps(command).encode())
        if batch and (len(batch) == BATCH_ITEMS or size + n > BATCH_BYTES):
            yield batch
            batch, size = [], 0
        batch.append(command)
        size += n
    if batch:
        yield batch


def command(corpus, namespace, key, doc):
    parts = ([{'key': 'title', 'role': 'title', 'content': {'kind': 'text', 'text': doc['title']}}] if doc['title'].strip() else [])
    parts.append({'key': 'body', 'role': 'body', 'content': {'kind': 'text', 'text': doc['text']}})
    return {'idempotency_key': f'{corpus}:{key}', 'source': {'corpus_id': corpus, 'namespace': namespace, 'record_key': key},
            'content': {'kind': 'manifest', 'parts': parts}}


def ingest(client, corpus, namespace, corpus_docs, timeout, stall, progress=None):
    """Submit every document, then wait on the change feed until each Record has its vectors.

    Returns {record_id: doc_id} and timings. Waiting reads record.enrichment_available events
    from a cursor taken before the first submission, so no Record is missed and nothing sleeps
    for a fixed time; a feed that stops moving for `stall` seconds fails with receipt details."""
    _, page = client.poll(f'/v0/changes?corpus_id={urllib.parse.quote(corpus)}', deadline=time.monotonic() + timeout)
    start = time.monotonic()
    receipts = []
    next_observation = start
    for batch in batches(command(corpus, namespace, key, doc) for key, doc in corpus_docs.items()):
        _, result = client.call('POST', '/v0/records/batch', {'items': batch})
        for outcome in result['items']:
            if 'receipt' not in outcome:
                raise RuntimeError(f"entry {outcome.get('index')} refused: {outcome.get('error')}")
            receipts.append(outcome['receipt']['receipt_id'])
    accepted = time.monotonic() - start
    enriched, moved = set(), time.monotonic()
    while len(enriched) < len(corpus_docs):
        _, page = client.poll(f'/v0/changes?corpus_id={urllib.parse.quote(corpus)}&cursor={urllib.parse.quote(page["next_cursor"])}&limit=100', deadline=min(start + timeout, moved + stall))
        new = {e['resource']['id'] for e in page['items'] if e['type'] == 'record.enrichment_available'} - enriched
        if new:
            enriched |= new
            moved = time.monotonic()
        now = time.monotonic()
        if progress is not None and now >= next_observation:
            progress(min(start + timeout, moved + stall))
            next_observation = time.monotonic() + 5
        if now - moved > stall or now - start > timeout:
            raise RuntimeError(f'{namespace}: {len(enriched)}/{len(corpus_docs)} Records have vectors after {round(now - start)} s; '
                               f'first receipts not done: {stuck(client, receipts)}')
        if not page['has_more'] and not new:
            time.sleep(1)  # poll interval of the change feed, not a wait for a result
    records = list_records(client, corpus, start + timeout)
    return records, {'records': len(records), 'accepted_seconds': round(accepted, 3), 'searchable_with_vectors_seconds': round(time.monotonic() - start, 3)}


def list_records(client, corpus, deadline):
    records = {}
    query = {'corpus_id': corpus, 'limit': 100}
    while True:
        _, page = client.poll('/v0/records?' + urllib.parse.urlencode(query), deadline=deadline)
        records.update({record['record_id']: record['source']['record_key'] for record in page['items']})
        if not page.get('next_page_cursor'):
            return records
        query['page_cursor'] = page['next_page_cursor']


def reused_records(client, corpus, docs, deadline):
    records = {record: doc for record, doc in list_records(client, corpus, deadline).items() if doc in docs}
    found = set(records.values())
    if len(found) != len(records):
        raise ValueError(f'corpus {corpus} has duplicate evaluation records')
    if found != docs.keys():
        raise ValueError(f'corpus {corpus} is missing {len(docs.keys() - found)} evaluation documents')
    return records


def read_corpus_state(path, target, corpus, fingerprint, docs):
    if path is None or not path.exists():
        return corpus, None
    state = load_object(path)
    if (state.get('api_url') != target or state.get('fingerprint') != fingerprint
            or (corpus is not None and state.get('corpus_id') != corpus)):
        raise ValueError('--corpus-state does not match the API URL, corpus ID or evaluation set fingerprint')
    records = state.get('records')
    if (not isinstance(state.get('corpus_id'), str) or not state['corpus_id']
            or not isinstance(records, dict) or len(records) != len(docs)
            or any(not key or not isinstance(doc, str) for key, doc in records.items())
            or set(records.values()) != docs.keys()):
        raise ValueError('--corpus-state contains an invalid record map')
    return state['corpus_id'], records


def write_corpus_state(path, target, corpus, fingerprint, records):
    state = {'api_url': target, 'corpus_id': corpus, 'fingerprint': fingerprint, 'records': records}
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, delete=False) as stream:
            temporary = pathlib.Path(stream.name)
            json.dump(state, stream)
        # Two first runs may both prepare a mapping. Never replace the other run's corpus.
        path.hardlink_to(temporary)
    except FileExistsError:
        raise ValueError('--corpus-state was created by another run; retry with the saved state') from None
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def load_object(path):
    value = json.loads(pathlib.Path(path).read_text())
    if not isinstance(value, dict):
        raise ValueError(f'{path} must contain a JSON object')
    return value


def stuck(client, receipts, show=3):
    out = []
    for rid in receipts:
        _, r = client.poll('/v0/ingestion-receipts/' + rid, deadline=time.monotonic() + 15)
        if r.get('availability', {}).get('state') != 'retrieval_ready' or r.get('processing', {}).get('state') != 'idle':
            out.append({k: r.get(k) for k in ['receipt_id', 'state', 'outcome', 'availability', 'processing', 'diagnostics']})
            if len(out) == show:
                break
    return out or 'every receipt is retrieval_ready and idle'


def evaluation_selection(selection):
    """Return the wire fields for an evaluation selection, or no fields.

    The runner uses ``plugin`` and ``space`` internally.  Accepting the wire
    names as aliases keeps Client.evaluation useful to callers that build the
    selection from command/API configuration.
    """
    if selection is None:
        return {}
    if isinstance(selection, dict):
        plugin = selection.get('plugin') or selection.get('plugin_id') or selection.get('evaluation_plugin')
        space = selection.get('space') or selection.get('space_id') or selection.get('evaluation_space')
    else:
        plugin = getattr(selection, 'plugin', None) or getattr(selection, 'plugin_id', None) \
            or getattr(selection, 'evaluation_plugin', None)
        space = getattr(selection, 'space', None) or getattr(selection, 'space_id', None) \
            or getattr(selection, 'evaluation_space', None)
    if not plugin or not space:
        raise ValueError('evaluation selection requires both plugin and space')
    return {'evaluation_plugin': plugin, 'evaluation_space': space}


def evaluation_label(selection):
    fields = evaluation_selection(selection)
    return f"evaluation/{fields['evaluation_plugin']}/{fields['evaluation_space']}"


def search(client, corpus, query, mode, profile, keys, limit=LIMIT, evaluation=_USE_CLIENT_EVALUATION):
    """(ranked doc ids, timing, error): hits deduplicated by Record, best first.

    timing is the client's milliseconds, with the engine's usage (elapsed_ms and its phases) when it reports them."""
    start = time.perf_counter()
    body = {'query': query, 'corpus_ids': [corpus], 'mode': mode, 'profile': profile, 'limit': limit}
    selected = getattr(client, 'evaluation', None) if evaluation is _USE_CLIENT_EVALUATION else evaluation
    body.update(evaluation_selection(selected))
    try:
        _, result = client.call('POST', '/v0/search', body, attempts=1)
    except embeddings.BudgetExceeded:
        raise  # admission stops the campaign; unattempted queries are not zero scores
    except Exception as error:  # a failed search scores 0 and is counted, never hidden
        return [], {'client_ms': (time.perf_counter() - start) * 1000}, str(error)[:200]
    usage = result.get('usage') or {}
    timing = {'client_ms': (time.perf_counter() - start) * 1000, 'elapsed_ms': usage.get('elapsed_ms'),
              'paid_calls': usage.get('paid_calls'), 'cost_cents': usage.get('cost_cents'), **(usage.get('phases') or {})}
    ranked = []
    for hit in result['items']:
        doc = keys.get(hit['record_id'])
        if doc is not None and doc not in ranked:
            ranked.append(doc)
    return ranked, timing, None


def time_summary(timings, failures):
    """p50/p95 of the client time, the engine's time and each phase over successful searches, and the share of the
    engine's time spent encoding queries: what a query-vector cache would save if every query hit it. A field the
    engine did not report (an installation before THE-873) is None."""
    def p(key):
        values = [t[key] for t in timings if t.get(key) is not None]
        return {'p50': rounded(percentile(values, 50)), 'p95': rounded(percentile(values, 95))}
    out = {'searches': len(timings), 'failures': failures, **{key: p(key) for key in ['client_ms', 'elapsed_ms', *PHASES]}}
    reported = [t for t in timings if t.get('query_encoding_ms') is not None and t.get('elapsed_ms') is not None]
    total = sum(t['elapsed_ms'] for t in reported)
    out['query_encoding_share'] = round(sum(t['query_encoding_ms'] for t in reported) / total, 3) if total else None
    return out


def measure_set(clients, name, directory, run_id, options, allow_paid=True, evaluation=None):
    """One result per client, in order. With two clients (--compare-to) each query is searched on
    both, alternating which goes first, so drift on the machine weighs on both sides alike."""
    import scoring
    data = trec.load(directory)
    if evaluation is None:
        evaluation = getattr(options, 'evaluation', None)
    allow_paid = allow_paid and name == 'miracl-fr'
    advertised = [advertised_profiles(client) for client in clients]
    all_profiles = {}
    for profiles in advertised:
        for full_name, profile in profiles.items():
            aliases = all_profiles.get(full_name, {}).get('aliases', [])
            all_profiles[full_name] = {**profile, 'aliases': aliases + profile.get('aliases', [profile['name']])}
    selected_profiles = select_profiles(all_profiles, getattr(options, 'profiles', None))
    evaluations = evaluation_selections(evaluation)
    modes = getattr(options, 'modes', MODES)
    defaults = [next((name for name, profile in profiles.items()
                      if 'default' in profile.get('aliases', [profile['name']])), None)
                for profiles in advertised]
    if evaluations and any(profile is None or is_paid_profile(profile) for profile in defaults):
        raise ValueError('embedding evaluation requires an advertised unpaid default alias')
    if allow_paid and any(
            client.deep_enabled and any(is_paid_profile(profile) and profile in profiles for profile in selected_profiles)
            for client, profiles in zip(clients, advertised)):
        selected = sorted(data['queries'])[:150]
        data['queries'] = {query: data['queries'][query] for query in selected}
        data['qrels'] = {query: data['qrels'][query] for query in selected}
    manifest_path = pathlib.Path(directory) / 'manifest.json'
    manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else {'name': name}
    manifest['fingerprint'] = trec.fingerprint(directory)
    if name in public_sets.SETS:
        manifest.update(tier=public_sets.SETS[name]['tier'], promotion_eligible=public_sets.SETS[name]['promotion_eligible'])
    namespace = f'eval-{name}'
    deep_queries = deep_warmups(set(data['queries'].values()), run_id)
    sides = []
    for client, profiles, default_profile in zip(clients, advertised, defaults):
        gates = client.embedding_gates
        for gate in gates:
            gate.set_name, gate.phase = name, 'indexing'
        state_path = getattr(options, 'corpus_state', None)
        save_state = state_path is not None and not state_path.exists()
        corpus, records = read_corpus_state(state_path, client.base, getattr(options, 'corpus_id', None),
                                            manifest['fingerprint'], data['corpus'])
        reused = corpus is not None
        cached_records = records is not None
        if not reused:
            body = {'name': f'Evaluation {name}', 'idempotency_key': f'eval-{name}-{run_id}'}
            if getattr(options, 'corpus_config', None) is not None:
                body['retrieval'] = options.corpus_config
            _, created = client.call('POST', '/v0/corpora', body, expected=(201,))
            corpus = created['corpus_id']
        result = {'manifest': manifest, 'corpus_id': corpus, 'queries': len(data['queries']), 'documents': len(data['corpus']),
                  'dropped_queries': len(data['dropped_queries']), 'ingestion': {}, 'profiles': {},
                  'systems': {}, 'status': 'indexing'}
        publish = getattr(options, 'publish_partial', None)
        if publish:
            publish(name, result)
        action = 'reusing' if reused else 'ingesting'
        print(f'[eval] {name}: {action} {len(data["corpus"])} documents', flush=True)
        index_started = time.monotonic()
        observed = result['evaluation_ingestion'] = {}
        def progress(deadline):
            observe_evaluation_spaces(client, corpus, evaluations, len(data['corpus']), deadline, index_started, observed)
            if publish:
                publish(name, result)
        try:
            if reused:
                if records is None:
                    records = reused_records(client, corpus, data['corpus'], index_started + options.ingest_timeout)
                result['ingestion'] = {'records': len(records), 'reused': True}
            else:
                records, result['ingestion'] = ingest(
                    client, corpus, namespace, data['corpus'], options.ingest_timeout, options.stall,
                    **({'progress': progress} if evaluations else {}))
            wait_evaluation_spaces(client, corpus, evaluations, len(records), index_started + options.ingest_timeout,
                                   index_started, observed)
            result['ingestion']['paired_vectors_seconds'] = round(time.monotonic() - index_started, 3)
            if save_state and not cached_records:
                write_corpus_state(state_path, client.base, corpus, manifest['fingerprint'], records)
            result['status'] = 'searching'
        finally:
            result['ingestion'].setdefault('partial_seconds', round(time.monotonic() - index_started, 3))
            if len(gates) == 1:
                gate = gates[0]
                result['embedding_indexing'] = gate.budget.summary(gate.label, name, 'indexing')
            if result['status'] == 'searching':
                for gate in gates:
                    gate.phase = 'query'
            if publish:
                publish(name, result)
        selection = {name: profile for name, profile in profiles.items()
                     if name in selected_profiles or (evaluations and name == default_profile)}
        served, refused = serving_profiles(client, corpus, selection, deep_queries[0], allow_paid=allow_paid, modes=modes)
        result['profiles'] = {'advertised': list(profiles), 'served': served, 'refused': refused,
                              'default': 'default' if default_profile == BASELINE_PROFILE else default_profile}
        wire_profiles = {name: profile.get('full_name', profile['name']) for name, profile in selection.items()}
        sides.append({'client': client, 'corpus': corpus, 'records': records, 'served': served,
                      'wire_profiles': wire_profiles, 'evaluation_default': default_profile, 'out': result})
    queries = sorted(data['queries'])
    reranking = any(is_paid_profile(profile) for side in sides for profile in side['served'])
    for profile, label, configuration in measurements(sides, evaluation):
        selected_evaluation = next((e for e in evaluation_selections(evaluation) if label == evaluation_label(e)), None)
        deep = is_paid_profile(profile)
        scored_limit = DEEP_LIMIT if deep or reranking else LIMIT
        limits = [scored_limit] if deep else [scored_limit] + [limit for limit in TIMING_LIMITS if limit != scored_limit]
        for mode in ['hybrid'] if deep else modes:
            serving = [side for side in sides if profile in side['served']
                       and (selected_evaluation is None or profile == side['evaluation_default'])]
            if configuration is not None:
                serving = [side for side in serving if side['client'].stack is not None and 'jev_pin' in side['client'].stack.state]
                for side in serving:
                    started = time.monotonic()
                    jev.select(side['client'].stack, configuration)
                    side['configuration_seconds'] = round(time.monotonic() - started, 3)
            warmups = [data['queries'][query] for query in queries[:WARMUP_QUERIES]]
            if deep:
                warmups = []
            for query in warmups:
                for side in serving:
                    search(side['client'], side['corpus'], query, mode, side['wire_profiles'][profile], side['records'], scored_limit,
                           evaluation=selected_evaluation)
            for side in serving:
                side.update(ranking={}, errors=[], timings={limit: [] for limit in limits}, failed={limit: 0 for limit in limits},
                            attempts=[], log_begin=jev.offset(side['client'].jev_log))
                gate = side['client'].gate_for(selected_evaluation)
                if gate is not None and selected_evaluation is not None:
                    side['embedding_begin'] = gate.budget.summary(gate.label, name, 'query')
            # The other limits are timed only, after the scored pass, so they never change what it measures.
            for limit in limits:
                for n, q in enumerate(queries):
                    for side in serving if n % 2 == 0 else serving[::-1]:
                        ranked, timing, error = search(side['client'], side['corpus'], data['queries'][q], mode, side['wire_profiles'][profile],
                                                      side['records'], limit, evaluation=selected_evaluation)
                        if limit == scored_limit:
                            side['ranking'][q] = ranked
                            side['attempts'].append(timing)
                            if error:
                                side['errors'].append(error)
                        if error:
                            side['failed'][limit] += 1
                        else:
                            side['timings'][limit].append(timing)
                if limit == scored_limit:
                    for side in serving:
                        side['log_end'] = jev.offset(side['client'].jev_log)
                        gate = side['client'].gate_for(selected_evaluation)
                        if gate is not None and selected_evaluation is not None:
                            after = gate.budget.summary(gate.label, name, 'query')
                            side['embedding_scored'] = {key: after[key] - side['embedding_begin'][key]
                                                        for key in ('confirmed_input_tokens', 'reserved_input_tokens',
                                                                    'cost_upper_bound_usd', 'admitted_calls')}
            system = f'{mode}/{label}'
            print(f'[eval] {name}: {system} searched', flush=True)
            for side in serving:
                events = jev.records(side['client'].jev_log, side['log_begin'], side['log_end'], profile.rsplit('/', 1)[-1])
                accounting = jev.summary(side['attempts'], events, unpaid=options.paid_calls == 0 and not deep)
                latencies = [t['client_ms'] for t in side['timings'][scored_limit]]
                side['out']['systems'][system] = {**scoring.score_set(data, side['ranking']), 'profile_version': side['served'][profile],
                                                  'scored_limit': scored_limit, 'reranker': jev.variant(label) if configuration is not None else None,
                                                  'accounting': {'scored': accounting},
                                                  'latency_ms': {'p50': rounded(percentile(latencies, 50)), 'p95': rounded(percentile(latencies, 95)), 'max': rounded(max(latencies, default=None))},
                                                  'time_by_limit': {str(limit): time_summary(t, side['failed'][limit]) for limit, t in side['timings'].items()},
                                                  'failures': len(side['errors']), 'errors': sorted(set(side['errors']))[:3],
                                                  'paid_calls_per_query': accounting['paid_calls_per_search']}
                if configuration is not None:
                    side['out']['systems'][system]['configuration_seconds'] = side['configuration_seconds']
                if selected_evaluation is not None and 'embedding_scored' in side:
                    usage = side['embedding_scored']
                    side['out']['systems'][system]['embedding_usage'] = usage
                    side['out']['systems'][system]['paid_calls_per_query'] = usage['admitted_calls'] / len(queries) if queries else None
                elif selected_evaluation is not None:
                    # Engine retrieval usage does not include an unmanaged
                    # ingestion owner's provider attempts. Unknown is not zero.
                    side['out']['systems'][system]['paid_calls_per_query'] = None
            publish = getattr(options, 'publish_partial', None)
            if publish:
                for side in serving:
                    publish(name, side['out'])
            if deep and not getattr(options, 'live_paid', False):
                for side in serving:
                    side.update(warm_timings=[], warm_attempts=[], warm_failures=0, warm_begin=jev.offset(side['client'].jev_log))
                for index, query in enumerate(queries):
                    for side in serving if index % 2 == 0 else serving[::-1]:
                        _, timing, error = search(side['client'], side['corpus'], data['queries'][query], 'hybrid', side['wire_profiles'][profile],
                                                  side['records'], DEEP_LIMIT, evaluation=selected_evaluation)
                        side['warm_attempts'].append(timing)
                        if error:
                            side['warm_failures'] += 1
                        else:
                            side['warm_timings'].append(timing)
                for side in serving:
                    events = jev.records(side['client'].jev_log, side['warm_begin'], jev.offset(side['client'].jev_log), profile.rsplit('/', 1)[-1])
                    result = side['out']['systems'][system]
                    result['accounting']['warm_cache'] = jev.summary(side['warm_attempts'], events)
                    result['warm_cache_timing'] = time_summary(side['warm_timings'], side['warm_failures'])
    for side in sides:
        side['out']['status'] = 'completed'
        compare_within(side['out'])
        if getattr(options, 'publish_partial', None):
            options.publish_partial(name, side['out'])
    return [side['out'] for side in sides]


def rounded(v):
    return None if v is None else round(v, 1)


def compare_within(result):
    """Each system against the run's baseline system, paired per query."""
    import scoring
    base = result['systems'].get(BASELINE_SYSTEM)
    if base:
        result['against_baseline_system'] = {s: {m: scoring.paired(v['per_query'][m], base['per_query'][m]) for m in scoring.METRICS}
                                             for s, v in result['systems'].items() if s != BASELINE_SYSTEM}
    result['against_served_mode'] = {}
    for system, value in result['systems'].items():
        mode, label = system.split('/', 1)
        default = result.get('profiles', {}).get('default', 'default')
        baseline = result['systems'].get(f'{mode}/{default}')
        if label.startswith('evaluation/') and baseline:
            result['against_served_mode'][system] = {
                m: scoring.paired(value['per_query'][m], baseline['per_query'][m]) for m in scoring.METRICS}


def compare_runs(current, baseline):
    """Each system against the same system of an earlier run, on sets with the same fingerprint."""
    import scoring
    for name, result in current['sets'].items():
        before = baseline.get('sets', {}).get(name)
        if not before or before['manifest'].get('fingerprint') != result['manifest'].get('fingerprint'):
            result['against_baseline_run'] = None
            continue
        result['against_baseline_run'] = {s: {m: scoring.paired(v['per_query'][m], before['systems'][s]['per_query'][m]) for m in scoring.METRICS}
                                          for s, v in result['systems'].items() if s in before['systems']
                                          and v.get('scored_limit', LIMIT) == before['systems'][s].get('scored_limit', LIMIT)}


def git(*args):
    return subprocess.run(['git', *args], cwd=ROOT, capture_output=True, text=True).stdout.strip()


def checkout(ref):
    """(commit, directory) of ref checked out in a detached worktree; fetched from origin when this clone lacks it."""
    commit = git('rev-parse', '--verify', '--quiet', ref + '^{commit}')
    if not commit:
        subprocess.run(['git', 'fetch', '--quiet', '--no-tags', '--depth=1', 'origin', ref], cwd=ROOT, check=True)
        commit = git('rev-parse', '--verify', '--quiet', 'FETCH_HEAD^{commit}')
    directory = ROOT / '.scratch' / 'eval' / f'source-{commit[:12]}'
    git('worktree', 'remove', '--force', str(directory))
    git('worktree', 'prune')
    subprocess.run(['git', 'worktree', 'add', '--quiet', '--detach', str(directory), commit], cwd=ROOT, check=True)
    return commit, directory


def load_retrieval_pin(path):
    pin = load_object(path)
    if set(pin) - {'manifest', 'endpoint', 'configuration', 'secret_names'}:
        raise ValueError('retrieval pin supports manifest, endpoint, configuration and secret_names')
    for key in ('manifest', 'endpoint'):
        if not isinstance(pin.get(key), str) or not pin[key]:
            raise ValueError(f'retrieval pin requires {key}')
    manifest = (pathlib.Path(path).resolve().parent / pin['manifest']).resolve()
    if not manifest.is_file():
        raise ValueError('retrieval plugin manifest does not exist')
    endpoint = urllib.parse.urlsplit(pin['endpoint'])
    if (endpoint.scheme not in ('http', 'https') or not endpoint.netloc
            or endpoint.username or endpoint.password or endpoint.query or endpoint.fragment):
        raise ValueError('plugin endpoint must be HTTP(S) without credentials, query or fragment')
    if not isinstance(pin.get('configuration', {}), dict):
        raise ValueError('retrieval configuration must be an object')
    names = pin.pop('secret_names', [])
    if (not isinstance(names, list)
            or any(not isinstance(name, str) or not re.fullmatch(r'[A-Z][A-Z0-9_]*', name) for name in names)):
        raise ValueError('secret_names must contain environment variable names')
    if any(not os.environ.get(name) for name in names):
        raise ValueError('a retrieval plugin secret environment variable is absent')
    for name in names:
        os.environ.pop(name)
    pin['manifest'] = str(manifest)
    return pin


def start_stack(phases, stacks, source=ROOT, suffix='', typesafe_key='', ingestion=None, retrieval=None):
    """Start an isolated stack with core and any opted-in plugins.

    Register the stack before startup so the caller can clean up partial starts.
    """
    host_platform = (platform.system(), platform.machine())
    if host_platform not in {('Linux', 'x86_64'), ('Darwin', 'arm64')}:
        sys.exit(f'eval: the local stack needs Linux x86_64 or Apple silicon macOS (this is {"/".join(host_platform)}); use --api-url')
    docker_error = 'eval: Docker is unavailable; install and start Docker, or use --api-url for an existing installation'
    try:
        docker = subprocess.run(['docker', 'info'], capture_output=True, timeout=15)
    except (FileNotFoundError, subprocess.TimeoutExpired):
        sys.exit(docker_error)
    if docker.returncode:
        sys.exit(docker_error)
    import connector_plugin
    import normalizer_plugin
    import subscription_plugin
    from local import GO, Stack, run
    from prepare_embeddings import prepare as prepare_embeddings
    from prepare_tokenizer import prepare as prepare_tokenizer
    class EvalStack(Stack):
        def config(self):
            super().config()
            jev.configure(self)
            if retrieval is not None:
                for name in ('config.json', 'worker.json'):
                    path = self.directory / name
                    config = load_object(path)
                    config.setdefault('plugins', []).append(retrieval)
                    config.setdefault('retrieval', {}).setdefault('profiles', {}).setdefault('default', BASELINE_PROFILE)
                    path.write_text(json.dumps(config))
            if ingestion is not None:
                for selection in evaluation_selections(ingestion):
                    embeddings.install(self.directory, selection)

    stack = EvalStack('quivr-eval-' + uuid.uuid4().hex[:10] + suffix, source)
    stacks.append(stack)
    normalizer_plugin.select(stack, 'none')
    subscription_plugin.select(stack, False)
    connector_plugin.select(stack, False)
    connector_plugin.select_first_party(stack, connector_plugin.CORE)
    stack.check_disk()
    tokenizer = {}
    enabled = source == ROOT and not suffix and bool(typesafe_key)
    steps = [('prepare_tokenizer', lambda: tokenizer.update(prepare_tokenizer())), ('prepare_model', prepare_embeddings)]
    if enabled:
        steps.append(('prepare_jev', lambda: jev.start(stack, tokenizer, typesafe_key)))
    for label, step in steps + [
                        ('go_build', lambda: run([GO, 'build', '-o', str(stack.directory / 'quivr'), './cmd/quivr'], cwd=source)),
                        ('start_dependencies', lambda: stack.compose('up', '-d', '--wait', '--wait-timeout', '300')),
                        ('migrate_and_start', lambda: (stack.migrate(), connector_plugin.start_first_party(stack), stack.start_processes()))]:
        started = time.monotonic()
        step()
        phases[label] = round(time.monotonic() - started, 3)
    client = Client(f"http://127.0.0.1:{stack.state['api_port']}", stack.state['admin'])
    client.deep_enabled = enabled
    client.stack = stack
    client.jev_log = getattr(stack, 'jev_log', None)
    if suffix:
        client.deep_skip_reason = 'comparison stack retains the core retrieval plugin'
    return client


def resolve_sets(options):
    """[(name, directory)] for the requested public sets and the optional private one."""
    chosen = [s for s in options.sets.split(',') if s] if options.sets is not None else list(public_sets.names(options.include_restricted))
    unknown = [s for s in chosen if s not in public_sets.SETS]
    if unknown:
        sys.exit(f"eval: unknown set {', '.join(unknown)}; public sets: {', '.join(public_sets.SETS)}")
    out = [(name, public_sets.prepare(name, options.cache, include_restricted=getattr(options, 'include_restricted', False))) for name in chosen]
    if options.private:
        headers = {'Authorization': os.environ['QUIVR_EVAL_SET_AUTHORIZATION']} if os.environ.get('QUIVR_EVAL_SET_AUTHORIZATION') else None
        path = trec.fetch(options.private, options.private_sha256, options.cache, headers)
        out.append((options.private_name, trec.materialize(path, options.cache)))
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('--sets', help='comma-separated public sets (default: default suite); empty for none')
    parser.add_argument('--include-restricted', action='store_true', help='opt into diagnostic-only sets under their restricted licences')
    parser.add_argument('--list-sets', action='store_true', help='print the licence-tier registry offline and exit')
    parser.add_argument('--private', default=os.environ.get('QUIVR_EVAL_SET', ''), help='a TREC-layout set: directory, archive path or URL (env QUIVR_EVAL_SET)')
    parser.add_argument('--private-sha256', default=os.environ.get('QUIVR_EVAL_SET_SHA256', ''), help='sha256 of the archive; required for a URL (env QUIVR_EVAL_SET_SHA256)')
    parser.add_argument('--private-name', default='private', help='name of the private set in the report')
    parser.add_argument('--baseline', help='report.json of an earlier run to compare each system with, per query')
    parser.add_argument('--api-url', help='measure this installation instead of a local stack; key in QUIVR_EVAL_API_KEY')
    parser.add_argument('--corpus-id', help='reuse this corpus on --api-url for a single set')
    parser.add_argument('--corpus-state', type=pathlib.Path, help='read or save a fingerprinted corpus mapping for one set on --api-url')
    parser.add_argument('--corpus-config', help='JSON retrieval configuration for newly created corpora')
    parser.add_argument('--profiles', action='append', help='select advertised full names or aliases, comma-separated or repeated')
    parser.add_argument('--retrieval-config', help='local candidate retrieval plugin pin with manifest, endpoint, configuration and secret_names')
    parser.add_argument('--evaluation-plugin', help='ingestion plugin owner for an evaluation search cut (requires --evaluation-space and --api-url)')
    parser.add_argument('--evaluation-space', help='vector space name for an evaluation search cut (requires --evaluation-plugin and --api-url)')
    parser.add_argument('--ingestion-config', help='JSON evaluation pin: manifest, endpoint, configuration, secret_names, plugin, space (local stack)')
    parser.add_argument('--compare-to', metavar='REF', help='also measure this git revision on a second local stack, searched alternately with this checkout')
    parser.add_argument('--out', default=str(ROOT / '.scratch/eval/runs' / datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')))
    parser.add_argument('--cache', default=str(ROOT / '.scratch/eval/cache'))
    parser.add_argument('--ingest-timeout', type=int, default=5400, help='seconds for one set to become searchable with vectors')
    parser.add_argument('--stall', type=int, default=600, help='seconds without a Record gaining vectors before failing')
    options = parser.parse_args()
    if options.list_sets:
        print(json.dumps({name: public_sets.SETS[name] for name in public_sets.names(options.include_restricted)}, indent=2))
        return
    if ci_guard.in_ci():
        parser.error('measurements run locally only; use --list-sets for offline CI preview')
    paid_set_selected = options.sets == 'miracl-fr' and not options.private
    if options.sets is None:
        options.sets = ','.join(public_sets.names(options.include_restricted))
    if options.ingestion_config and (options.api_url or options.compare_to or options.evaluation_plugin or options.evaluation_space):
        parser.error('--ingestion-config uses one local stack and excludes existing selection and --compare-to')
    ingestion = embeddings.load_pin(options.ingestion_config) if options.ingestion_config else None
    if bool(options.evaluation_plugin) != bool(options.evaluation_space):
        parser.error('--evaluation-plugin and --evaluation-space must be provided together')
    if (options.evaluation_plugin or options.evaluation_space) and not options.api_url:
        parser.error('evaluation selection requires --api-url')
    if (options.corpus_id is not None or options.corpus_state is not None) and not options.api_url:
        parser.error('--corpus-id and --corpus-state require --api-url')
    if (options.corpus_id or options.corpus_state) and len([name for name in options.sets.split(',') if name]) + bool(options.private) != 1:
        parser.error('--corpus-id and --corpus-state require exactly one evaluation set')
    if options.corpus_config and (options.corpus_id or (options.corpus_state and options.corpus_state.exists())):
        parser.error('--corpus-config requires creating a new corpus')
    if options.retrieval_config and options.api_url:
        parser.error('--retrieval-config configures the local stack; use --profiles with --api-url')
    options.corpus_config = load_object(options.corpus_config) if options.corpus_config else None
    if options.profiles is not None:
        options.profiles = [name.strip() for value in options.profiles for name in value.split(',') if name.strip()]
        if not options.profiles:
            parser.error('--profiles requires at least one profile name')
    typesafe_key = os.environ.pop('TYPESAFE_API_KEY', '').strip()
    retrieval = load_retrieval_pin(options.retrieval_config) if options.retrieval_config else None
    paid_eligible = not options.api_url and paid_set_selected
    typesafe_key = typesafe_key if paid_eligible else ''
    budget = jev.Budget() if typesafe_key else None
    options.live_paid = bool(typesafe_key)
    options.evaluation = ({'plugin': options.evaluation_plugin, 'space': options.evaluation_space}
                          if options.evaluation_plugin else None)
    if ingestion:
        options.evaluation = {key: ingestion[key] for key in ('plugin', 'space')}
    if options.compare_to and (options.api_url or options.baseline):
        parser.error('--compare-to measures two local stacks; it excludes --api-url and --baseline')
    try:
        import scoring  # noqa: F401
        import ranx  # noqa: F401
    except ImportError:
        sys.exit('eval: missing packages; run: python3 -m pip install -r scripts/eval/requirements.txt')

    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)  # a cancelled run still writes its report and cleans up
    out = pathlib.Path(options.out)
    out.mkdir(parents=True, exist_ok=True)
    run_id = uuid.uuid4().hex[:10]
    started = time.monotonic()
    report = {'status': 'failed', 'run': {'id': run_id, 'started_at': now(), 'source_revision': git('rev-parse', 'HEAD'),
                                          'source_dirty': bool(git('status', '--porcelain', '--untracked-files=no')), 'host': host(),
                                          'github': {k: os.environ[k] for k in ['GITHUB_RUN_ID', 'GITHUB_REF_NAME', 'GITHUB_EVENT_NAME'] if k in os.environ},
                                          'phases_seconds': {}},
              'convention': None, 'test': None, 'baseline_system': BASELINE_SYSTEM, 'limit': LIMIT, 'sets': {}}
    if options.evaluation:
        report['run']['evaluation'] = dict(options.evaluation)
    if budget:
        report['run']['paid_policy'] = {'set': 'miracl-fr', 'max_queries': 150, 'candidate_count': 30,
                                       'trim_tokens': 256, 'ranking': 'noul', 'max_input_tokens': jev.MAX_RUN_INPUT_TOKENS,
                                       'max_paid_searches': jev.MAX_RUN_PAID_SEARCHES,
                                       'paid_probes_warmups_replay': False}
    stacks, worktree, watch = [], None, None
    print(f"[eval] host: {report['run']['host'].get('cpu_model', 'unknown CPU')}, {report['run']['host']['logical_cpus']} logical CPUs", flush=True)
    try:
        import scoring
        report.update(convention=scoring.CONVENTION, test=scoring.TEST)
        report['promotion_sets'] = [name for name in options.sets.split(',') if name in public_sets.DEFAULT_SETS]
        sets = resolve_sets(options)
        clients = []
        if options.api_url:
            if not os.environ.get('QUIVR_EVAL_API_KEY'):
                sys.exit('eval: --api-url needs QUIVR_EVAL_API_KEY (corpora:write, content:read, content:write, changes:read, search:query)')
            clients.append(Client(options.api_url, os.environ['QUIVR_EVAL_API_KEY']))
            clients[-1].evaluation = options.evaluation
            clients[-1].deep_enabled = False
            clients[-1].deep_skip_reason = 'paid evaluation requires an isolated local stack'
            options.paid_calls = None
            report['run']['target'] = 'existing installation (--api-url)'
        else:
            if options.compare_to:
                commit, worktree = checkout(options.compare_to)
                report['compare'] = {'ref': options.compare_to, 'source_revision': commit, 'phases_seconds': {}, 'sets': {}}
                clients.append(start_stack(report['compare']['phases_seconds'], stacks, worktree, '-base'))
            clients.append(start_stack(report['run']['phases_seconds'], stacks, typesafe_key=typesafe_key, ingestion=ingestion, retrieval=retrieval))
            clients[-1].budget = budget
            options.paid_calls = 0
            report['run']['target'] = 'isolated local stack, optional reranker' if typesafe_key else 'isolated local stack, core plugins only'
            if retrieval is not None:
                report['run']['target'] = 'isolated local stack with configured retrieval plugin'
            if ingestion is not None:
                report['run']['target'] = 'isolated local stack, core.ingest and configured evaluation owner'
            watch = resources.Watch(stacks)
            watch.snapshot('stack started')
        for name, directory in sets:
            *base, report['sets'][name] = measure_set(clients, name, directory, run_id, options,
                                                     allow_paid=not options.private or name != options.private_name)
            if base:
                report['compare']['sets'][name] = base[0]
            if watch:
                watch.snapshot('after ' + name)
        if 'compare' in report:
            report['baseline_run'] = {'ref': options.compare_to, 'source_revision': report['compare']['source_revision']}
            compare_runs(report, report['compare'])
            print('\n'.join(render.comparison(report)), flush=True)
        if options.baseline:
            baseline = json.loads(pathlib.Path(options.baseline).read_text())
            report['baseline_run'] = {k: baseline.get('run', {}).get(k) for k in ['id', 'started_at', 'source_revision', 'github']}
            compare_runs(report, baseline)
        report['status'] = 'completed'
    except BaseException as error:
        report['error'] = type(error).__name__ + ': ' + str(error)[:1000]
        raise
    finally:
        if budget:
            report['run']['jev_budget'] = budget.summary()
            budget.print()
        report['run'].update(finished_at=now(), duration_seconds=round(time.monotonic() - started, 3))
        if watch:
            watch.snapshot('at the end')
            report['resources'] = watch.summary()
            if report['resources']['cause']:
                print('[eval] ' + report['resources']['cause'], flush=True)
        (out / 'report.json').write_text(json.dumps(report, indent=1, ensure_ascii=False))
        (out / 'report.md').write_text(render.markdown(report))
        print('Evaluation report:', out / 'report.md', flush=True)
        for stack in stacks:
            try:
                try:
                    stack.capture()
                finally:
                    try:
                        jev.stop(stack)
                    finally:
                        stack.down(True)
            except Exception as error:  # the other stack and the worktree are still cleaned up
                print(f'[eval] cleaning up {stack.name} failed: {error}', file=sys.stderr, flush=True)
        if worktree is not None:
            git('worktree', 'remove', '--force', str(worktree))


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds')


def host():
    info = {'machine': platform.machine(), 'system': platform.system(), 'logical_cpus': os.cpu_count(), 'python': platform.python_version()}
    try:
        info['cpu_model'] = re.search(r'model name\s*:\s*(.+)', pathlib.Path('/proc/cpuinfo').read_text()).group(1).strip()
    except (OSError, AttributeError):
        pass
    return info


if __name__ == '__main__':
    main()
