#!/usr/bin/env python3
"""Measure synthetic archive throughput on an existing local demo/dev stack."""
import argparse
import json
import pathlib
import re
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from archive_source import seed
from local import Stack


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stack', required=True, help='Running local stack name under .scratch/')
    parser.add_argument('--items', type=int, default=1000, help='Synthetic unique filler members, plus three revisions')
    parser.add_argument('--batch-size', type=int, default=100)
    parser.add_argument('--concurrency', type=int, default=8)
    parser.add_argument('--timeout', type=int, default=600)
    args = parser.parse_args()
    if not re.fullmatch(r'[a-z0-9-]+', args.stack) or args.items < 1 or not 1 <= args.batch_size <= 1000 or not 1 <= args.concurrency <= 32:
        parser.error('invalid stack, item count, batch size or concurrency')
    directory = pathlib.Path(__file__).resolve().parents[1] / '.scratch' / args.stack
    if not (directory / 'config.json').exists():
        parser.error('start the named local stack first')
    stack = Stack(args.stack)
    run = uuid.uuid4().hex[:12]
    # Fresh bytes prevent repeated runs from measuring verified-Blob cache hits.
    private = seed(stack, bucket='synthetic-archive-' + run, count=args.items, marker=run)
    fixture = json.loads(private.read_text())
    token = stack.state['demo']
    base = f"http://127.0.0.1:{stack.state['api_port']}"

    def api(method, path, body=None):
        req = urllib.request.Request(base + path, method=method,
            data=None if body is None else json.dumps(body).encode(),
            headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f'Archive measurement API {method} failed with HTTP {exc.code}') from None
        except Exception:
            raise RuntimeError(f'Archive measurement API {method} failed') from None

    corpus = api('POST', '/v0/corpora', {'name': 'Synthetic archive measurement', 'idempotency_key': 'archive-corpus-' + run})['corpus_id']
    path = '/v0/changes?' + urllib.parse.urlencode({'corpus_id': corpus, 'limit': 100})
    cursor = api('GET', path)['next_cursor']
    config = {**fixture['config'], 'batch_size': args.batch_size, 'concurrency': args.concurrency}
    started = time.monotonic()
    created = api('POST', '/v0/connectors', {'idempotency_key': 'archive-measure-' + run,
        'corpus_id': corpus, 'source_namespace': 'synthetic-archive', 'kind': 'object_storage_archive',
        'config': config, 'schedule': {'interval_seconds': 1}, 'credential': {'secret': fixture['credential']}})
    connector = created['connector_id']
    accepted_at = None
    ready_at = None
    seen = set()
    accepted_events = set()
    samples = []
    expected = fixture['members_total']
    health = {}
    try:
        while time.monotonic() - started < args.timeout:
            health = api('GET', '/v0/connectors/' + connector)['health']
            done = (health.get('diagnostics') or {}).get('members_done', 0)
            # Drain full pages so the observer does not cap measured throughput.
            while True:
                page = api('GET', path + '&' + urllib.parse.urlencode({'cursor': cursor}))
                for event in page['items']:
                    if event['type'] == 'record.accepted':
                        accepted_events.add(event['event_id'])
                    if event['type'] == 'record.retrieval_ready':
                        seen.add(event['resource']['id'])
                cursor = page['next_cursor']
                if not page['has_more']:
                    break
            elapsed = time.monotonic() - started
            if accepted_at is None and done == expected and len(accepted_events) == expected:
                accepted_at = elapsed
            samples.append({'seconds': round(elapsed, 3), 'accepted': len(accepted_events),
                            'checkpoint_members_done': done, 'searchable_records': len(seen)})
            if len(seen) >= expected - 1 and ready_at is None:
                ready_at = elapsed
            if health.get('last_error'):
                raise RuntimeError('Archive measurement source failure: ' + health['last_error']['code'])
            if accepted_at is not None and ready_at is not None:
                break
            time.sleep(0.2)
    finally:
        api('POST', '/v0/connectors/' + connector + '/disable', {'idempotency_key': 'archive-measure-stop-' + run})
    accepted_rate = expected / accepted_at if accepted_at else 0
    ready_rate = (expected - 1) / ready_at if ready_at else 0
    bottleneck = None
    if accepted_rate < 50:
        bottleneck = 'acquisition: source reading, Upload Session transfer/verification and durable submission; compare concurrency runs'
    elif ready_at is not None and accepted_at is not None and ready_at > accepted_at:
        bottleneck = 'processing after durable acceptance: normalization, segmentation and search publication'
    report = {'stack': args.stack, 'corpus_id': corpus, 'connector_id': connector,
        'fresh_source_bytes': True,
        'members': expected, 'unique_records': expected - 1, 'batch_size': args.batch_size, 'concurrency': args.concurrency,
        'accepted_seconds': accepted_at, 'searchable_seconds': ready_at,
        'accepted_members_per_second': round(accepted_rate, 2), 'searchable_records_per_second': round(ready_rate, 2),
        'target_accepted_members_per_second': 50, 'bottleneck': bottleneck, 'samples': samples,
        'status': 'complete' if accepted_at and ready_at else 'timed_out'}
    report['plugin_timings'] = api('GET', '/v0/admin/stats/plugins')['items']
    report['processing_timings'] = api('GET', '/v0/admin/stats/steps')['items']
    report['timings_window'] = '1h, Organization-wide; not isolated to this run'
    out = stack.directory / f'archive-measure-{run}.json'
    out.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({k: v for k, v in report.items()
                      if k not in ('samples', 'plugin_timings', 'processing_timings')}, indent=2))
    print('Evidence:', out)
    if report['status'] != 'complete':
        raise SystemExit(1)


if __name__ == '__main__':
    main()
