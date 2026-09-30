#!/usr/bin/env python3
"""Measure how well Quivr search ranks evaluation sets (THE-775): `make eval`.

For each set (public ones from public_sets.py, or a private one in the TREC layout of trec.py),
ingest its documents into a new Corpus through the public API, wait until every Record has its
vectors, run every query in each search mode and each profile the API serves, and score the
rankings (scoring.py). Writes report.json and report.md; see docs/agents/evaluation.md.

By default it starts an isolated local stack (Linux x86_64 only, like make measure). With
--api-url it measures an existing installation instead, using the key in QUIVR_EVAL_API_KEY.
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
import report as render  # noqa: E402
import trec  # noqa: E402
from measure_metrics import percentile  # noqa: E402

MODES = ['lexical', 'semantic', 'hybrid']
BASELINE_SYSTEM = 'hybrid/default'  # the API's default mode and profile
LIMIT = 50  # the API maximum; hits are deduplicated by Record before scoring
BATCH_ITEMS, BATCH_BYTES = 100, 8 << 20  # under the batch bounds of 100 entries and 10 MiB
WARMUP_QUERIES = 3


class Client:
    def __init__(self, base, key, timeout=60):
        self.base, self.key, self.timeout = base.rstrip('/'), key, timeout

    def call(self, method, path, body=None, expected=(200,), attempts=5):
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


def serving_profiles(client, corpus, advertised):
    """Which advertised profiles the installation serves; a refused one is recorded, not measured."""
    served, refused = {}, {}
    for name in advertised:
        status, body = client.call('POST', '/v0/search', {'query': 'profile probe', 'corpus_ids': [corpus], 'mode': 'lexical', 'profile': name, 'limit': 1}, expected=(200, 422))
        if status == 200:
            served[name] = body['retrieval_profile']['version']
        else:
            refused[name] = f"422 {body.get('code', '')}".strip()
    return served, refused


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


def search(client, corpus, query, mode, profile, keys):
    """(ranked doc ids, milliseconds, error): hits deduplicated by Record, best first."""
    start = time.perf_counter()
    try:
        _, result = client.call('POST', '/v0/search', {'query': query, 'corpus_ids': [corpus], 'mode': mode, 'profile': profile, 'limit': LIMIT}, attempts=1)
    except Exception as error:  # a failed search scores 0 and is counted, never hidden
        return [], (time.perf_counter() - start) * 1000, str(error)[:200]
    ms = (time.perf_counter() - start) * 1000
    ranked = []
    for hit in result['items']:
        doc = keys.get(hit['record_id'])
        if doc is not None and doc not in ranked:
            ranked.append(doc)
    return ranked, ms, None


def measure_set(client, name, directory, run_id, options):
    import scoring
    data = trec.load(directory)
    manifest_path = pathlib.Path(directory) / 'manifest.json'
    manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else {'name': name, 'fingerprint': trec.fingerprint(directory)}
    namespace = f'eval-{name}'
    _, created = client.call('POST', '/v0/corpora', {'name': f'Evaluation {name}', 'idempotency_key': f'eval-{name}-{run_id}'}, expected=(201,))
    corpus = created['corpus_id']
    print(f'[eval] {name}: ingesting {len(data["corpus"])} documents', flush=True)
    records, ingestion = ingest(client, corpus, namespace, data['corpus'], options.ingest_timeout, options.stall)
    advertised = advertised_profiles(client)
    served, refused = serving_profiles(client, corpus, advertised)
    out = {'manifest': manifest, 'queries': len(data['queries']), 'documents': len(data['corpus']),
           'dropped_queries': len(data['dropped_queries']), 'ingestion': ingestion,
           'profiles': {'advertised': advertised, 'served': served, 'refused': refused}, 'systems': {}}
    queries = sorted(data['queries'])
    for profile in served:
        for mode in MODES:
            for q in queries[:WARMUP_QUERIES]:
                search(client, corpus, data['queries'][q], mode, profile, records)
            ranking, latencies, errors = {}, [], []
            for q in queries:
                ranking[q], ms, error = search(client, corpus, data['queries'][q], mode, profile, records)
                if error:
                    errors.append(error)
                else:
                    latencies.append(ms)
            system = f'{mode}/{profile}'
            print(f'[eval] {name}: {system} searched', flush=True)
            out['systems'][system] = {**scoring.score(data['qrels'], ranking), 'profile_version': served[profile],
                                      'latency_ms': {'p50': rounded(percentile(latencies, 50)), 'p95': rounded(percentile(latencies, 95)), 'max': rounded(max(latencies, default=None))},
                                      'failures': len(errors), 'errors': sorted(set(errors))[:3], 'paid_calls_per_query': options.paid_calls}
    compare_within(out)
    return out


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
                                          for s, v in result['systems'].items() if s in before['systems']}


def git(*args):
    return subprocess.run(['git', *args], cwd=ROOT, capture_output=True, text=True).stdout.strip()


def start_stack(phases):
    """An isolated local stack with no plugin pinned: the engine alone, as make measure runs it."""
    if platform.system() != 'Linux' or platform.machine() != 'x86_64':
        sys.exit(f'eval: the local stack needs Linux x86_64 (this is {platform.system()}/{platform.machine()}); use --api-url for an existing installation')
    import connector_plugin
    import normalizer_plugin
    import subscription_plugin
    from local import GO, Stack, run
    from prepare_embeddings import prepare as prepare_embeddings
    from prepare_tokenizer import prepare as prepare_tokenizer
    stack = Stack('quivr-eval-' + uuid.uuid4().hex[:10])
    normalizer_plugin.select(stack, 'none')
    subscription_plugin.select(stack, False)
    connector_plugin.select(stack, False)
    connector_plugin.select_first_party(stack, [])
    stack.check_disk()
    for label, step in [('prepare_tokenizer', prepare_tokenizer), ('prepare_model', prepare_embeddings),
                        ('go_build', lambda: run([GO, 'build', '-o', str(stack.directory / 'quivr'), './cmd/quivr'])),
                        ('start_dependencies', lambda: stack.compose('up', '-d', '--wait', '--wait-timeout', '300')),
                        ('migrate_and_start', lambda: (stack.migrate(), stack.start_processes()))]:
        started = time.monotonic()
        step()
        phases[label] = round(time.monotonic() - started, 3)
    return stack, Client(f"http://127.0.0.1:{stack.state['api_port']}", stack.state['admin'])


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
    parser.add_argument('--out', default=str(ROOT / '.scratch/eval/runs' / datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')))
    parser.add_argument('--cache', default=str(ROOT / '.scratch/eval/cache'))
    parser.add_argument('--ingest-timeout', type=int, default=5400, help='seconds for one set to become searchable with vectors')
    parser.add_argument('--stall', type=int, default=600, help='seconds without a Record gaining vectors before failing')
    options = parser.parse_args()
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
    stack = None
    try:
        import scoring
        report.update(convention=scoring.CONVENTION, test=scoring.TEST)
        sets = resolve_sets(options)
        if options.api_url:
            if not os.environ.get('QUIVR_EVAL_API_KEY'):
                sys.exit('eval: --api-url needs QUIVR_EVAL_API_KEY (corpora:write, content:read, content:write, changes:read, search:query)')
            client, options.paid_calls = Client(options.api_url, os.environ['QUIVR_EVAL_API_KEY']), None
            report['run']['target'] = 'existing installation (--api-url)'
        else:
            stack, client = start_stack(report['run']['phases_seconds'])
            options.paid_calls = 0  # the local stack embeds with its own TEI and calls no paid service
            report['run']['target'] = 'isolated local stack, no plugin pinned'
        for name, directory in sets:
            report['sets'][name] = measure_set(client, name, directory, run_id, options)
        if options.baseline:
            baseline = json.loads(pathlib.Path(options.baseline).read_text())
            report['baseline_run'] = {k: baseline.get('run', {}).get(k) for k in ['id', 'started_at', 'source_revision', 'github']}
            compare_runs(report, baseline)
        report['status'] = 'completed'
    except BaseException as error:
        report['error'] = type(error).__name__ + ': ' + str(error)[:1000]
        raise
    finally:
        report['run'].update(finished_at=now(), duration_seconds=round(time.monotonic() - started, 3))
        (out / 'report.json').write_text(json.dumps(report, indent=1, ensure_ascii=False))
        (out / 'report.md').write_text(render.markdown(report))
        print('Evaluation report:', out / 'report.md', flush=True)
        if stack is not None:
            try:
                stack.capture()
            finally:
                stack.down(True)


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
