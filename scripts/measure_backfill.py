#!/usr/bin/env python3
"""make measure-backfill: live ingestion freshness while a backfill runs (THE-784).

Runs an isolated real stack (Linux x86_64 only, like the rest of the harness)
and pins the Go SDK sample ingestion plugin (sdks/go/examples/hash-embedder)
with its small space served alone. A Corpus ingests PAST articles. Then, twice,
LIVE Records are ingested one at a time and each one's accepted-to-searchable
time is read from the API: first alone, then while a backfill fills the large
space of the PAST articles at RATE Versions per second. The report,
backfill-measurement.json and .md, gives both freshness distributions, the
backfill's throughput, and whether freshness stayed within the target. A
missed target is a reported finding; only harness or dependency errors exit
nonzero.

The sample plugin embeds by hashing words, so the measurement isolates what a
backfill costs the engine (PostgreSQL, Weaviate, Temporal, worker slots); a
model's own compute adds to the plugin's latency, which the rate bounds.
"""
import datetime
import json
import os
import platform
import signal
import statistics
import sys
import time
import urllib.error
import urllib.request
import uuid

import ingestion_plugin as sample
import ports

PAST, LIVE, RATE = int(os.environ.get('QUIVR_MEASURE_PAST', '300')), int(os.environ.get('QUIVR_MEASURE_LIVE', '30')), float(os.environ.get('QUIVR_MEASURE_RATE', '20'))
# Freshness during a backfill stays within target when its p95 is at most
# twice the p95 alone, or at most one second more.
TARGET = 'p95 during <= max(2 x p95 alone, p95 alone + 1 s)'


def call(base, token, method, path, body=None, expected=200):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method, headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        result = json.load(response)
        if response.status != expected:
            raise RuntimeError(f'{method} {path} returned {response.status}: {result}')
        return result


def searchable(base, token, corpus, key, text, deadline=120):
    """Ingest one Record and return seconds from acceptance to searchable."""
    command = {'idempotency_key': key, 'source': {'corpus_id': corpus, 'namespace': 'measure', 'record_key': key}, 'content': {'kind': 'text', 'text': text}}
    started = time.monotonic()
    receipt = call(base, token, 'POST', '/v0/records', command, 202)['receipt_id']
    while True:
        r = call(base, token, 'GET', '/v0/ingestion-receipts/' + receipt)
        if (r.get('availability') or {}).get('searchable'):
            return time.monotonic() - started
        if time.monotonic() - started > deadline:
            raise RuntimeError(f'Record {key} never became searchable: {r}')
        time.sleep(.05)


def distribution(seconds):
    ordered = sorted(seconds)
    return {'count': len(ordered), 'p50': round(statistics.median(ordered), 3), 'p95': round(ordered[max(0, int(len(ordered) * .95) - 1)], 3), 'max': round(ordered[-1], 3)}


def live(base, token, corpus, phase):
    return distribution([searchable(base, token, corpus, f'{phase}-{i}', f'Live report {i} of the {phase} phase on the harbour tide.') for i in range(LIVE)])


