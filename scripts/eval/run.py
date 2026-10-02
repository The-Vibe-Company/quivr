#!/usr/bin/env python3
"""Measure how well Quivr search ranks evaluation sets (THE-775): `make eval`.

For each set (public ones from public_sets.py, or a private one in the TREC layout of trec.py),
ingest its documents into a new Corpus through the public API, wait until every Record has its
vectors, run every query in each search mode and each profile the API serves, and score the
rankings (scoring.py). Writes report.json and report.md; see docs/agents/evaluation.md.

By default it starts an isolated local stack (Linux x86_64 only, like make measure). With
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
import trec  # noqa: E402
from measure_metrics import percentile  # noqa: E402

MODES = ['lexical', 'semantic', 'hybrid']
BASELINE_SYSTEM = 'hybrid/default'  # the API's default mode and profile
LIMIT = 50  # the API maximum; hits are deduplicated by Record before scoring
TIMING_LIMITS = [LIMIT, 10]  # limit 10, the API default, is timed only, in a second pass
PHASES = ['query_encoding_ms', 'index_query_ms', 'hydration_ms', 'plugin_rounds_ms']  # of usage.phases
BATCH_ITEMS, BATCH_BYTES = 100, 8 << 20  # under the batch bounds of 100 entries and 10 MiB
WARMUP_QUERIES = 3
DEEP_LIMIT = 10


class Client:
    def __init__(self, base, key, timeout=60):
        self.base, self.key, self.timeout = base.rstrip('/'), key, timeout
        self.jev_log = None
        self.stack = None
        self.budget = None
        self.deep_enabled = bool(os.environ.get('TYPESAFE_API_KEY'))
        self.deep_skip_reason = 'TYPESAFE_API_KEY absent; paid deep evaluation skipped'

    def call(self, method, path, body=None, expected=(200,), attempts=5):
        paid = method == 'POST' and path == '/v0/search' and body is not None and is_deep(body.get('profile', 'default'))
        if not paid:
            return self._call(method, path, body, expected, attempts)
        if self.budget is None:
            raise RuntimeError('paid deep evaluation requires a run input token budget')
        with self.budget.request_lock:
            self.budget.admit()
            begin = jev.offset(self.jev_log)
            try:
                return self._call(method, path, body, expected, 1)
            finally:
                self.budget.settle(self.jev_log, begin)

    def _call(self, method, path, body, expected, attempts):
        """JSON response of an expected status; 429 and 503 are retried with a bounded backoff."""
        data = json.dumps(body).encode() if body is not None else None
        for attempt in range(attempts):
            request = urllib.request.Request(self.base + path, data=data, method=method,
                                             headers={'Authorization': 'Bearer ' + self.key, 'Content-Type': 'application/json'})
            try:
                response = urllib.request.urlopen(request, timeout=self.timeout)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                raw = response.read()
            result = json.loads(raw) if raw else {}
            if response.status in expected:
                return response.status, result
            if response.status not in (429, 503) or attempt == attempts - 1:
                raise RuntimeError(f'{method} {path} returned {response.status}: {str(result)[:300]}')
            time.sleep(min(2 ** attempt, 10))


def advertised_profiles(client):
    """The profile names the installation lists (GET /v0/search/profiles), default first."""
    _, listed = client.call('GET', '/v0/search/profiles')
    return [p['name'] for p in listed['items']]


def serving_profiles(client, corpus, advertised, probe_query='profile probe', allow_paid=True):
    """Which advertised profiles the installation serves; a refused one is recorded, not measured."""
    served, refused = {}, {}
    for name in advertised:
        if is_deep(name) and not allow_paid:
            refused[name] = 'optional private set excluded from paid evaluation'
            continue
        if is_deep(name) and not client.deep_enabled:
            refused[name] = client.deep_skip_reason
            continue
        if is_deep(name) and client.stack is not None and 'jev_pin' in client.stack.state:
            _, listed = client.call('GET', '/v0/search/profiles')
            provider = next(profile['provider'] for profile in listed['items'] if profile['name'] == name)
            # The same identity a search's retrieval_profile.version carries.
            served[name] = f"plugin:{provider['plugin_id']}@{provider['plugin_version']}/{name}"
            continue
        status, body = client.call('POST', '/v0/search', {'query': probe_query, 'corpus_ids': [corpus], 'mode': 'hybrid' if is_deep(name) else 'lexical', 'profile': name, 'limit': 1}, expected=(200, 422))
        if status == 200:
            served[name] = body['retrieval_profile']['version']
        else:
            refused[name] = f"422 {body.get('code', '')}".strip()
    return served, refused


def is_deep(profile):
    return profile == 'deep' or profile.startswith('deep-k')


def measurements(sides):
    profiles = list(dict.fromkeys(profile for side in sides for profile in side['served']))
    profiles.sort(key=lambda profile: (profile != 'default', is_deep(profile)))
    for profile in profiles:
        if profile == 'deep' and any(side['client'].stack is not None and 'jev_pin' in side['client'].stack.state for side in sides):
            for label, configuration in jev.matrix():
                yield profile, label, configuration
        else:
            yield profile, profile, None


def deep_warmups(queries, run_id):
    result = []
    for index in range(WARMUP_QUERIES):
        query = f'evaluation warmup {run_id} {index}'
        while query in queries:
            query += ' warmup'
        result.append(query)
    return result


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


def ingest(client, corpus, namespace, corpus_docs, timeout, stall):
    """Submit every document, then wait on the change feed until each Record has its vectors.

    Returns {record_id: doc_id} and timings. Waiting reads record.enrichment_available events
    from a cursor taken before the first submission, so no Record is missed and nothing sleeps
    for a fixed time; a feed that stops moving for `stall` seconds fails with receipt details."""
    _, page = client.call('GET', f'/v0/changes?corpus_id={urllib.parse.quote(corpus)}')
    start = time.monotonic()
    receipts = []
    for batch in batches(command(corpus, namespace, key, doc) for key, doc in corpus_docs.items()):
        _, result = client.call('POST', '/v0/records/batch', {'items': batch})
        for outcome in result['items']:
            if 'receipt' not in outcome:
                raise RuntimeError(f"entry {outcome.get('index')} refused: {outcome.get('error')}")
            receipts.append(outcome['receipt']['receipt_id'])
    accepted = time.monotonic() - start
    enriched, moved = set(), time.monotonic()
    while len(enriched) < len(corpus_docs):
        _, page = client.call('GET', f'/v0/changes?corpus_id={urllib.parse.quote(corpus)}&cursor={urllib.parse.quote(page["next_cursor"])}&limit=100')
        new = {e['resource']['id'] for e in page['items'] if e['type'] == 'record.enrichment_available'} - enriched
        if new:
            enriched |= new
            moved = time.monotonic()
        now = time.monotonic()
        if now - moved > stall or now - start > timeout:
            raise RuntimeError(f'{namespace}: {len(enriched)}/{len(corpus_docs)} Records have vectors after {round(now - start)} s; '
                               f'first receipts not done: {stuck(client, receipts)}')
        if not page['has_more'] and not new:
            time.sleep(1)  # poll interval of the change feed, not a wait for a result
    records = {}
    _, page = client.call('GET', f'/v0/records?corpus_id={urllib.parse.quote(corpus)}&limit=100')
    while True:
        records.update({r['record_id']: r['source']['record_key'] for r in page['items']})
        if not page.get('next_page_cursor'):
            break
        _, page = client.call('GET', f"/v0/records?corpus_id={urllib.parse.quote(corpus)}&limit=100&page_cursor={urllib.parse.quote(page['next_page_cursor'])}")
    return records, {'records': len(records), 'accepted_seconds': round(accepted, 3), 'searchable_with_vectors_seconds': round(time.monotonic() - start, 3)}


def stuck(client, receipts, show=3):
    out = []
    for rid in receipts:
        _, r = client.call('GET', '/v0/ingestion-receipts/' + rid)
        if r.get('availability', {}).get('state') != 'retrieval_ready' or r.get('processing', {}).get('state') != 'idle':
            out.append({k: r.get(k) for k in ['receipt_id', 'state', 'outcome', 'availability', 'processing', 'diagnostics']})
            if len(out) == show:
                break
    return out or 'every receipt is retrieval_ready and idle'


def search(client, corpus, query, mode, profile, keys, limit=LIMIT):
    """(ranked doc ids, timing, error): hits deduplicated by Record, best first.

    timing is the client's milliseconds, with the engine's usage (elapsed_ms and its phases) when it reports them."""
    start = time.perf_counter()
    try:
        _, result = client.call('POST', '/v0/search', {'query': query, 'corpus_ids': [corpus], 'mode': mode, 'profile': profile, 'limit': limit}, attempts=1)
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


def measure_set(clients, name, directory, run_id, options, allow_paid=True):
    """One result per client, in order. With two clients (--compare-to) each query is searched on
    both, alternating which goes first, so drift on the machine weighs on both sides alike."""
    import scoring
    data = trec.load(directory)
    allow_paid = allow_paid and name == 'miracl-fr'
    if allow_paid and any(client.deep_enabled for client in clients):
        selected = sorted(data['queries'])[:150]
        data['queries'] = {query: data['queries'][query] for query in selected}
        data['qrels'] = {query: data['qrels'][query] for query in selected}
    manifest_path = pathlib.Path(directory) / 'manifest.json'
    manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else {'name': name, 'fingerprint': trec.fingerprint(directory)}
    namespace = f'eval-{name}'
    deep_queries = deep_warmups(set(data['queries'].values()), run_id)
    sides = []
    for client in clients:
        _, created = client.call('POST', '/v0/corpora', {'name': f'Evaluation {name}', 'idempotency_key': f'eval-{name}-{run_id}'}, expected=(201,))
        corpus = created['corpus_id']
        print(f'[eval] {name}: ingesting {len(data["corpus"])} documents', flush=True)
        records, ingestion = ingest(client, corpus, namespace, data['corpus'], options.ingest_timeout, options.stall)
        advertised = advertised_profiles(client)
        served, refused = serving_profiles(client, corpus, advertised, deep_queries[0], allow_paid=allow_paid)
        sides.append({'client': client, 'corpus': corpus, 'records': records, 'served': served,
                      'out': {'manifest': manifest, 'queries': len(data['queries']), 'documents': len(data['corpus']),
                              'dropped_queries': len(data['dropped_queries']), 'ingestion': ingestion,
                              'profiles': {'advertised': advertised, 'served': served, 'refused': refused}, 'systems': {}}})
    queries = sorted(data['queries'])
    reranking = any('deep' in side['served'] for side in sides)
    for profile, label, configuration in measurements(sides):
        deep = is_deep(profile)
        scored_limit = DEEP_LIMIT if deep or reranking else LIMIT
        limits = [scored_limit] if deep else [scored_limit] + [limit for limit in TIMING_LIMITS if limit != scored_limit]
        for mode in ['hybrid'] if deep else MODES:
            serving = [side for side in sides if profile in side['served']]
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
                    search(side['client'], side['corpus'], query, mode, profile, side['records'], scored_limit)
            for side in serving:
                side.update(ranking={}, errors=[], timings={limit: [] for limit in limits}, failed={limit: 0 for limit in limits},
                            attempts=[], log_begin=jev.offset(side['client'].jev_log))
            # The other limits are timed only, after the scored pass, so they never change what it measures.
            for limit in limits:
                for n, q in enumerate(queries):
                    for side in serving if n % 2 == 0 else serving[::-1]:
                        ranked, timing, error = search(side['client'], side['corpus'], data['queries'][q], mode, profile, side['records'], limit)
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
            system = f'{mode}/{label}'
            print(f'[eval] {name}: {system} searched', flush=True)
            for side in serving:
                events = jev.records(side['client'].jev_log, side['log_begin'], side['log_end'], profile)
                accounting = jev.summary(side['attempts'], events, unpaid=options.paid_calls == 0 and not deep)
                latencies = [t['client_ms'] for t in side['timings'][scored_limit]]
                side['out']['systems'][system] = {**scoring.score(data['qrels'], side['ranking']), 'profile_version': side['served'][profile],
                                                  'scored_limit': scored_limit, 'reranker': jev.variant(label) if configuration is not None else None,
                                                  'accounting': {'scored': accounting},
                                                  'latency_ms': {'p50': rounded(percentile(latencies, 50)), 'p95': rounded(percentile(latencies, 95)), 'max': rounded(max(latencies, default=None))},
                                                  'time_by_limit': {str(limit): time_summary(t, side['failed'][limit]) for limit, t in side['timings'].items()},
                                                  'failures': len(side['errors']), 'errors': sorted(set(side['errors']))[:3],
                                                  'paid_calls_per_query': accounting['paid_calls_per_search']}
                if configuration is not None:
                    side['out']['systems'][system]['configuration_seconds'] = side['configuration_seconds']
            if deep and not getattr(options, 'live_paid', False):
                for side in serving:
                    side.update(warm_timings=[], warm_attempts=[], warm_failures=0, warm_begin=jev.offset(side['client'].jev_log))
                for index, query in enumerate(queries):
                    for side in serving if index % 2 == 0 else serving[::-1]:
                        _, timing, error = search(side['client'], side['corpus'], data['queries'][query], 'hybrid', profile, side['records'], DEEP_LIMIT)
                        side['warm_attempts'].append(timing)
                        if error:
                            side['warm_failures'] += 1
                        else:
                            side['warm_timings'].append(timing)
                for side in serving:
                    events = jev.records(side['client'].jev_log, side['warm_begin'], jev.offset(side['client'].jev_log), profile)
                    result = side['out']['systems'][system]
                    result['accounting']['warm_cache'] = jev.summary(side['warm_attempts'], events)
                    result['warm_cache_timing'] = time_summary(side['warm_timings'], side['warm_failures'])
    for side in sides:
        compare_within(side['out'])
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


def legacy_systems(systems):
    """Systems of a run made before the default profile was renamed from balanced, under today's names."""
    return {(s[:-len('/balanced')] + '/default' if s.endswith('/balanced') else s): v for s, v in systems.items()}


