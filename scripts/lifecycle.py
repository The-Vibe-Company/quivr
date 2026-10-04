"""Stop, migrate and reset semantics of the local harness, proven on the isolated verification project (THE-662).

- down (make down) stops processes and containers and keeps data: a Corpus survives `up`.
- migrate (make migrate) is idempotent on a running project and refuses a stopped one with an actionable error.
- reset (make reset) deletes only this project's volumes and data-bound state; the next `up` starts from a fresh schema.

Assertions go through the public API; the harness only starts and stops the project.
"""
import json, time, urllib.error, urllib.request


def _call(stack, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(f"http://127.0.0.1:{stack.state['api_port']}{path}", data=data, method=method,
                                 headers={'Authorization': 'Bearer ' + stack.state['admin'], 'Content-Type': 'application/json'})
    try:
        response = urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, json.load(response)


def verify(stack):
    report = {}
    started = time.monotonic()
    # down/reset recreate the containers: keep the whole run's service logs first.
    stack.capture()
    for service in ['postgres', 'temporal', 'seaweed', 'weaviate', 'tei']:
        log = stack.directory / (service + '.log')
        if log.exists():
            log.replace(stack.directory / (service + '-before-lifecycle.log'))
    status, corpus = _call(stack, 'POST', '/v0/corpora', {'name': 'Lifecycle', 'idempotency_key': 'lifecycle-' + stack.name})
    assert status == 201, (status, corpus)
    corpus_id = corpus['corpus_id']

    # migrate on a running project: idempotent, the API stays ready.
    stack.migrate(); stack.migrate(); stack.await_ready('probe_port')
    assert _call(stack, 'GET', '/v0/corpora/' + corpus_id)[0] == 200
    report['migrate_running_idempotent'] = 'passed'

    # down keeps data; migrate on the stopped project is refused with guidance.
    stack.down(False)
    try:
        stack.migrate()
        raise AssertionError('migrate on a stopped project must fail')
    except RuntimeError as error:
        assert 'make dev' in str(error), error
    report['migrate_stopped_refused'] = 'passed'
    stack.up()
    status, body = _call(stack, 'GET', '/v0/corpora/' + corpus_id)
    assert status == 200 and body['corpus_id'] == corpus_id, (status, body)
    report['down_keeps_data'] = 'passed'

    # reset deletes this project's data only; up initializes a fresh schema.
    stack.down(True)
    assert 'scoped_id' not in stack.state
    stack.up()
    assert _call(stack, 'GET', '/v0/corpora/' + corpus_id)[0] == 404
    status, page = _call(stack, 'GET', '/v0/corpora')
    assert status == 200 and page['items'] == [], page
    report['reset_starts_fresh'] = 'passed'
    report['duration_seconds'] = round(time.monotonic() - started, 3)
    (stack.directory / 'lifecycle.json').write_text(json.dumps(report, indent=2))
