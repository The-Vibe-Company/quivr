"""Public-HTTP evidence that m365_mail collection resumes after the worker dies.

A mailbox on the local fake Graph holds a backlog spread over several delta
pages, served slowly. The worker is killed while the first run is still
paging; more mail arrives while nothing runs. After restart the instance
resumes from its committed delta checkpoint: every mail becomes exactly one
Record Version, and neither the deposited secret nor an access token appears
in any response or process log.
"""
import datetime, json, time, urllib.error, urllib.parse, urllib.request, uuid

SECRET = 'm365-restart-secret-not-real'


def verify(stack):
    s = stack.state
    base = f"http://127.0.0.1:{s['api_port']}"
    graph = f"http://127.0.0.1:{s['graph_port']}"
    run = uuid.uuid4().hex[:8]
    mailbox, client = f'monitoring-restart-{run}@example.org', f'app-restart-{run}'

    def fake(path, body):
        req = urllib.request.Request(graph + path, data=json.dumps(body).encode(), method='POST', headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(req, timeout=5) as r:
            assert r.status == 200

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
            assert SECRET.encode() not in raw and b'fake-access-' not in raw, 'secret leaked in a response'
            return result

    now = datetime.datetime.now(datetime.timezone.utc)
    stamp = lambda minutes: (now - datetime.timedelta(minutes=minutes)).strftime('%Y-%m-%dT%H:%M:%SZ')
    fake('/_fake/apps', {'client_id': client, 'secret': SECRET})
    backlog = [f'r{i:02d}' for i in range(45)]  # five delta pages of ten
    for i, mid in enumerate(backlog):
        fake('/_fake/messages', {'mailbox': mailbox, 'id': mid, 'received': stamp(50 - i), 'subject': f'Dépêche {mid}', 'html': f'<p>Texte {mid}</p>'})
    fake('/_fake/delay', {'mailbox': mailbox, 'seconds': 3})

    corpus = call('POST', '/v0/corpora', {'name': 'M365 restart', 'idempotency_key': 'm365-restart-' + run}, 201)['corpus_id']
    cursor = call('GET', '/v0/changes?corpus_id=' + corpus)['next_cursor']
    connector = call('POST', '/v0/connectors', {'idempotency_key': 'm365-restart-' + run, 'corpus_id': corpus, 'source_namespace': 'mail', 'kind': 'm365_mail',
                                                'config': {'tenant_id': '00000000-0000-0000-0000-000000000000', 'mailbox': mailbox, 'backfill_since': stamp(55)},
                                                'schedule': {'interval_seconds': 2}, 'credential': {'secret': {'client_id': client, 'client_secret': SECRET}}}, 201)['connector_id']
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
                    assert e['type'] != 'record.withdrawn', e
                cursor = page['next_cursor']
                if not page['has_more']:
                    break
            if until():
                return
            assert time.monotonic() < end, materialized
            time.sleep(.3)

    # The first committed page is accepted while later pages are still being served.
    follow(lambda: len(materialized) >= 1)
    stack.stop_worker()
    late = ['late-1', 'late-2']
    for mid in late:
        fake('/_fake/messages', {'mailbox': mailbox, 'id': mid, 'received': stamp(0), 'subject': 'Arrivée pendant l’arrêt', 'html': '<p>Nouvelle dépêche</p>'})
    fake('/_fake/delay', {'mailbox': mailbox, 'seconds': 0})
    time.sleep(3)
    stack.start_worker()
    # A run killed mid-activity is retried after its 30 s heartbeat timeout at worst.
    expected = backlog + late
    follow(lambda: len(materialized) == len(expected), deadline=240)
    records = {call('GET', '/v0/records/' + rid)['source']['record_key']: n for rid, n in materialized.items()}
    assert sorted(records) == sorted(f'<{m}@example.org>' for m in expected), records
    assert all(n == 1 for n in records.values()), records
    health = call('GET', '/v0/connectors/' + connector)['health']
    assert health['state'] == 'active', health
    call('POST', '/v0/connectors/' + connector + '/disable', {'idempotency_key': 'm365-restart-stop-' + run})
    for log in [*stack.directory.glob('api*.log*'), *stack.directory.glob('worker*.log*')]:
        data = log.read_bytes()
        assert SECRET.encode() not in data and b'fake-access-' not in data, 'secret or token leaked in ' + log.name
    (stack.directory / 'm365-restart.json').write_text(json.dumps({'status': 'passed', 'records': len(records)}))
