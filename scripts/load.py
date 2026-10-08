#!/usr/bin/env python3
"""Local-only synthetic load measurement: make load [args='--scenario <file> --out <dir>']."""
import argparse
import concurrent.futures
import datetime
import hashlib
import http.server
import json
import os
import pathlib
import platform
import queue
import random
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

from load_report import distribution, markdown, request_summary
from load_scenarios import read

ROOT = pathlib.Path(__file__).resolve().parents[1]
SCENARIOS = ROOT / 'tests/load/scenarios'
TIMEOUT = 5
POLL = .1
PROBE_HTTP_PER_SECOND = 10


def call(base, token, method, path, body=None):
    started = time.monotonic()
    request = urllib.request.Request(base + path, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
    try:
        try:
            response = urllib.request.urlopen(request, timeout=TIMEOUT)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            status = response.status
        result = json.loads(raw) if raw else {}
        error = result.get('code', '') if status >= 400 else ''
    except (OSError, ValueError) as failure:
        status, result, error = None, {}, type(failure).__name__
    return {'ms': (time.monotonic() - started)*1000, 'status': status, 'error': error}, result


class Receiver:
    """Deduplicate received notices by subscription/version, preserving first arrival."""
    def __init__(self, port):
        self.lock = threading.Lock()
        self.notices = {}
        # Every acknowledged POST, so redelivered duplicates stay visible.
        self.posts = 0
        receiver = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                try:
                    notice = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
                    ref = notice['references']
                    key = (ref['record_version_id'], ref['subscription_id'])
                    with receiver.lock:
                        receiver.posts += 1
                        receiver.notices.setdefault(key, time.monotonic())
                    self.send_response(204)
                except (ValueError, KeyError):
                    self.send_response(400)
                self.end_headers()
            def log_message(self, *_):
                pass
        class Server(http.server.ThreadingHTTPServer):
            # Keep the local receiver ahead of bounded webhook fan-out. A
            # small listen queue adds one-second TCP retransmission stalls
            # to otherwise immediate fake acknowledgements.
            request_queue_size = 128
        self.server = Server(('127.0.0.1', port), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def arrivals(self, version):
        with self.lock:
            return [at for (v, _), at in self.notices.items() if v == version]

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()


def arrival_offsets(s):
    """Open-loop arrivals; burst transitions do not depend on server response time."""
    burst = s['ingestion']['burst']
    duration = s['duration_seconds']
    boundaries = sorted({0, burst['at_seconds'],
                         burst['at_seconds'] + burst['duration_seconds'], duration})
    rate = s['ingestion']['per_second']
    for start, end in zip(boundaries, boundaries[1:]):
        per_second = rate * (burst['multiplier'] if
            burst['at_seconds'] <= start < burst['at_seconds'] + burst['duration_seconds'] else 1)
        if per_second == 0:
            continue
        index = 0
        while start + index/per_second < end:
            yield start + index/per_second
            index += 1


class Workload:
    def __init__(self, stack, scenario, receiver):
        self.stack, self.s, self.receiver = stack, scenario, receiver
        self.token = stack.state['admin']
        self.corpus = None
        self.records = []
        self.requests = []
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.observe_stop = threading.Event()
        self.user_counter = 0
        self.users = set()
        self.dropped = 0
        self.scheduled = 0
        self.faults = []
        self.driver_errors = []
        self.started = None
        self.probe_next = 0
        self.probe_throttled = False
        self.probe_requests = []

    def guarded(self, function, *args):
        try:
            function(*args)
        except Exception as error:
            with self.lock:
                self.driver_errors.append({'task': function.__name__, 'error': type(error).__name__})
            self.stop.set()

    def endpoint(self, index=0):
        endpoints = self.stack.endpoints()
        if not endpoints:
            raise RuntimeError('no API replica remains alive')
        return endpoints[index % len(endpoints)]

    def expect(self, method, path, body=None, expected=200):
        row, result = call(self.endpoint(), self.token, method, path, body)
        if row['status'] != expected:
            raise RuntimeError(f'{method} {path}: status {row["status"]}, code {row["error"]}')
        return result

    def submit(self, index, arrival, measured):
        marker = f'zqload{index:09d}'
        rng = random.Random(self.s['seed'] + index)
        vocabulary = ['harbour', 'ferry', 'weather', 'transport', 'science', 'energy']
        text = ' '.join(['harbour', marker] + rng.choices(vocabulary,
                        k=self.s['corpus']['words_per_record'] - 2))
        record = {'marker': marker, 'arrival': arrival, 'measured': measured}
        with self.lock:
            self.records.append(record)
        at = time.monotonic()-self.started if measured else None
        row, result = call(self.endpoint(index), self.token, 'POST', '/v0/records', {
            'idempotency_key': marker, 'source': {'corpus_id': self.corpus,
            'namespace': 'local-load', 'record_key': marker}, 'content': {'kind': 'text', 'text': text}})
        with self.lock:
            if measured:
                self.requests.append({'operation': 'ingestion', 'at_seconds': at, **row})
            record['status'] = row['status']
            if row['status'] == 202:
                record['receipt'] = result['receipt_id']

    def observe(self):
        # Probe requests are deliberately separate from measured load requests.
        # Lag is an upper bound at this polling cadence, including probe backlog.
        while not self.observe_stop.is_set():
            with self.lock:
                pending = [r for r in self.records if r.get('receipt') and
                           not (r.get('searchable') and r.get('alerted'))]
            for record in pending:
                if self.observe_stop.is_set():
                    return
                if not record.get('version'):
                    row, receipt = self.probe('GET',
                                         '/v0/ingestion-receipts/' + record['receipt'])
                    if row['status'] == 200 and receipt.get('version_id'):
                        record.update(version=receipt['version_id'], record_id=receipt['record_id'])
                if not record.get('searchable'):
                    row, found = self.probe('POST', '/v0/search', {
                        'query': record['marker'], 'corpus_ids': [self.corpus],
                        'mode': 'hybrid', 'limit': 10})
                    if row['status'] == 200 and any(hit['record_id'] == record.get('record_id') and
                        hit['version_id'] == record.get('version') and hit.get('embedding_artifact_id')
                        for hit in found.get('items', [])):
                        record['searchable'] = time.monotonic()
                if not self.s['alerts']:
                    record['alerted'] = True
                elif record.get('version'):
                    notices = self.receiver.arrivals(record['version'])
                    record['notice_count'] = len(notices)
                    if notices:
                        record['first_alert'] = min(notices)
                    if len(notices) == self.s['alerts']:
                        record['alerted'] = max(notices)
            self.observe_stop.wait(POLL)

    def probe(self, method, path, body=None):
        # One observer shares a global budget across both receipt and search
        # probes, and rotates over live replicas instead of loading only API0.
        delay = max(0, self.probe_next-time.monotonic()) if self.probe_throttled else 0
        if self.observe_stop.wait(delay):
            return {'status': None}, {}
        started = time.monotonic()
        if self.probe_throttled:
            self.probe_next = started + 1/PROBE_HTTP_PER_SECOND
        at = started-self.started if self.started is not None else None
        row, result = call(self.endpoint(len(self.probe_requests)), self.token, method, path, body)
        self.probe_requests.append({'at_seconds': at, **row})
        return row, result

    def caught_up(self):
        with self.lock:
            return all(r.get('status') is not None and (r['status'] != 202 or
                       (r.get('searchable') and r.get('alerted'))) for r in self.records)

    def drain(self):
        deadline = time.monotonic() + self.s['drain_seconds']
        while not self.caught_up() and time.monotonic() < deadline:
            self.stop.wait(POLL) if not self.stop.is_set() else time.sleep(POLL)
        return self.caught_up()

    def setup(self):
        self.corpus = self.expect('POST', '/v0/corpora', {
            'name': 'Local load', 'idempotency_key': 'load-corpus'}, 201)['corpus_id']
        for index in range(self.s['alerts']):
            saved = self.expect('POST', '/v0/saved-queries', {
                'name': f'Local alert {index}', 'idempotency_key': f'query-{index}',
                'definition': {'corpus_ids': [self.corpus], 'expression': {'term': 'harbour', 'alert': index},
                'retrieval_profile': 'default', 'temporal_policy': 'from_activation'}}, 201)
            self.expect('POST', '/v0/subscriptions', {'idempotency_key': f'subscription-{index}',
                'name': f'Local alert {index}', 'saved_query_id': saved['saved_query_id'],
                'saved_query_version_id': saved['current_version']['version_id'],
                'evaluator': {'plugin_id': 'load.fake', 'version': '0.1.0', 'configuration': {}},
                'destination_id': 'load-receiver', 'owner': f'user-{index % self.s["search"]["users"]}'}, 201)
        self.observer = threading.Thread(target=self.guarded, args=(self.observe,), daemon=True)
        self.observer.start()
        with concurrent.futures.ThreadPoolExecutor(max_workers=self.s['ingestion']['concurrency']) as pool:
            # Bound outstanding setup work even for a large corpus.
            window = 4*self.s['ingestion']['concurrency']
            for start in range(0, self.s['corpus']['records'], window):
                for _ in pool.map(lambda i: self.submit(i, time.monotonic(), False),
                                  range(start, min(start+window, self.s['corpus']['records']))):
                    pass
        if not self.drain() or any(r['status'] != 202 for r in self.records):
            raise RuntimeError('initial corpus did not become searchable and fully alerted within drain_seconds')
        # Each requested mode must work before timing it.
        for mode in self.s['search']['mix']:
            if self.s['search']['mix'][mode] > 0:
                result = self.expect('POST', '/v0/search', self.search_body(mode))
                if not result.get('items'):
                    raise RuntimeError(f'{mode} warmup returned no content')

    def search_body(self, mode):
        return {'query': 'harbour ferry', 'corpus_ids': [self.corpus],
                'mode': 'hybrid' if mode == 'deep' else mode,
                'profile': 'deep' if mode == 'deep' else 'default', 'limit': 10}

    def search(self, worker, end):
        rng = random.Random(self.s['seed'] + worker)
        modes, weights = zip(*self.s['search']['mix'].items())
        while time.monotonic() < end and not self.stop.is_set():
            with self.lock:
                user = self.user_counter % self.s['search']['users']
                self.user_counter += 1
                self.users.add(user)
            mode = rng.choices(modes, weights)[0]
            at = time.monotonic()-self.started
            row, _ = call(self.endpoint(worker), self.token, 'POST', '/v0/search', self.search_body(mode))
            with self.lock:
                self.requests.append({'operation': mode, 'at_seconds': at, **row})

    def execute(self):
        if 'anchor_records' in self.s['ingestion']:
            return self.anchored()
        self.setup()
        started = time.monotonic()
        self.started = started
        self.probe_throttled = True
        end = started + self.s['duration_seconds']
        pending = queue.Queue(maxsize=2*self.s['ingestion']['concurrency'])
        def ingest():
            while True:
                job = pending.get()
                try:
                    if job is None:
                        return
                    self.guarded(self.submit, *job, True)
                finally:
                    pending.task_done()
        def produce():
            for index, offset in enumerate(arrival_offsets(self.s), self.s['corpus']['records']):
                if self.stop.wait(max(0, started + offset - time.monotonic())):
                    return
                self.scheduled += 1
                try:
                    pending.put_nowait((index, started + offset))
                except queue.Full:
                    self.dropped += 1
        def fault():
            at = self.s['replicas'].get('kill_at_seconds')
            if at is not None and not self.stop.wait(max(0, started + at - time.monotonic())):
                killed = self.stack.kill_replica()
                self.faults.append({'at_seconds': round(time.monotonic()-started, 3), 'killed': killed})
        threads = [threading.Thread(target=self.guarded, args=(ingest,))
                   for _ in range(self.s['ingestion']['concurrency'])]
        searches = [threading.Thread(target=self.guarded, args=(self.search, i, end))
                    for i in range(self.s['search']['concurrency'])]
        controls = [threading.Thread(target=self.guarded, args=(fn,)) for fn in (produce, fault)]
        try:
            for thread in threads + searches + controls:
                thread.start()
            for thread in controls + searches:
                thread.join()
            pending.join()
            elapsed = time.monotonic()-started
            self.probe_throttled = False
            complete = self.drain()
        finally:
            self.probe_throttled = False
            self.stop.set()
            for thread in controls + searches:
                if thread.ident:
                    thread.join()
            for thread in threads:
                pending.put(None)
            for thread in threads:
                if thread.ident:
                    thread.join()
        self.close()
        operations = ['ingestion', *self.s['search']['mix']]
        summaries = {op: request_summary([r for r in self.requests if r['operation'] == op], elapsed,
                                         202 if op == 'ingestion' else 200) for op in operations}
        measured = [r for r in self.records if r['measured']]
        accepted = [r for r in measured if r.get('status') == 202]
        missing_search = sum(not r.get('searchable') for r in accepted)
        missing_alerts = sum(self.s['alerts']-r.get('notice_count', 0) for r in accepted)
        lag = {'arrival_to_searchable': distribution([(r['searchable']-r['arrival'])*1000
                for r in accepted if r.get('searchable')]),
               'arrival_to_first_alert': distribution([(r['first_alert']-r['arrival'])*1000
                for r in accepted if r.get('first_alert')]),
               'arrival_to_all_alerts': distribution([(r['alerted']-r['arrival'])*1000
                for r in accepted if self.s['alerts'] and r.get('alerted')])}
        errors = sum(row['errors'] for row in summaries.values())
        fault_windows = {}
        if self.faults:
            cut = self.faults[0]['at_seconds']
            for label, before, seconds in [('before_kill', True, cut),
                                           ('after_kill', False, elapsed-cut)]:
                fault_windows[label] = {op: request_summary([r for r in self.requests if
                    r['operation'] == op and (r['at_seconds'] < cut) == before], seconds,
                    202 if op == 'ingestion' else 200) for op in operations}
        return {'status': 'complete' if complete and not self.dropped and not errors
                and not self.driver_errors and all(summaries[op]['attempts'] for op in
                self.s['search']['mix'] if self.s['search']['mix'][op]) else 'failed',
                'elapsed_seconds': round(elapsed, 3), 'users_exercised': len(self.users),
                'requests': summaries, 'lag': lag, 'faults': self.faults, 'fault_windows': fault_windows,
                'probes': {'timed_http_per_second_limit': PROBE_HTTP_PER_SECOND,
                    'total_attempts': len(self.probe_requests), 'timed_window': request_summary([
                        row for row in self.probe_requests if row['at_seconds'] is not None and
                        0 <= row['at_seconds'] < elapsed], elapsed, 200)},
                'driver_errors': self.driver_errors,
                'work': {'scheduled': self.scheduled, 'driver_dropped': self.dropped,
                         'submitted': len(measured), 'accepted': len(accepted),
                         'missing_searchable': missing_search, 'expected_alerts': len(accepted)*self.s['alerts'],
                         'missing_alerts': missing_alerts}}

    def anchored(self):
        """All anchor_records documents arrive at one instant after warmup.

        Stage times come from the durable Version step columns, written by the
        transaction that commits each step, on PostgreSQL's clock. Search probes
        stop for the burst; every 0.5 s the drain reads receipts until each
        Version is known, and the step columns. Public hybrid search verifies
        every document afterwards. Alerts are first arrivals at the local receiver.
        """
        from load_stack import STEPS
        self.setup()
        self.observe_stop.set()
        self.observer.join(timeout=2*TIMEOUT+1)
        count, first = self.s['ingestion']['anchor_records'], self.s['corpus']['records']
        pending = queue.Queue()
        for index in range(first, first + count):
            pending.put(index)
        posts = self.receiver.posts
        # Step columns use the database clock, which a Docker VM may skew from ours.
        # The bound only ever overstates step times, by at most overstated_by.
        offset, overstated_by = self.stack.clock_offset()
        anchor = time.time() + offset
        self.started = started = time.monotonic()
        def client():
            while not self.stop.is_set():
                try:
                    index = pending.get_nowait()
                except queue.Empty:
                    return
                self.submit(index, started, True)
        clients = [threading.Thread(target=self.guarded, args=(client,))
                   for _ in range(self.s['ingestion']['concurrency'])]
        for thread in clients:
            thread.start()
        for thread in clients:
            thread.join()
        elapsed = time.monotonic() - started
        accepted = [r for r in self.records if r['measured'] and r.get('status') == 202]
        deadline = started + self.s['drain_seconds']
        durable = {}
        while time.monotonic() < deadline and not self.stop.is_set():
            for r in accepted:
                if time.monotonic() >= deadline:
                    break
                if not r.get('version'):
                    row, receipt = call(self.endpoint(), self.token, 'GET', '/v0/ingestion-receipts/' + r['receipt'])
                    if row['status'] == 200 and receipt.get('version_id'):
                        r.update(version=receipt['version_id'], record_id=receipt['record_id'])
            versions = [r['version'] for r in accepted if r.get('version')]
            durable = self.stack.step_times(versions)
            done = len(versions) == len(accepted) and all(
                durable.get(v, {}).get('enriched') and (not self.s['alerts'] or durable[v].get('evaluated'))
                for v in versions)
            if done and len(self.receiver.notices) >= len(self.records) * self.s['alerts']:
                break
            self.stop.wait(.5)
        with concurrent.futures.ThreadPoolExecutor(8) as pool:
            verified = sum(pool.map(self.verify, accepted))
        stages = {stage: distribution([(times[stage] - anchor)*1000 for times in durable.values() if times.get(stage)])
                  for stage in STEPS}
        alerted = [max(arrivals) for r in accepted if self.s['alerts'] and r.get('version') and
                   len(arrivals := self.receiver.arrivals(r['version'])) == self.s['alerts']]
        expected = len(accepted) * self.s['alerts']
        received = sum(len(self.receiver.arrivals(r['version'])) for r in accepted if r.get('version'))
        duplicates = self.receiver.posts - posts - received
        self.close()
        summary = request_summary([r for r in self.requests if r['operation'] == 'ingestion'], elapsed, 202)
        complete = (len(accepted) == count and verified == count and received == expected
                    and duplicates == 0 and not self.driver_errors and summary['errors'] == 0)
        return {'status': 'complete' if complete else 'failed', 'elapsed_seconds': round(elapsed, 3),
                'requests': {'ingestion': summary}, 'driver_errors': self.driver_errors,
                'anchored': {'documents': count, 'accepted': len(accepted), 'verified_hybrid': verified,
                             'database_clock_offset_seconds': round(offset, 3),
                             'steps_overstated_by_at_most_seconds': round(overstated_by, 3),
                             'stages_from_anchor': stages,
                             'all_alerts_from_anchor': distribution([(at - started)*1000 for at in alerted]),
                             'expected_alerts': expected, 'received_alerts': received,
                             'duplicate_alerts': duplicates}}

    def verify(self, record):
        row, found = call(self.endpoint(), self.token, 'POST', '/v0/search', {
            'query': record['marker'], 'corpus_ids': [self.corpus], 'mode': 'hybrid', 'limit': 10})
        return row['status'] == 200 and any(hit['record_id'] == record.get('record_id') and
            hit['version_id'] == record.get('version') and hit.get('embedding_artifact_id')
            for hit in found.get('items', []))

    def close(self):
        self.stop.set()
        self.observe_stop.set()
        if hasattr(self, 'observer'):
            self.observer.join(timeout=2*TIMEOUT+1)
            if self.observer.is_alive():
                raise RuntimeError('load observer failed to stop')


def machine():
    memory = os.sysconf('SC_PHYS_PAGES') * os.sysconf('SC_PAGE_SIZE')
    return {'system': platform.system(), 'architecture': platform.machine(),
            'kernel': platform.release(), 'cpus': os.cpu_count(), 'memory_bytes': memory}


def output(command):
    return subprocess.check_output(command, text=True, cwd=ROOT).strip()


def run_scenario(s):
    from load_stack import LoadStack
    run = {'scenario': s, 'scenario_sha256': hashlib.sha256(
        json.dumps(s, sort_keys=True).encode()).hexdigest(), 'status': 'failed'}
    stack = LoadStack(s)
    receiver, workload = None, None
    try:
        receiver = Receiver(stack.state['receiver_port'])
        stack.up()
        workload = Workload(stack, s, receiver)
        run.update(workload.execute())
    except Exception as error:
        run['error'] = str(error)
        print(f"{s['name']}: failed; inspect {stack.directory}", file=sys.stderr)
    finally:
        previous = {sig: signal.signal(sig, signal.SIG_IGN) for sig in (signal.SIGINT, signal.SIGTERM)}
        try:
            if workload:
                workload.close()
        except Exception as error:
            run.update(status='failed', observer_cleanup_error=type(error).__name__)
        try:
            stack.down()
            run['cleanup_verified'] = not stack.compose('ps', '--all', '-q',
                capture_output=True, text=True, timeout=10).stdout.strip()
            if not run['cleanup_verified']:
                run['status'] = 'failed'
        except Exception as error:
            run.update(status='failed', cleanup_verified=False, cleanup_error=type(error).__name__)
        finally:
            if receiver:
                receiver.close()
            for sig, handler in previous.items():
                signal.signal(sig, handler)
    return run


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--scenario', type=pathlib.Path, action='append',
                        help='repeat to select YAML files; default: every versioned scenario')
    parser.add_argument('--out', type=pathlib.Path,
                        default=ROOT / '.scratch/load' / datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
    parser.add_argument('--validate', action='store_true', help='validate selected YAML offline, without a stack')
    args = parser.parse_args(argv)
    selected = args.scenario or sorted(SCENARIOS.glob('*.yaml'))
    if not selected:
        parser.error('no scenarios selected')
    scenarios = [read(path) for path in selected]
    if args.validate:
        print(json.dumps(scenarios, indent=2))
        return 0
    if any(os.environ.get(k, '').lower() not in ('', '0', 'false') for k in ('CI', 'GITHUB_ACTIONS')):
        parser.error('load measurements run locally only, never in CI')
    from load_stack import local_docker_host
    try:
        docker_host = local_docker_host()
    except ValueError as error:
        parser.error(str(error))
    report = {'report_version': 1, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'status': 'failed', 'machine': machine(), 'revision': output(['git', 'rev-parse', 'HEAD']),
              'dirty': bool(output(['git', 'status', '--porcelain'])), 'runs': []}
    import yaml
    report['versions'] = {'python': platform.python_version(), 'go': output([os.environ.get('GO', 'go'), 'version']),
        'docker': output(['docker', '--host', docker_host, 'version', '--format', '{{.Server.Version}}']),
        'services': {k: v['image'] for k, v in yaml.safe_load((ROOT / 'deploy/compose/compose.yaml').read_text())['services'].items()
                     if k != 'tei'}, 'plugin': 'load.fake@0.1.0',
        'plugin_sha256': hashlib.sha256((ROOT / 'tests/fakes/load-plugin/main.go').read_bytes()).hexdigest()}
    report['source_sha256'] = {str(path.relative_to(ROOT)): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in [ROOT / 'scripts' / name for name in
        ('load.py', 'load_stack.py', 'load_scenarios.py', 'load_report.py', 'local.py')]
        + list((ROOT / 'tests/fakes/load-plugin').glob('*')) if path.is_file()}
    args.out.mkdir(parents=True, exist_ok=True)
    def cancelled(*_):
        raise InterruptedError('load measurement cancelled')
    previous = {sig: signal.signal(sig, cancelled) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        for s in scenarios:
            print(f"Running {s['name']} ({s['search']['concurrency']} search workers)", flush=True)
            report['runs'].append(run_scenario(s))
            report['status'] = 'complete' if all(r['status'] == 'complete' for r in report['runs']) else 'failed'
            (args.out / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
            (args.out / 'report.md').write_text(markdown(report))
            if report['runs'][-1].get('error') == 'load measurement cancelled':
                break
    finally:
        (args.out / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
        (args.out / 'report.md').write_text(markdown(report))
        for sig, handler in previous.items():
            signal.signal(sig, handler)
    print(f"{report['status']}: {args.out / 'report.md'}", flush=True)
    return 0 if report['status'] == 'complete' else 1


if __name__ == '__main__':
    raise SystemExit(main())
