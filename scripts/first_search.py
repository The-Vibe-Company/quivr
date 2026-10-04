"""The first search after a restart answers within the default profile (THE-813).

The api's query encoder, core.ingest, loads its tokenizer on first use: about half a second, the
whole `default` budget. This step restarts core.ingest cold with the api, then sends the first
semantic and hybrid searches as soon as the api reports ready, before anything else calls the
plugin; the worker starts after them. They must answer, not 503 search_unavailable.
"""
import json, time, urllib.error, urllib.request

import connector_plugin


def verify(stack):
    s = stack.state
    base = f"http://127.0.0.1:{s['api_port']}"

    def call(method, path, body=None, status=200):
        req = urllib.request.Request(base + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                     headers={'Authorization': 'Bearer ' + s['admin'], 'Content-Type': 'application/json'})
        try:
            r = urllib.request.urlopen(req, timeout=8)
        except urllib.error.HTTPError as e:
            r = e
        with r:
            result = json.load(r)
            assert r.status == status, (method, path, r.status, result)
            return result

    corpus = call('POST', '/v0/corpora', {'name': 'First search', 'idempotency_key': 'first-search-after-start'}, 201)['corpus_id']
    command = {'idempotency_key': 'first-search-record', 'source': {'corpus_id': corpus, 'namespace': 'first-search', 'record_key': 'r'},
               'content': {'kind': 'text', 'text': 'Les phares de Bretagne guident les navires la nuit.'}}
    rid = call('POST', '/v0/records', command, 202)['receipt_id']
    deadline = time.monotonic() + 60
    # Searchable with its vectors: enrichment is done, so nothing calls core.ingest after the restart.
    while not ((r := call('GET', '/v0/ingestion-receipts/' + rid)).get('availability', {}).get('searchable') and r['processing']['state'] == 'idle'):
        assert time.monotonic() < deadline, r
        time.sleep(.2)
    # Cold: a new query encoder process and a new api, which reports ready first. The worker, which
    # shares core.ingest on this stack, starts only after the searches, so no ingestion warms it.
    stack.stop_processes()
    connector_plugin.start_first_party(stack, only=['core-ingest'])
    stack.spawn('api', 'config.json')
    stack.await_ready('probe_port')
    results = {}
    for mode in ['semantic', 'hybrid']:
        started = time.monotonic()
        answer = call('POST', '/v0/search', {'query': 'phare breton', 'corpus_ids': [corpus], 'mode': mode})
        results[mode] = {'hits': len(answer['items']), 'profile': answer['retrieval_profile'], 'seconds': round(time.monotonic() - started, 3)}
        assert answer['items'] and answer['items'][0]['record_id'] == r['record_id'], (mode, answer)
    stack.start_worker()
    (stack.directory / 'first-search-after-start.json').write_text(json.dumps(results))
