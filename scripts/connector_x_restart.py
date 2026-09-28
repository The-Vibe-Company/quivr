"""Public-HTTP evidence that x_list polling resumes from its watermark after the worker dies.

An x_list Connector Instance polls the local fake X API. Posts published before
the kill are collected; the API and worker are then killed, more posts are
published while nothing runs, and after restart every post of the list is one
Record with exactly one materialized Version: no gap and no duplicate. The
deposited bearer token never appears in a response or process log.
"""
import datetime, json, os, signal, time, urllib.error, urllib.parse, urllib.request, uuid

TOKEN = 'x-test-bearer-restart-not-real'


def verify(stack, fake_x_url):
    s = stack.state
    base = f"http://127.0.0.1:{s['api_port']}"
    list_id = str(int(time.time() * 1000))

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
            assert TOKEN.encode() not in raw, 'secret leaked in a response'
            return result

    def publish(first, count):
        now = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='milliseconds').replace('+00:00', 'Z')
        posts = [{'id': str(1900000000000000000 + first + i), 'text': f'Post {first + i}', 'created_at': now} for i in range(count)]
        req = urllib.request.Request(fake_x_url + '/_control/lists/' + list_id, method='POST', data=json.dumps({'posts': posts}).encode(), headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(req, timeout=5):
            pass
        return [p['id'] for p in posts]

    corpus = call('POST', '/v0/corpora', {'name': 'X restart', 'idempotency_key': 'x-restart-' + list_id}, 201)['corpus_id']
    cursor = call('GET', '/v0/changes?corpus_id=' + corpus)['next_cursor']
    connector = call('POST', '/v0/connectors', {'idempotency_key': 'x-restart-' + list_id, 'corpus_id': corpus, 'source_namespace': 'x-restart', 'kind': 'x_list',
                                                'config': {'list_id': list_id}, 'schedule': {'interval_seconds': 1},
                                                'credential': {'secret': {'bearer_token': TOKEN}}}, 201)['connector_id']
    deadline = time.monotonic() + 60
    while 'last_success_at' not in call('GET', '/v0/connectors/' + connector)['health']:
        assert time.monotonic() < deadline, 'first poll never completed'
        time.sleep(.3)
    keys = publish(1, 7)
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
    keys += publish(20, 6)  # published while nothing runs
    time.sleep(3)
    stack.config()
    stack.start_processes()
    follow(lambda: len(materialized) == len(keys), deadline=180)
    records = {call('GET', '/v0/records/' + rid)['source']['record_key']: n for rid, n in materialized.items()}
    assert sorted(records) == sorted(keys), records
    assert all(n == 1 for n in records.values()), records
    health = call('GET', '/v0/connectors/' + connector)['health']
    assert health['state'] == 'active' and health['usage']['items_read'] >= len(keys), health
    call('POST', '/v0/connectors/' + connector + '/disable', {'idempotency_key': 'x-restart-stop'})
    for log in [*stack.directory.glob('api*.log*'), *stack.directory.glob('worker*.log*')]:
        assert TOKEN.encode() not in log.read_bytes(), 'secret leaked in ' + log.name
    (stack.directory / 'connector-x-restart.json').write_text(json.dumps({'status': 'passed', 'records': len(records)}))