def compare_runs(current, baseline):
    """Each system against the same system of an earlier run, on sets with the same fingerprint."""
    import scoring
    for name, result in current['sets'].items():
        before = baseline.get('sets', {}).get(name)
        if before:
            before = {**before, 'systems': legacy_systems(before.get('systems', {}))}
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


def start_stack(phases, stacks, source=ROOT, suffix='', typesafe_key=''):
    """A client of an isolated local stack with only the core plugins pinned (core.ingest, core.retrieve), as make measure runs it,
    built from source; appended to stacks as soon as it exists, so a failed start is still cleaned up."""
    if platform.system() != 'Linux' or platform.machine() != 'x86_64':
        sys.exit(f'eval: the local stack needs Linux x86_64 (this is {platform.system()}/{platform.machine()}); use --api-url for an existing installation')
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
                        ('migrate_and_start', lambda: (stack.migrate(), connector_plugin.start_first_party(stack, only=['core-ingest'] if enabled else None), stack.start_processes()))]:
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
    chosen = [s for s in options.sets.split(',') if s]
    unknown = [s for s in chosen if s not in public_sets.SETS]
    if unknown:
        sys.exit(f"eval: unknown set {', '.join(unknown)}; public sets: {', '.join(public_sets.SETS)}")
    out = [(name, public_sets.prepare(name, options.cache)) for name in chosen]
    if options.private:
        headers = {'Authorization': os.environ['QUIVR_EVAL_SET_AUTHORIZATION']} if os.environ.get('QUIVR_EVAL_SET_AUTHORIZATION') else None
        path = trec.fetch(options.private, options.private_sha256, options.cache, headers)
        out.append((options.private_name, trec.materialize(path, options.cache)))
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('--sets', default=','.join(public_sets.SETS), help='comma-separated public sets (default: all); empty for none')
    parser.add_argument('--private', default=os.environ.get('QUIVR_EVAL_SET', ''), help='a TREC-layout set: directory, archive path or URL (env QUIVR_EVAL_SET)')
    parser.add_argument('--private-sha256', default=os.environ.get('QUIVR_EVAL_SET_SHA256', ''), help='sha256 of the archive; required for a URL (env QUIVR_EVAL_SET_SHA256)')
    parser.add_argument('--private-name', default='private', help='name of the private set in the report')
    parser.add_argument('--baseline', help='report.json of an earlier run to compare each system with, per query')
    parser.add_argument('--api-url', help='measure this installation instead of a local stack; key in QUIVR_EVAL_API_KEY')
    parser.add_argument('--compare-to', metavar='REF', help='also measure this git revision on a second local stack, searched alternately with this checkout')
    parser.add_argument('--out', default=str(ROOT / '.scratch/eval/runs' / datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')))
    parser.add_argument('--cache', default=str(ROOT / '.scratch/eval/cache'))
    parser.add_argument('--ingest-timeout', type=int, default=5400, help='seconds for one set to become searchable with vectors')
    parser.add_argument('--stall', type=int, default=600, help='seconds without a Record gaining vectors before failing')
    options = parser.parse_args()
    typesafe_key = os.environ.pop('TYPESAFE_API_KEY', '').strip()
    if typesafe_key:
        if options.api_url or [name.strip() for name in options.sets.split(',') if name.strip()] != ['miracl-fr']:
            parser.error('paid evaluation requires a local miracl-fr-only run')
        options.private = ''
    budget = jev.Budget() if typesafe_key else None
    options.live_paid = bool(typesafe_key)
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
        sets = resolve_sets(options)
        clients = []
        if options.api_url:
            if not os.environ.get('QUIVR_EVAL_API_KEY'):
                sys.exit('eval: --api-url needs QUIVR_EVAL_API_KEY (corpora:write, content:read, content:write, changes:read, search:query)')
            clients.append(Client(options.api_url, os.environ['QUIVR_EVAL_API_KEY']))
            clients[-1].deep_enabled = bool(typesafe_key)
            options.paid_calls = None
            report['run']['target'] = 'existing installation (--api-url)'
        else:
            if options.compare_to:
                commit, worktree = checkout(options.compare_to)
                report['compare'] = {'ref': options.compare_to, 'source_revision': commit, 'phases_seconds': {}, 'sets': {}}
                clients.append(start_stack(report['compare']['phases_seconds'], stacks, worktree, '-base'))
            clients.append(start_stack(report['run']['phases_seconds'], stacks, typesafe_key=typesafe_key))
            clients[-1].budget = budget
            options.paid_calls = 0
            report['run']['target'] = 'isolated local stack, optional reranker' if typesafe_key else 'isolated local stack, core plugins only'
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
