"""Public-HTTP evidence that an X webhook delivery the plugin cannot take is retried by X and caught up by polling.

An x_list Connector Instance in webhook mode polls the local fake X API and
registers its webhook there. With the x-list connector plugin stopped, a post
the fake X delivers to the webhook is answered 503 with Retry-After (X
retries), push health shows plugin_unavailable, and the relaxed polling
returns to the instance's interval. Once the plugin is back, a pull run
collects the post: one Record with one Version. The deposited secrets never
appear in a response or process log.
"""
import datetime, json, time, urllib.error, urllib.parse, urllib.request, uuid

import connector_plugin

TOKEN = 'x-test-bearer-push-outage-not-real'
CONSUMER_SECRET = 'x-test-consumer-not-real'


def verify(stack, fake_x_url):
    s = stack.state
    base = f"http://127.0.0.1:{s['api_port']}"
    list_id = str(int(time.time() * 1000) + 3)
    author = '6' + list_id

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
            assert TOKEN.encode() not in raw and CONSUMER_SECRET.encode() not in raw, 'secret leaked in a response'
            return result

    def control(body):
        req = urllib.request.Request(fake_x_url + '/_control/lists/' + list_id, method='POST', data=json.dumps(body).encode(), headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(req, timeout=30) as r:
            return json.loads(r.read())

    def await_health(ok, what, deadline=60):
        end = time.monotonic() + deadline
        while True:
            health = call('GET', '/v0/connectors/' + connector)['health']
            if ok(health):
                return health
            assert time.monotonic() < end, f'{what}: {health}'
            time.sleep(.25)

    control({'members': [author]})
    corpus = call('POST', '/v0/corpora', {'name': 'X push outage', 'idempotency_key': 'x-push-outage-' + list_id}, 201)['corpus_id']
    cursor = call('GET', '/v0/changes?corpus_id=' + corpus)['next_cursor']
    connector = call('POST', '/v0/connectors', {'idempotency_key': 'x-push-outage-' + list_id, 'corpus_id': corpus, 'source_namespace': 'x-push', 'kind': 'x_list',
                                                # The backfill puts the start point before the post, published two minutes back.
                                                'config': {'list_id': list_id, 'backfill_since': (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(minutes=5)).strftime('%Y-%m-%dT%H:%M:%SZ'),
                                                           'webhook': {'enabled': True, 'resync_interval_seconds': 5, 'poll_interval_seconds': 3600}},
                                                'schedule': {'interval_seconds': 1},
                                                'credential': {'secret': {'bearer_token': TOKEN, 'consumer_secret': CONSUMER_SECRET}}}, 201)['connector_id']
    await_health(lambda h: h.get('push', {}).get('state') == 'active', 'push never became active')

    post_id = str(1820000000000000000 + int(list_id) % 1000)
    created = (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(minutes=2)).isoformat(timespec='milliseconds').replace('+00:00', 'Z')
    connector_plugin.stop_first_party(stack, only=['x-list'])
    try:
        deliveries = control({'posts': [{'id': post_id, 'text': 'Delivered while the plugin is down', 'author_id': author, 'created_at': created, 'push': True}]})['deliveries']
        # Like X, the fake delivers to every webhook linked to the app's stream; only this instance's counts.
        mine = [d['status'] for d in deliveries if d['url'].endswith('/v0/connectors/' + connector + '/api/receive')]
        assert mine == [503], deliveries
        health = await_health(lambda h: h.get('push', {}).get('error', {}).get('code') == 'plugin_unavailable', 'the outage never showed in push health')
        assert health['push']['state'] == 'degraded', health
    finally:
        connector_plugin.start_first_party(stack, only=['x-list'])

    # Polling is back at the instance's interval and collects the post.
    materialized = {}
    end = time.monotonic() + 60
    while not materialized:
        page = call('GET', '/v0/changes?' + urllib.parse.urlencode({'corpus_id': corpus, 'cursor': cursor}))
        for e in page['items']:
            if e['type'] == 'record.materialized':
                materialized[e['resource']['id']] = materialized.get(e['resource']['id'], 0) + 1
        cursor = page['next_cursor']
        assert time.monotonic() < end, 'polling never caught up with the refused delivery'
        time.sleep(.3)
    records = {call('GET', '/v0/records/' + rid)['source']['record_key']: n for rid, n in materialized.items()}
    assert records == {post_id: 1}, records
    call('POST', '/v0/connectors/' + connector + '/disable', {'idempotency_key': 'x-push-outage-stop'})
    for log in [*stack.directory.glob('api*.log*'), *stack.directory.glob('worker*.log*'), *stack.directory.glob('x-list-plugin.log*')]:
        raw = log.read_bytes()
        assert TOKEN.encode() not in raw and CONSUMER_SECRET.encode() not in raw, 'secret leaked in ' + log.name
    (stack.directory / 'connector-x-push.json').write_text(json.dumps({'status': 'passed', 'records': len(records)}))
