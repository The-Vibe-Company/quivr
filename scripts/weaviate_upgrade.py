"""Prove persisted adapter search across sequential Weaviate minor upgrades."""
import argparse
import json
import os
import pathlib
import sys
import subprocess
import time
import urllib.error
import urllib.request
import uuid

import gotest

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from deploy import infrastructure
OLD_IMAGE = 'cr.weaviate.io/semitechnologies/weaviate:1.37.15@sha256:cede92f9f43f9b4be25a9f453e756173bbe171d2e88210a0e5b7ab72e8b96d96'
BRIDGE_IMAGE = 'cr.weaviate.io/semitechnologies/weaviate:1.38.20@sha256:d23d7bb6242026106ee1ec5aefdbb6a867e260ff171cedc9c3af55c9688e30f6'
OWNER = 'TestPersistedProjectionAcrossWeaviateUpgrade'


def verify(stack):
    exercise(stack.directory / 'weaviate-upgrade')


def exercise(directory, negative_control=False):
    directory.mkdir(parents=True, exist_ok=True)
    project = 'quivr-weaviate-upgrade-' + uuid.uuid4().hex[:10]
    compose_file = directory / 'compose.json'
    config_file = directory / 'config.json'
    snapshot = directory / 'snapshot.json'
    target = infrastructure.resolve(environ={})['weaviate']['image']
    report = {'images': [OLD_IMAGE, BRIDGE_IMAGE, target], 'stages': [], 'result': 'failed', 'negative_control': negative_control}
    started = time.monotonic()

    def compose(*args, **kwargs):
        return subprocess.run(['docker', 'compose', '-p', project, '-f', str(compose_file), *args], cwd=ROOT, **{'check': True, **kwargs})

    def start(image):
        compose_file.write_text(json.dumps({'services': {'weaviate': {
            'image': image,
            'environment': {'AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED': 'true', 'AUTOSCHEMA_ENABLED': 'false', 'CLUSTER_HOSTNAME': 'quivr-upgrade', 'DEFAULT_VECTORIZER_MODULE': 'none', 'PERSISTENCE_DATA_PATH': '/var/lib/weaviate'},
            'ports': ['127.0.0.1::8080'], 'volumes': ['data:/var/lib/weaviate'],
        }}, 'volumes': {'data': {}}}))
        compose('up', '-d', stdout=subprocess.DEVNULL)
        address = compose('port', 'weaviate', '8080', capture_output=True, text=True).stdout.strip()
        endpoint = 'http://' + address
        config_file.write_text(json.dumps({'weaviate_url': endpoint}))
        version = image.split(':', 1)[1].split('@', 1)[0]
        await_server(endpoint, version)
        report['stages'].append({'version': version, 'raft_synchronized': True})

    def await_server(endpoint, version):
        deadline = time.monotonic() + 180
        last = None
        # Condition polling covers provisioning, not an assertion wait or a retry
        # of failed tests. Every minor must report synchronized Raft metadata.
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(endpoint + '/v1/.well-known/ready', timeout=2):
                    pass
                with urllib.request.urlopen(endpoint + '/v1/meta', timeout=2) as response:
                    meta = json.load(response)
                if meta['version'] != version:
                    raise RuntimeError(f'expected server {version}, got {meta["version"]}')
                with urllib.request.urlopen(endpoint + '/v1/cluster/statistics', timeout=2) as response:
                    statistics = json.load(response)
                nodes = statistics.get('statistics', [])
                if len(nodes) == 1 and statistics.get('synchronized') is True:
                    return
                last = statistics
            except (urllib.error.URLError, TimeoutError, ConnectionError) as error:
                last = str(error)
            time.sleep(.1)  # Poll the observable readiness/synchronization condition, bounded above.
        raise RuntimeError(f'Weaviate {version} did not become ready and Raft-synchronized: {last}')

    def stop(version):
        await_server(json.loads(config_file.read_text())['weaviate_url'], version)
        compose('stop', '--timeout', '30', 'weaviate', stdout=subprocess.DEVNULL)
        container = compose('ps', '--all', '-q', 'weaviate', capture_output=True, text=True).stdout.strip()
        state = json.loads(subprocess.check_output(['docker', 'inspect', '--format', '{{json .State}}', container], text=True))
        if state['ExitCode'] != 0 or state['OOMKilled']:
            raise RuntimeError(f'Weaviate {version} shutdown was not clean: {state}')

    def test(phase, version):
        try:
            results = gotest.run(os.environ.get('GO', 'go'), ['-count=1', '-timeout=30s', '-run', '^' + OWNER + '$', './internal/adapters/weaviate'],
                                log=directory / (version + '-' + phase + '.log'), cwd=ROOT,
                                env={**os.environ, 'QUIVR_ADAPTER_CONFIG': str(config_file), 'QUIVR_WEAVIATE_UPGRADE_PHASE': phase, 'QUIVR_WEAVIATE_UPGRADE_SNAPSHOT': str(snapshot)})
        except gotest.Failed as error:
            for failure in error.failures:
                print(failure['excerpt'], flush=True)
            raise
        if not any(item['test'] == OWNER and item['status'] == 'pass' for item in results.tests()):
            raise RuntimeError('upgrade owner test did not execute and pass')
        report['stages'][-1]['test'] = results.tests()

    def logs(version):
        with (directory / (version + '-weaviate.log')).open('w') as out:
            compose('logs', '--no-color', 'weaviate', stdout=out, stderr=out, check=False)

    try:
        for index, image in enumerate([OLD_IMAGE, target] if negative_control else report['images']):
            version = image.split(':', 1)[1].split('@', 1)[0]
            stage_started = time.monotonic()
            try:
                start(image)
                test('seed' if index == 0 else 'verify', version)
                stop(version)
                report['stages'][-1]['seconds'] = round(time.monotonic() - stage_started, 3)
            finally:
                logs(version)
            if negative_control and index == 0:
                compose('down', '--volumes', stdout=subprocess.DEVNULL)
        report['result'] = 'passed'
    finally:
        report['seconds'] = round(time.monotonic() - started, 3)
        cleanup = compose('down', '--volumes', stdout=subprocess.DEVNULL, check=False)
        passed = report['result'] == 'passed'
        if cleanup.returncode:
            report['cleanup_failed'] = True
            report['result'] = 'failed'
        (directory / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
        if cleanup.returncode and passed:
            raise RuntimeError('failed to remove the isolated upgrade containers/volume')
        print('Weaviate upgrade evidence:', directory, flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=pathlib.Path, default=ROOT / '.scratch' / ('quivr-weaviate-upgrade-' + uuid.uuid4().hex[:10]))
    parser.add_argument('--negative-control', action='store_true', help='delete only this run’s seeded volume; verification must fail on missing persisted data')
    args = parser.parse_args()
    exercise(args.output.resolve(), args.negative_control)
