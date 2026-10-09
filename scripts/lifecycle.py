"""Stop, migrate and reset semantics of the local harness, proven on the isolated verification project (THE-662).

- down (make down) stops processes and containers and keeps data: a Corpus survives `up`.
- migrate (make migrate) is idempotent on a running project and refuses a stopped one with an actionable error.
- reset (make reset) deletes only this project's volumes and data-bound state; the next `up` starts from a fresh schema.

Assertions go through the public API; the harness only starts and stops the project.
"""
import json, os, subprocess, sys, time, urllib.error, urllib.request
from pathlib import Path

from deploy.reset_storage import s3_request


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

    # Seed a searchable document through the public API, then observe the index.
    status, receipt = _call(stack, 'POST', '/v0/records', {
        'idempotency_key': 'reset-content-' + stack.name,
        'source': {'corpus_id': corpus_id, 'namespace': 'reset-proof', 'record_key': 'ferry'},
        'content': {'kind': 'text', 'text': 'The ferry timetable lists the morning crossing.'}})
    assert status == 202, receipt
    from deploy.reset_support import wait_until
    index_url = json.loads((stack.directory / 'config.json').read_text())['weaviate_url']
    def indexed():
        with urllib.request.urlopen(index_url + '/v1/objects?limit=1', timeout=5) as response:
            return len(json.load(response).get('objects') or [])
    wait_until(indexed, lambda count: count > 0, seconds=30)
    # The command owns this proof: no direct down(True) can bypass its guards.
    source = stack.directory / 'lifecycle-sources.json'
    source.write_text(json.dumps({'version': 1, 'corpora': [{'idempotency_key': 'reload-' + stack.name,
                                                          'name': 'Reload lifecycle'}]}))
    source.chmod(0o600)
    apply_env = {**os.environ, 'QUIVR_API_URL': f"http://127.0.0.1:{stack.state['api_port']}",
                 'QUIVR_API_KEY': stack.state['admin']}
    def apply():
        result = subprocess.run([str(stack.directory / 'quivr'), 'apply', '-f', str(source), '--confirm'],
                                env=apply_env, capture_output=True, text=True)
        assert result.returncode == 0, result.stderr
        return result.stdout
    assert 'Applied.' in apply()
    assert 'No changes.' in apply()
    config = json.loads((stack.directory / 'config.json').read_text())['s3']
    s3_request(config['endpoint'], config['access_key'], config['secret_key'], 'PUT',
               '/' + config['bucket'] + '/installation-reset-proof', b'neutral reset fixture')
    temporal = stack.compose('ps', '-q', 'temporal', capture_output=True, text=True).stdout.strip()
    workflow_id = 'installation-reset-' + stack.name
    subprocess.run(['docker', 'exec', temporal, 'temporal', 'workflow', 'start', '--address',
                    '127.0.0.1:7233', '--namespace', 'default', '--workflow-id', workflow_id,
                    '--type', 'InstallationResetProof', '--task-queue', 'example.install-reload'],
                   capture_output=True, text=True, check=True)
    # A separate project volume must survive even when it shares the display prefix.
    survivor = stack.name + '-unrelated'
    subprocess.run(['docker', 'volume', 'create', '--label', 'com.docker.compose.project=' + stack.name, survivor], capture_output=True, check=True)
    installation = stack.directory / 'installation.json'
    installation.write_text(json.dumps({'version': 1, 'deployment': stack.name, 'platform': 'compose',
                                       'project': stack.name, 'dedicated': True}))
    installation.chmod(0o600)
    command = [sys.executable, str(Path(__file__).resolve().parents[1] / 'deploy/reset.py'), '-f', str(installation)]
    preview = subprocess.run(command, capture_output=True, text=True)
    assert preview.returncode == 0, preview.stderr
    assert json.loads(preview.stdout)['mode'] == 'preview'
    assert _call(stack, 'GET', '/v0/corpora/' + corpus_id)[0] == 200
    # Verification owns a fake provider subprocess; close it before the reset's
    # fresh Stack starts its provider fixture on the same saved port.
    if hasattr(stack, 'fake_graph'):
        stack.fake_graph.close()
        del stack.fake_graph
    try:
        result = subprocess.run([*command, '--confirm', '--deployment-name', stack.name],
                                capture_output=True, text=True)
        assert result.returncode == 0, result.stderr
        # Startup output precedes the final deletion report.
        report_start = result.stdout.rfind('{\n  "status": "reset"')
        assert report_start >= 0, 'reset deletion report missing'
        deletion = json.loads(result.stdout[report_start:])['result']
        assert deletion['terminated_workflows'] >= 1, deletion
        assert deletion['deleted_vector_objects'] >= 1, deletion
        assert deletion['deleted_blob_objects_at_least'] >= 1, deletion
        assert len(deletion['deleted_volumes']) == 4, deletion
        subprocess.run(['docker', 'volume', 'inspect', survivor], capture_output=True, check=True)
    finally:
        subprocess.run(['docker', 'volume', 'rm', survivor], capture_output=True, check=True)
        stack.state = json.loads(stack.statefile.read_text())
        stack.start_fake_graph()
    report['guarded_reset_survivor_and_workflows'] = 'passed'
    assert _call(stack, 'GET', '/v0/corpora/' + corpus_id)[0] == 404
    status, page = _call(stack, 'GET', '/v0/corpora')
    assert status == 200 and page['items'] == [], page
    report['reset_starts_fresh'] = 'passed'
    assert 'Applied.' in apply()
    assert 'No changes.' in apply()
    report['reload_with_same_private_journal'] = 'passed'
    report['duration_seconds'] = round(time.monotonic() - started, 3)
    (stack.directory / 'lifecycle.json').write_text(json.dumps(report, indent=2))
