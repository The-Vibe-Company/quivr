"""Public-HTTP evidence that scheduled acquisition resumes after the worker dies.

A fixture Connector Instance consumes one script step per run. The worker (and
API) are killed after the first steps were acquired; after restart the instance
continues from its committed Acquisition Checkpoint to the end of the script,
every item becomes exactly one Record Version, and the deposited secret never
appears in any response or process log.
"""
import json, os, signal, time, urllib.error, urllib.parse, urllib.request, uuid

SECRET = 'fixture-test-secret-restart-not-real'


def verify(stack):
    s = stack.state
    base = f"http://127.0.0.1:{s['api_port']}"

    def call(method, path, body=None, status=200):
        req = urllib.request.Request(base + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                     headers={'Authorization': 'Bearer ' + s['connector'], 'Content-Type': 'application/json'})
        try:
            r = urllib.request.urlopen(req, timeout=8)
        except urllib.error.HTTPError as e:
            r = e
        with r:
            raw = r.read()
            result = json.loads(raw)
            (stack.directory / ('response-' + uuid.uuid4().hex + '.json')).write_text(json.dumps(dict(path=urllib.parse.urlparse(path).path, method=method, status=r.status, body=result)))
            assert r.status == status, (r.status, result)
            assert SECRET.encode() not in raw, 'secret leaked in a response'
            return result

    corpus = call('POST', '/v0/corpora', {'name': 'Connector restart', 'idempotency_key': 'connector-restart'}, 201)['corpus_id']
    cursor = call('GET', '/v0/changes?corpus_id=' + corpus)['next_cursor']
    keys = [f'item-{i}' for i in range(8)]
    script = [{'items': [{'record_key': k, 'text': f'Dépêche numéro {i}'}]} for i, k in enumerate(keys)]
    connector = call('POST', '/v0/connectors', {'idempotency_key': 'restart', 'corpus_id': corpus, 'source_namespace': 'restart', 'kind': 'fixture',
                                                'config': {'requires_credential': True, 'script': script}, 'schedule': {'interval_seconds': 2},
                                                'credential': {'secret': {'token': SECRET}}}, 201)['connector_id']
    materialized = {}

    def follow(until, deadline=120):
        nonlocal cursor
        end = time.monotonic() + deadline
        while True:
            while True:
                page = call('GET', '/v0/changes?' + urllib.parse.urlencode({'corpus_id': corpus, 'cursor': cursor}))
                for e in page['items']:
                    if e['type'] == 'record.materialized':
                        materialized[e['resource']['id']] = materialized.get(e['resource']['id'], 0) + 1
                cursor = page['next_cursor']
                if not page['has_more']:
                    break
            if until():
                return
            assert time.monotonic() < end, materialized
            time.sleep(.3)

    follow(lambda: len(materialized) >= 2)
    for pid in s['pids']:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    s['pids'] = []
    stack.save()
    time.sleep(3)  # at least one scheduled run is due while nothing is running
    stack.config()
    stack.start_processes()
    # A run killed mid-activity resumes after its 60 s activity timeout at worst.
    follow(lambda: len(materialized) == len(keys), deadline=180)
    records = {call('GET', '/v0/records/' + rid)['source']['record_key']: n for rid, n in materialized.items()}
    assert sorted(records) == sorted(keys), records
    assert all(n == 1 for n in records.values()), records
    health = call('GET', '/v0/connectors/' + connector)['health']
    assert health['state'] == 'active' and 'last_error' not in health, health
    call('POST', '/v0/connectors/' + connector + '/disable', {'idempotency_key': 'restart-stop'})
    for log in [*stack.directory.glob('api*.log*'), *stack.directory.glob('worker*.log*')]:
        assert b'fixture-test-secret' not in log.read_bytes(), 'secret leaked in ' + log.name
    (stack.directory / 'connector-restart.json').write_text(json.dumps({'status': 'passed', 'records': len(records), 'worker_killed_after': 2}))
