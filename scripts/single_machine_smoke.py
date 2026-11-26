"""Public API proof of an image-only Compose installation and restart."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import shutil
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]


def http(base, key, method, path, body=None, expected=200):
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(base + path, data=data, method=method,
        headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
    try:
        response = urllib.request.urlopen(request, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        if response.status != expected:
            raise RuntimeError(f'unexpected HTTP response status {response.status} (expected {expected})')
        return json.load(response)


def until(probe, description, seconds=120):
    deadline = time.monotonic() + seconds
    while True:
        value = probe()
        if value:
            return value
        if time.monotonic() >= deadline:
            raise RuntimeError('deadline waiting for ' + description)
        # Poll an observable condition, never depend on an elapsed sleep for success.
        time.sleep(.1)


def run(directory, model):
    spec = importlib.util.spec_from_file_location('single_machine_setup', directory / 'setup.py')
    setup = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(setup)
    project = 'compose-smoke-' + uuid.uuid4().hex[:12]
    values, _ = setup.initialize(directory, directory / 'release.json', project, model, 'small', {})
    values.update(QUIVR_API_PORT='0', QUIVR_ADMIN_PORT='0', QUIVR_DEMO_PORT='0')
    setup.write_private(directory / '.env', ''.join(k + '=' + v + '\n' for k, v in values.items()))
    compose = ['docker', 'compose', '--project-directory', str(directory), '--profile', 'demo']
    def command(*args):
        result = subprocess.run([*compose, *args], capture_output=True, text=True, timeout=600)
        if result.returncode:
            # Docker failures may include expanded credentials. Emit states, never full config/logs.
            states = subprocess.run([*compose, 'ps', '-a', '--format', 'json'], capture_output=True, text=True, timeout=20)
            for line in states.stdout.splitlines():
                try:
                    row = json.loads(line)
                    if isinstance(row, dict):
                        print(json.dumps({key: row.get(key) for key in ('Service', 'State', 'Health', 'ExitCode')}))
                except ValueError:
                    pass
            raise RuntimeError('Compose ' + args[0] + ' failed; inspect the isolated runner privately')
        return result.stdout.strip()
    def failed_migration():
        path = directory / 'compose.yaml'
        original = path.read_text()
        broken = json.loads(original)
        broken['services']['migrate']['environment']['DATABASE_URL'] = 'postgres://quivr:fixture-invalid-password@postgres:5432/quivr?sslmode=disable'
        path.write_text(json.dumps(broken))
        try:
            result = subprocess.run([*compose, 'up', '-d', '--wait', '--wait-timeout', '120'],
                                    capture_output=True, text=True, timeout=150)
            if result.returncode == 0:
                raise RuntimeError('invalid migration credentials unexpectedly started the stack')
            rows = [json.loads(line) for line in command('ps', '-a', '--format', 'json').splitlines() if line.strip()]
            rows = [row for entry in rows for row in (entry if isinstance(entry, list) else [entry])]
            migrator = next((row for row in rows if row['Service'] == 'migrate'), None)
            if not migrator or migrator.get('State') != 'exited' or migrator.get('ExitCode') in (None, 0):
                raise RuntimeError('fault did not reach a failed one-shot migration')
            if any(row['Service'] in ('api', 'worker') and row['State'] == 'running' for row in rows):
                raise RuntimeError('API/worker started despite failed migration')
        finally:
            path.write_text(original)
        command('rm', '-f', 'migrate')
    try:
        setup.prepare_cache(directory, None, None)
        values['QUIVR_OFFLINE'] = '1'
        setup.write_private(directory / '.env', ''.join(k + '=' + v + '\n' for k, v in values.items()))
        setup.initialize(directory, directory / 'release.json', project, model, 'small', {})
        command('config', '--quiet')
        command('pull')
        failed_migration()
        command('up', '-d', '--wait', '--wait-timeout', '360')
        port = command('port', 'api', '8080')
        admin_port = command('port', 'api', '8082')
        base, admin_base, key = 'http://' + port, 'http://' + admin_port, values['QUIVR_API_KEY']
        admin_key = values['QUIVR_ADMIN_KEY']
        # Admin routes must be refused on the functional API port and served only on the dedicated admin listener.
        http(base, key, 'GET', '/v0/admin/plugins', expected=404)
        http(base, admin_key, 'GET', '/v0/admin/plugins', expected=404)
        http(admin_base, key, 'GET', '/v0/admin/plugins', expected=401)
        http(admin_base, admin_key, 'GET', '/v0/admin/plugins', expected=200)
        http(admin_base, admin_key, 'GET', '/v0/corpora', expected=404)
        corpus = http(base, key, 'POST', '/v0/corpora', {
            'name': 'Compose smoke', 'idempotency_key': project}, 201)['corpus_id']
        cursor = http(base, key, 'GET', '/v0/changes?corpus_id=' + corpus)['next_cursor']
        expected_records = set()
        # Multiple passages expose CPU provider batching/deadline mistakes missed by one short input.
        texts = ['The lighthouse guides ships into the harbour.\n\n' +
                 ('Ships follow the lighthouse safely across the bay. ' * 80),
                 'Gardeners plant apple trees in the orchard.',
                 'The observatory measures distant stars and planets.']
        for index, text in enumerate(texts):
            receipt = http(base, key, 'POST', '/v0/records', {
                'idempotency_key': project + '-' + str(index), 'source': {
                    'corpus_id': corpus, 'namespace': 'example.single-machine', 'record_key': str(index)},
                'content': {'kind': 'text', 'text': text}}, 202)['receipt_id']
            def ready():
                result = http(base, key, 'GET', '/v0/ingestion-receipts/' + receipt)
                return result if result.get('availability', {}).get('searchable') else None
            expected_records.add(until(ready, 'searchable receipt')['record_id'])
        enriched = set()
        def enrichment():
            nonlocal cursor
            page = http(base, key, 'GET', '/v0/changes?' + urllib.parse.urlencode({'corpus_id': corpus, 'cursor': cursor}))
            cursor = page['next_cursor']
            for event in page['items']:
                if event['type'] == 'record.enrichment_available':
                    enriched.add(event['resource']['id'])
            return expected_records <= enriched
        until(enrichment, 'vector enrichment events')
        def searches():
            for mode in ('lexical', 'semantic', 'hybrid'):
                result = http(base, key, 'POST', '/v0/search', {
                    'query': 'lighthouse harbour', 'corpus_ids': [corpus], 'mode': mode})
                if not result['items'] or not any(item['record_id'] in expected_records for item in result['items']):
                    raise RuntimeError(mode + ' search did not find an ingested document')
        searches()
        # Dispatch baseline queries while newly accepted ingestion can run.
        # Completion speed is not a failure: E5 may finish before the queries.
        pending_receipt = http(base, key, 'POST', '/v0/records', {
            'idempotency_key': project + '-concurrent', 'source': {
                'corpus_id': corpus, 'namespace': 'example.single-machine', 'record_key': 'concurrent'},
            'content': {'kind': 'text', 'text': '\n\n'.join(
                'Section ' + str(index) + ': ' + ('Ships follow the lighthouse across the bay. ' * 80)
                for index in range(8))}}, 202)['receipt_id']
        def pending_searchable():
            result = http(base, key, 'GET', '/v0/ingestion-receipts/' + pending_receipt)
            return result if result.get('availability', {}).get('searchable') else None
        pending_observed = not bool(pending_searchable())
        searches()
        pending_record = until(pending_searchable, 'concurrent searchable receipt')['record_id']
        expected_records.add(pending_record)
        until(enrichment, 'concurrent vector enrichment')
        # Recreate processes on the same volumes; the migrator must run again, offline.
        command('stop')
        command('rm', '-f', 'migrate')
        failed_migration()
        # A second bundle directory must retain project and volume identity.
        upgraded = directory.parent / (directory.name + '-upgrade')
        shutil.copytree(directory, upgraded)
        setup.initialize(upgraded, upgraded / 'release.json', project, model, 'small', {})
        compose = ['docker', 'compose', '--project-directory', str(upgraded), '--profile', 'demo']
        command('up', '-d', '--wait', '--wait-timeout', '360')
        base = 'http://' + command('port', 'api', '8080')
        searches()
        demo = 'http://' + command('port', 'demo', '3000')
        with urllib.request.urlopen(demo, timeout=10) as response:
            if response.status != 200:
                raise RuntimeError('demo profile did not serve')
        print(json.dumps({'model': model, 'status': 'passed', 'documents': len(expected_records),
                          'search_modes': ['lexical', 'semantic', 'hybrid'], 'queries_after_ingestion_acceptance': True, 'pending_receipt_observed': pending_observed, 'offline_restart': True, 'demo': True}))
    finally:
        command('down', '-v', '--remove-orphans')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--model', choices=['gemma', 'e5'], required=True)
    args = parser.parse_args()
    run(args.directory.resolve(), args.model)
