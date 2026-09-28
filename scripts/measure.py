#!/usr/bin/env python3
"""Measure lexical, semantic and hybrid public search on the frozen THE-661 workload.

Runs an isolated real stack (Linux x86_64 only, like the rest of the harness),
records cold/warm phases, quality and latency, and writes measurement.json and
measurement.md. Harness or dependency errors exit nonzero; a missed latency target
or a relevance deficit is a reported finding, not a failure.
"""
import concurrent.futures
import datetime
import hashlib
import json
import os
import pathlib
import platform
import re
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid

import measure_metrics as m

ROOT = pathlib.Path(__file__).resolve().parents[1]
WORKLOAD = ROOT / 'tests/measurement/workload-v1.json'
THE_641 = 'https://linear.app/thevibecompany/issue/THE-641'
PRIOR = {'source': 'prototype 1e4446a public-search-report.md', 'semantic': {'mrr_at_10': 0.9583, 'recall_at_3': 1.0}, 'hybrid': {'mrr_at_10': 0.7969, 'recall_at_3': 0.9583}}


class Client:
    def __init__(self, base, token, timeout):
        self.base, self.token, self.timeout = base, token, timeout

    def call(self, method, path, body=None, expected=200):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method, headers={'Authorization': 'Bearer ' + self.token, 'Content-Type': 'application/json'})
        try:
            response = urllib.request.urlopen(req, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            result = json.load(response)
            if response.status != expected:
                raise RuntimeError(f'{method} {path} returned {response.status}: {result}')
            return result

    def timed_search(self, query):
        start = time.perf_counter()
        try:
            result = self.call('POST', '/v0/search', query)
            return {'ok': True, 'ms': (time.perf_counter() - start) * 1000, 'result': result}
        except Exception as error:  # timeouts, refused connections and non-200 are failures, retained
            return {'ok': False, 'ms': (time.perf_counter() - start) * 1000, 'error': type(error).__name__ + ': ' + str(error)[:160]}


def output(*args):
    return subprocess.run(args, cwd=ROOT, capture_output=True, text=True).stdout.strip()


def sha256(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


def host():
    info = {'platform': platform.platform(), 'machine': platform.machine(), 'logical_cpus': os.cpu_count(), 'python': platform.python_version(),
            'go': output(os.environ.get('GO', 'go'), 'version'), 'docker': output('docker', 'version', '--format', '{{.Server.Version}}'),
            'docker_resources': output('docker', 'info', '--format', '{{.NCPU}} CPUs, {{.MemTotal}} bytes'),
            'github_runner': {k: os.environ[k] for k in ['RUNNER_OS', 'RUNNER_ARCH', 'RUNNER_NAME', 'ImageOS', 'ImageVersion'] if k in os.environ}}
    for path, key, pattern in [('/proc/cpuinfo', 'cpu_model', r'model name\s*:\s*(.+)'), ('/proc/meminfo', 'memory_total', r'MemTotal:\s*(.+)')]:
        try:
            found = re.search(pattern, pathlib.Path(path).read_text())
            info[key] = found.group(1).strip() if found else None
        except OSError:
            info[key] = None
    return info


def pins():
    compose = (ROOT / 'deploy/compose/compose.yaml').read_text()
    return {'source_revision': output('git', 'rev-parse', 'HEAD'), 'source_dirty': bool(output('git', 'status', '--porcelain', '--untracked-files=no')),
            'images': sorted(set(re.findall(r'image:\s*(\S+)', compose))),
            'model_lock': json.loads((ROOT / 'third_party/e5/model-lock.json').read_text()),
            'processing_profile_sha256': sha256('internal/processing/profile.json'),
            'tokenizer_requirements_sha256': sha256('third_party/tokenizer/requirements-linux-x86_64.txt'),
            'workload_sha256': sha256('tests/measurement/workload-v1.json'),
            'hybrid_recipe_source': 'internal/adapters/weaviate/projection.go (unchanged by this measurement)'}


class ResourceSampler(threading.Thread):
    """Peak CPU/memory of this project's containers and host API/worker processes."""

    def __init__(self, stack):
        super().__init__(daemon=True)
        self.stack, self.stop, self.peaks, self.samples, self.errors = stack, threading.Event(), {}, 0, []

    def peak(self, name, cpu, mem_mib):
        p = self.peaks.setdefault(name, {'cpu_percent_peak': 0.0, 'memory_mib_peak': 0.0})
        p['cpu_percent_peak'] = max(p['cpu_percent_peak'], round(cpu, 1))
        p['memory_mib_peak'] = max(p['memory_mib_peak'], round(mem_mib, 1))

    def run(self):
        while not self.stop.is_set():
            try:
                self.sample()
            except Exception as error:  # sampling is diagnostic; keep the measurement running
                self.errors.append(str(error)[:160])
            self.samples += 1
            self.stop.wait(2)

    def sample(self):
        units = {'B': 1 / 2**20, 'KiB': 1 / 1024, 'MiB': 1, 'GiB': 1024, 'kB': 1e3 / 2**20, 'MB': 1e6 / 2**20, 'GB': 1e9 / 2**20}
        for line in output('docker', 'stats', '--no-stream', '--format', '{{json .}}').splitlines():
            row = json.loads(line)
            if not row['Name'].startswith(self.stack.name):
                continue
            used = re.match(r'([\d.]+)\s*([A-Za-z]+)', row['MemUsage'])
            self.peak(row['Name'][len(self.stack.name) + 1:].rsplit('-', 1)[0], float(row['CPUPerc'].rstrip('%') or 0), float(used.group(1)) * units.get(used.group(2), 0))
        for pid, name in zip(self.stack.state['pids'], ['api', 'worker']):
            values = output('ps', '-o', 'rss=,pcpu=', '-p', str(pid)).split()
            if len(values) == 2:
                self.peak('quivr-' + name, float(values[1]), int(values[0]) / 1024)


class Distractors(threading.Thread):
    """Background public ingestion at the declared rate into a second Corpus."""

    def __init__(self, client, corpus, spec):
        super().__init__(daemon=True)
        self.client, self.corpus, self.spec = client, corpus, spec
        self.stop, self.accepted, self.errors = threading.Event(), 0, []

    def run(self):
        interval, i = 1 / self.spec['rate_per_second'], 0
        text = self.spec['text_template']
        next_at = time.monotonic()
        while not self.stop.is_set():
            key = f'distractor-{i}'
            command = {'idempotency_key': key, 'source': {'corpus_id': self.corpus, 'namespace': 'measurement-distractors', 'record_key': key}, 'content': {'kind': 'text', 'text': text.format(i=i)}}
            try:
                self.client.call('POST', '/v0/records', command, 202)
                self.accepted += 1
            except Exception as error:
                self.errors.append(str(error)[:160])
            i += 1
            next_at += interval
            self.stop.wait(max(0, next_at - time.monotonic()))


def timed(label, phases, fn):
    start = time.monotonic()
    result = fn()
    phases[label] = round(time.monotonic() - start, 3)
    return result


def model_ready(client, corpus, deadline_seconds=180):
    """First 200 semantic search: the query embedding went through TEI end to end."""
    deadline = time.monotonic() + deadline_seconds
    while True:
        sample = client.timed_search({'query': 'préparation du modèle', 'corpus_ids': [corpus], 'mode': 'semantic'})
        if sample['ok']:
            return
        if time.monotonic() > deadline:
            raise RuntimeError('model never ready: ' + sample['error'])
        time.sleep(0.2)


def ingest_fixture(client, corpus, rows):
    receipts = {}
    for row in rows:
        parts = [{'key': 'title', 'role': 'title', 'content': {'kind': 'text', 'text': row[1]}}, {'key': 'body', 'role': 'body', 'content': {'kind': 'text', 'text': row[2]}}]
        command = {'idempotency_key': 'fixture-' + row[0], 'source': {'corpus_id': corpus, 'namespace': 'relevance-v1', 'record_key': row[0]}, 'content': {'kind': 'manifest', 'parts': parts}}
        receipts[row[0]] = client.call('POST', '/v0/records', command, 202)['receipt_id']
    records, deadline = {}, time.monotonic() + 240
    for key, rid in receipts.items():
        while True:
            r = client.call('GET', '/v0/ingestion-receipts/' + rid)
            if r['state'] == 'resolved' and r.get('outcome') != 'created':
                raise RuntimeError(f'fixture {key} not created: {r}')
            if r.get('availability', {}).get('searchable') and r['processing']['state'] == 'idle':
                records[key] = r['record_id']
                break
            if time.monotonic() > deadline:
                raise RuntimeError(f'fixture {key} never searchable and idle: {r}')
            time.sleep(0.2)
    # Semantic search drops hits without committed vectors, so all 24 prove enrichment is complete.
    while True:
        hits = client.call('POST', '/v0/search', {'query': 'dépêche', 'corpus_ids': [corpus], 'mode': 'semantic', 'limit': 50})['items']
        if {h['record_id'] for h in hits} >= set(records.values()):
            return records
        if time.monotonic() > deadline:
            raise RuntimeError(f'only {len(hits)} fixture Records have vectors')
        time.sleep(0.5)


def run_latency(client, corpus, plan, rows, concurrency, search):
    query_text = {r[0]: r[4] for r in rows}
    def one(item):
        key, mode = item
        sample = client.timed_search({'query': query_text[key], 'corpus_ids': [corpus], 'mode': mode, 'profile': search['profile'], 'limit': search['limit']})
        sample.pop('result', None)
        return mode, sample
    by_mode = {}
    if concurrency == 1:
        results = map(one, plan)
    else:
        with concurrent.futures.ThreadPoolExecutor(concurrency) as pool:
            results = list(pool.map(one, plan))
    for mode, sample in results:
        by_mode.setdefault(mode, []).append(sample)
    return by_mode


def measure(stack, workload, rows, report):
    """Fill report in place so an interrupted or failed run keeps what it already measured."""
    from prepare_embeddings import prepare as prepare_embeddings
    from prepare_tokenizer import prepare as prepare_tokenizer
    from local import GO, run
    report.update(host=host(), pins=pins(), phases_seconds={}, fixture_sha256=workload['fixture']['sha256'])
    phases = report['phases_seconds']
    timed('prepare_tokenizer', phases, prepare_tokenizer)
    report['embedding_preparation'] = timed('prepare_model', phases, prepare_embeddings)
    timed('go_build', phases, lambda: run([GO, 'build', '-o', str(stack.directory / 'quivr'), './cmd/quivr']))
    timed('image_pull', phases, lambda: stack.compose('pull', '--quiet'))

    def start():
        stack.compose('up', '-d', '--wait', '--wait-timeout', '300')
        stack.migrate()
        stack.start_processes()

    cfg = workload['search']
    def client():
        return Client(f"http://127.0.0.1:{stack.state['api_port']}", stack.state['admin'], cfg['request_timeout_seconds'])

    timed('cold_start', phases, start)
    probe = client().call('POST', '/v0/corpora', {'name': 'Model readiness', 'idempotency_key': 'measure-probe'}, 201)['corpus_id']
    timed('cold_model_ready', phases, lambda: model_ready(client(), probe))
    stack.stop_processes()
    stack.compose('down', '--volumes')
    timed('warm_start', phases, start)
    c = client()
    probe = c.call('POST', '/v0/corpora', {'name': 'Model readiness', 'idempotency_key': 'measure-probe'}, 201)['corpus_id']
    timed('warm_model_ready', phases, lambda: model_ready(c, probe))

    corpus = c.call('POST', '/v0/corpora', {'name': 'Retrieval baseline v1', 'idempotency_key': 'measure-fixture'}, 201)['corpus_id']
    records = timed('fixture_ingest_to_vectors', phases, lambda: ingest_fixture(c, corpus, rows))
    keys = {v: k for k, v in records.items()}

    quality = report['quality'] = {}
    profile = None
    for mode in cfg['modes']:
        ranks, details = [], []
        for row in rows:
            result = c.call('POST', '/v0/search', {'query': row[4], 'corpus_ids': [corpus], 'mode': mode, 'profile': cfg['profile'], 'limit': cfg['limit']})
            profile = result['retrieval_profile']
            rank = m.rank_of(result['items'], records[row[0]])
            ranks.append(rank)
            details.append({'query_id': row[0], 'rank': rank, 'top10': [keys.get(h['record_id'], 'other') for h in result['items']]})
        quality[mode] = {**m.quality(ranks), 'per_query': details}
    report['retrieval_profile'] = profile

    lat = workload['latency']
    ids = [r[0] for r in rows]
    target = lat['target']['p95_ms_below']
    warmup = run_latency(c, corpus, m.schedule(ids, cfg['modes'], lat['warmup_passes'], lat['seed']), rows, 1, cfg)
    report['warmup_failures'] = {mode: sum(1 for s in samples if not s['ok']) for mode, samples in warmup.items()}
    plan = m.schedule(ids, cfg['modes'], lat['measured_passes'], lat['seed'])
    sampler = ResourceSampler(stack)
    sampler.start()
    latency = report['latency'] = {}
    try:
        for condition in lat['conditions']:
            distractors = None
            if condition['ingestion']:
                other = c.call('POST', '/v0/corpora', {'name': 'Concurrent ingestion', 'idempotency_key': 'measure-distractors'}, 201)['corpus_id']
                distractors = Distractors(c, other, workload['concurrent_ingestion'])
                distractors.start()
                time.sleep(2)  # the ingestion stream is running before the first measured request
            started = time.monotonic()
            try:
                samples = run_latency(c, corpus, plan, rows, condition['concurrency'], cfg)
            finally:
                if distractors:
                    distractors.stop.set()
                    distractors.join()
            cell = {mode: m.latency_summary(samples[mode], target) for mode in cfg['modes']}
            cell['_condition'] = {**condition, 'wall_seconds': round(time.monotonic() - started, 3)}
            if distractors:
                cell['_condition'].update(distractor_records_accepted=distractors.accepted, distractor_errors=distractors.errors[:5])
            latency[condition['name']] = cell
    finally:
        sampler.stop.set()
        sampler.join()
        report['resources'] = {'sampling_interval_seconds': 2, 'samples': sampler.samples, 'peaks': sampler.peaks, 'sampling_errors': sampler.errors[:5], 'note': 'containers: docker stats instantaneous CPU and memory; quivr-api/worker: ps RSS and lifetime-average CPU'}

    sem, hyb = quality['semantic'], quality['hybrid']
    primary = {mode: latency['sequential'][mode]['target_met'] for mode in cfg['modes']}
    report['findings'] = {
        'hybrid_deficit': {'reproduced': hyb['mrr_at_10'] < sem['mrr_at_10'], 'semantic_mrr_at_10': round(sem['mrr_at_10'], 4), 'hybrid_mrr_at_10': round(hyb['mrr_at_10'], 4),
                           'semantic_recall_at_3': round(sem['recall_at_3'], 4), 'hybrid_recall_at_3': round(hyb['recall_at_3'], 4), 'prior': PRIOR, 'tracked_by': THE_641,
                           'note': 'measurement only; weights and fixture unchanged, relevance investigation belongs to THE-641'},
        'p95_target_simple_search': {'target_ms_below': target, 'condition': 'sequential', 'met_by_mode': primary, 'all_met': all(primary.values())},
        'p95_target_other_conditions': {name: {mode: latency[name][mode]['target_met'] for mode in cfg['modes']} for name in latency if name != 'sequential'},
    }


def markdown(r):
    lines = [f"# Text retrieval baseline — {r['workload']}", '', f"Status: **{r['status']}** · source `{r['pins']['source_revision']}`{' (dirty)' if r['pins']['source_dirty'] else ''} · {r['finished_at']}", '']
    if r['status'] != 'completed':
        partial = [k for k in ['host', 'phases_seconds', 'embedding_preparation', 'quality', 'retrieval_profile', 'warmup_failures', 'latency', 'resources'] if r.get(k)]
        return '\n'.join(lines + ['Error: `' + r.get('error', '') + '`', '', 'Partial results are kept in measurement.json: ' + (', '.join(partial) or 'none'), ''])
    h = r['host']
    lines += [f"Host: {h.get('cpu_model')}, {h['logical_cpus']} logical CPUs, {h.get('memory_total')}, {h['machine']}; Docker {h['docker']}. Profile `{r['retrieval_profile']['version']}`.", '',
              '## Quality (24 CC0 FR/EN queries, public search, limit 10)', '', '| Mode | MRR@10 | Recall@3 | Recall@10 |', '| --- | --- | --- | --- |']
    for mode, q in r['quality'].items():
        lines.append(f"| {mode} | {q['mrr_at_10']:.4f} | {q['recall_at_3']:.4f} | {q['recall_at_10']:.4f} |")
    lines += ['', '## Latency (client wall time, ms; 120 samples per cell; target p95 < 1000 ms)', '', '| Condition | Mode | p50 | p95 | max | failures | target |', '| --- | --- | --- | --- | --- | --- | --- |']
    for name, cell in r['latency'].items():
        for mode, s in cell.items():
            if not mode.startswith('_'):
                lines.append(f"| {name} | {mode} | {s['p50_ms']} | {s['p95_ms']} | {s['max_ms']} | {s['failures']}/{s['samples']} | {'met' if s['target_met'] else 'MISSED'} |")
    lines += ['', '## Phases (seconds)', '', '| Phase | Seconds |', '| --- | --- |'] + [f'| {k} | {v} |' for k, v in r['phases_seconds'].items()]
    lines += ['', f"Model files downloaded this run: {r['embedding_preparation']['downloaded_bytes']} bytes.", '', '## Resource peaks during the latency workload', '', r['resources']['note'] + '.', '', '| Component | CPU % peak | Memory MiB peak |', '| --- | --- | --- |']
    lines += [f"| {k} | {v['cpu_percent_peak']} | {v['memory_mib_peak']} |" for k, v in sorted(r['resources']['peaks'].items())]
    d = r['findings']['hybrid_deficit']
    lines += ['', '## Findings', '', f"- Hybrid deficit {'reproduced' if d['reproduced'] else 'not reproduced'}: hybrid MRR@10 {d['hybrid_mrr_at_10']} vs semantic {d['semantic_mrr_at_10']} (prior {PRIOR['hybrid']['mrr_at_10']} vs {PRIOR['semantic']['mrr_at_10']}); tracked by [THE-641]({THE_641}).",
              f"- Simple-search p95 target (sequential): {r['findings']['p95_target_simple_search']['met_by_mode']}.", '', '## Limits', ''] + ['- ' + x for x in r['limits']]
    return '\n'.join(lines) + '\n'


def main():
    if platform.system() != 'Linux' or platform.machine() != 'x86_64':
        sys.exit(f'measure: unsupported platform {platform.system()}/{platform.machine()}; the pinned E5/tokenizer harness requires Linux x86_64')
    workload, rows = m.load_workload(WORKLOAD, ROOT)
    from local import Stack

    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)  # a cancelled run still writes its report and cleans up
    stack = Stack('quivr-measure-' + uuid.uuid4().hex[:10])
    started = time.monotonic()
    report = {'status': 'failed', 'workload': workload['workload'], 'limits': workload['limits']}
    try:
        measure(stack, workload, rows, report)
        report['status'] = 'completed'
    except BaseException as error:
        report['error'] = type(error).__name__ + ': ' + str(error)[:500]
        raise
    finally:
        # Write the report before cleanup: cancellation may interrupt log capture or teardown.
        report.setdefault('pins', pins())
        report['duration_seconds'] = round(time.monotonic() - started, 3)
        report['finished_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds')
        (stack.directory / 'measurement.json').write_text(json.dumps(report, indent=2, ensure_ascii=False))
        (stack.directory / 'measurement.md').write_text(markdown(report))
        print('Measurement artifacts:', stack.directory, flush=True)
        try:
            stack.capture()
        finally:
            stack.down(True)


if __name__ == '__main__':
    main()