def measure(stack, report):
    stack.up()
    s = stack.state
    base, admin = f"http://127.0.0.1:{s['api_port']}", s['admin']
    directory = stack.directory / 'measure-backfill'
    directory.mkdir(exist_ok=True)
    binary = directory / 'hash-embedder'
    sample.subprocess.run([sample.GO, 'build', '-o', str(binary), './examples/hash-embedder'], cwd=sample.ROOT / 'sdks' / 'go', check=True)
    port = ports.allocate()
    plugin = sample.start_plugin(directory, binary, port, sample.SAMPLE / 'quivr-plugin.yaml', 'plugin.log')
    try:
        sample.await_healthy(plugin, port, directory / 'plugin.log')
        for name in ['config.json', 'worker.json']:
            cfg = json.loads((stack.directory / name).read_text())
            cfg['plugins'] = [p for p in cfg.get('plugins', []) if not p['manifest'].endswith('/core-ingest/quivr-plugin.yaml')] + [
                {'manifest': str(sample.SAMPLE / 'quivr-plugin.yaml'), 'endpoint': f'http://127.0.0.1:{port}', 'spaces': {'example.hash_embedder.small': 'served'}}]
            cfg['backfill'] = {'rate': RATE, 'poll': '200ms', 'max_cost_without_confirmation': 1000}
            (stack.directory / name).write_text(json.dumps(cfg))
        stack.stop_processes()
        stack.start_processes()
        corpus = call(base, admin, 'POST', '/v0/corpora', {'name': 'Backfill measurement', 'idempotency_key': 'measure-backfill'}, 201)['corpus_id']
        started = time.monotonic()
        for i in range(PAST):
            searchable(base, admin, corpus, f'past-{i}', f'Archive article {i}: the port authority reviewed berth {i % 17} after the storm.')
        report['past_ingestion_seconds'] = round(time.monotonic() - started, 3)
        report['alone'] = live(base, admin, corpus, 'alone')

        # The large space enters as an evaluation space: the Corpus predates it.
        manifest = (sample.SAMPLE / 'quivr-plugin.yaml').read_text()
        operator = s['backfiller']
        registered = call(base, operator, 'POST', '/v0/admin/plugins', {'idempotency_key': 'measure-both', 'endpoint': f'http://127.0.0.1:{port}', 'manifest': manifest, 'spaces': sample.SPACES}, 202)
        location = '/v0/admin/plugins/' + registered['registration_id']
        deadline = time.monotonic() + 60
        while call(base, operator, 'GET', location)['state'] == 'registered':
            if time.monotonic() > deadline:
                raise RuntimeError('the registration check never settled')
            time.sleep(.1)
        call(base, operator, 'POST', location + '/activate', {})
        body = {'idempotency_key': 'measure-backfill', 'corpus_id': corpus, 'dry_run': True}
        report['estimate'] = call(base, operator, 'POST', '/v0/admin/backfills', body)
        body.update(dry_run=False, confirm_cost=True)
        op = call(base, operator, 'POST', '/v0/admin/backfills', body, 202)
        started = time.monotonic()
        report['during'] = live(base, admin, corpus, 'during')
        report['backfill_running_after_live'] = call(base, operator, 'GET', '/v0/operations/' + op['operation_id'])['state'] == 'running'
        while (done := call(base, operator, 'GET', '/v0/operations/' + op['operation_id']))['state'] in ('queued', 'running'):
            if time.monotonic() - started > 3600:
                raise RuntimeError('the backfill never finished')
            time.sleep(.2)
        elapsed = time.monotonic() - started
        report['backfill'] = {'state': done['state'], 'counters': done['counters'], 'seconds': round(elapsed, 3), 'versions_per_second': round(done['counters'].get('versions_done', 0) / elapsed, 3)}
        alone, during = report['alone']['p95'], report['during']['p95']
        report['within_target'] = during <= max(2 * alone, alone + 1)
    finally:
        sample.stop_plugin(plugin)


def markdown(r):
    lines = ['# Live ingestion freshness during a backfill (THE-784)', '',
             f"Status: {r['status']}. {r.get('past', PAST)} past articles, {r.get('live', LIVE)} live Records per phase, backfill rate {r.get('rate', RATE)} Versions/s.", '',
             '| Phase | Records | p50 s | p95 s | max s |', '| --- | --- | --- | --- | --- |']
    for phase in ['alone', 'during']:
        d = r.get(phase)
        if d:
            lines.append(f"| {phase} | {d['count']} | {d['p50']} | {d['p95']} | {d['max']} |")
    if 'backfill' in r:
        b = r['backfill']
        lines += ['', f"Backfill: {b['state']}, {b['counters'].get('versions_done', 0)} Versions in {b['seconds']} s ({b['versions_per_second']} Versions/s)."]
        lines.append(f"Still running when the last live Record was searchable: {r.get('backfill_running_after_live')}.")
    if 'within_target' in r:
        lines += ['', f"Target ({TARGET}): {'met' if r['within_target'] else 'MISSED'}."]
    if 'error' in r:
        lines += ['', 'Error: ' + r['error']]
    return '\n'.join(lines) + '\n'


def main():
    if platform.system() != 'Linux' or platform.machine() != 'x86_64':
        sys.exit(f'measure-backfill: unsupported platform {platform.system()}/{platform.machine()}; the stack requires Linux x86_64')
    from local import Stack

    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    stack = Stack('quivr-measure-backfill-' + uuid.uuid4().hex[:10])
    report = {'status': 'failed', 'past': PAST, 'live': LIVE, 'rate': RATE, 'target': TARGET}
    started = time.monotonic()
    try:
        measure(stack, report)
        report['status'] = 'completed'
    except BaseException as error:
        report['error'] = type(error).__name__ + ': ' + str(error)[:500]
        raise
    finally:
        report['duration_seconds'] = round(time.monotonic() - started, 3)
        report['finished_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds')
        (stack.directory / 'backfill-measurement.json').write_text(json.dumps(report, indent=2))
        (stack.directory / 'backfill-measurement.md').write_text(markdown(report))
        print('Measurement artifacts:', stack.directory, flush=True)
        try:
            stack.capture()
        finally:
            stack.down(True)


if __name__ == '__main__':
    main()
