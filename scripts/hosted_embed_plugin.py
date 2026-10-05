"""Fake-only certification and local-stack pin/search of hosted.embed."""
import plugin_environment
import argparse
import json
import os
import pathlib
import subprocess
import time
import urllib.request

from fake_api import Fake
import ingestion_plugin
import ports

ROOT = pathlib.Path(__file__).resolve().parents[1]
PLUGIN = ROOT / 'plugins' / 'hosted-embed'


def build(directory):
    directory.mkdir(parents=True, exist_ok=True)
    binary = directory.resolve() / 'hosted-embed'
    subprocess.run([os.environ.get('GO', 'go'), 'build', '-o', str(binary), '.'], cwd=PLUGIN, check=True)
    return binary


def package(binary, directory, endpoint, format):
    directory.mkdir(parents=True, exist_ok=True)
    configuration = {'format': format, 'base_url': endpoint + ('/openai/v1' if format == 'openai' else '/providers/cohere/v2'),
                     'auth': 'api-key' if format == 'cohere' else 'bearer', 'model': 'test-model', 'dimensions': 8,
                     'query_prefix': 'query: ', 'document_prefix': 'passage: '}
    config = directory / 'configuration.json'
    config.write_text(json.dumps(configuration))
    manifest = directory / 'quivr-plugin.yaml'
    with manifest.open('w') as output:
        subprocess.run([str(binary), 'configure', str(config)], stdout=output, check=True)
    declaration = json.loads(manifest.read_text())
    space = next(iter(declaration['contributions']['ingestion']['spaces']))
    return manifest, configuration, space


def certify(quivr, directory):
    started = time.monotonic()
    binary = build(directory)
    with Fake('embedding') as fake:
        for format in ['openai', 'cohere']:
            manifest, config, _ = package(binary, directory / format, fake.url, format)
            article = json.loads((ROOT / 'contracts/plugins/v0/fixtures/ingestion/article.json').read_text())
            article['ingestion']['configuration'] = config
            fixture = manifest.parent / 'fixture.json'
            fixture.write_text(json.dumps(article))
            report = directory / f'hosted-embed-{format}-contract-report.json'
            log = directory / f'hosted-embed-{format}-contract.log'
            with log.open('w') as output:
                result = subprocess.run([str(quivr), 'plugin', 'test', '--startup-timeout', '120s', '--report', str(report),
                                         '--fixture', str(fixture), str(manifest.parent)],
                                        env={**os.environ, 'AZURE_FOUNDRY_KEY': 'fake-key'}, stdout=output, stderr=subprocess.STDOUT)
            if result.returncode or '\nCERTIFIED' not in '\n' + log.read_text():
                raise RuntimeError(f'{format} did not certify: {log.read_text()}')
            print(f'Certified hosted.embed {format}: {report}', flush=True)
    print(f'Hosted embedding certification: {time.monotonic() - started:.1f}s', flush=True)


def verify(stack):
    """Use both provider formats as actual pins, restoring the stack afterwards."""
    started = time.monotonic()
    directory = stack.directory / 'hosted-embed'
    binary = build(directory)
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    plugin = None
    try:
        with Fake('embedding') as fake:
            for format in ['openai', 'cohere']:
                manifest, config, space = package(binary, directory / format, fake.url, format)
                # Each configured manifest is an immutable registration. Distinct
                # release versions also let this one stack switch format safely.
                if format == 'cohere':
                    config['plugin_version'] = '1.0.1'
                    config_path = manifest.parent / 'configuration.json'
                    config_path.write_text(json.dumps(config))
                    with manifest.open('w') as output:
                        subprocess.run([str(binary), 'configure', str(config_path)], stdout=output, check=True)
                port = ports.allocate()
                env = {**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port),
                       'QUIVR_PLUGIN_MANIFEST': str(manifest), 'AZURE_FOUNDRY_KEY': 'fake-key'}
                log = directory / f'{format}.log'
                with log.open('w') as output:
                    plugin = subprocess.Popen([str(binary)], env=env, stdout=output, stderr=output, start_new_session=True)
                ingestion_plugin.await_healthy(plugin, port, log)
                for name, original in configs.items():
                    cfg = json.loads(original)
                    cfg['ingestion'] = {'default': 'hosted.embed'}
                    cfg['plugins'].append({'manifest': str(manifest), 'endpoint': f'http://127.0.0.1:{port}',
                                           'configuration': config, 'spaces': {space: 'served'}})
                    path = stack.directory / name
                    path.write_text(json.dumps(cfg))
                    path.chmod(0o600)
                stack.stop_processes()
                stack.start_processes()
                stack.tests('^TestHostedEmbeddingPinFindsText$', {'QUIVR_TEST_HOSTED_SPACE': space})
                stack.stop_processes()
                ingestion_plugin.stop_plugin(plugin)
                plugin = None
    finally:
        if plugin is not None:
            ingestion_plugin.stop_plugin(plugin)
        for name, original in configs.items():
            (stack.directory / name).write_text(original)
        stack.stop_processes()
        stack.start_processes()
    (stack.directory / 'hosted-embed.json').write_text(json.dumps({'formats': ['openai', 'cohere'], 'seconds': round(time.monotonic() - started, 1)}))


def verify_redeploy(stack):
    """Promote a configured owner, replace its build, then search and revert.

    Run before other plugin scenarios create Corpora: promotion requires every
    routed generation to carry the evaluation space.
    """
    started = time.monotonic()
    directory = stack.directory / 'hosted-redeploy'
    binary = build(directory / 'old')
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    # Configuration restoration alone intentionally preserves admin overrides.
    # Return to this exact reachable plan after exercising the serving changes.
    request = urllib.request.Request(f"http://127.0.0.1:{stack.state['api_port']}/v0/admin/plugins/plan",
                                     headers={'Authorization': 'Bearer ' + stack.state['operator']})
    with urllib.request.urlopen(request, timeout=10) as response:
        original_plan = json.load(response)['plan_id']
    plugin = None
    retained_plugin = None
    try:
        with Fake('embedding') as fake:
            manifest, config, space = package(binary, directory / 'old', fake.url, 'cohere')
            port = ports.allocate()

            def start(executable, declaration, name):
                env = {**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port),
                       'QUIVR_PLUGIN_MANIFEST': str(declaration), 'AZURE_FOUNDRY_KEY': 'fake-key'}
                log = directory / (name + '.log')
                with log.open('w') as output:
                    process = subprocess.Popen([str(executable)], env=env, stdout=output, stderr=output, start_new_session=True)
                try:
                    ingestion_plugin.await_healthy(process, port, log)
                except BaseException:
                    ingestion_plugin.stop_plugin(process)
                    raise
                return process

            def configure(declaration):
                for name, original in configs.items():
                    cfg = json.loads(original)
                    cfg['ingestion'] = {'default': 'core.ingest', 'evaluation': {'text/plain': ['hosted.embed']}}
                    cfg['plugins'].append({'manifest': str(declaration), 'endpoint': f'http://127.0.0.1:{port}',
                                           'configuration': config, 'spaces': {space: 'served'}})
                    path = stack.directory / name
                    path.write_text(json.dumps(cfg))
                    path.chmod(0o600)

            plugin = start(binary, manifest, 'old')
            configure(manifest)
            stack.stop_processes()
            stack.start_processes()
            run = {'QUIVR_TEST_HOSTED_REDEPLOY_SPACE': space,
                   'QUIVR_TEST_HOSTED_REDEPLOY_ORIGINAL_PLAN': original_plan}
            stack.tests('^TestHostedEmbeddingRedeployBefore$', run)
            stack.stop_processes()
            ingestion_plugin.stop_plugin(plugin)
            plugin = None
            # Two builds with the same id/version/configuration/space. The
            # generated run command changes their exact manifest digests.
            next_binary = build(directory / 'new')
            next_manifest, _, next_space = package(next_binary, directory / 'new', fake.url, 'cohere')
            assert next_space == space and next_manifest.read_bytes() != manifest.read_bytes()
            plugin = start(next_binary, next_manifest, 'new')
            configure(next_manifest)
            stack.start_processes()
            stack.tests('^TestHostedEmbeddingRedeployAfter$', run)
            # An endpoint move is incompatible with automatic build following.
            # Keep the prior endpoint reachable for the exact saved rollback.
            stack.stop_processes()
            retained_plugin, plugin = plugin, None
            port = ports.allocate()
            plugin = start(next_binary, next_manifest, 'moved')
            configure(next_manifest)
            stack.start_processes()
            stack.tests('^TestHostedEmbeddingRollbackCoverage$', run)
    finally:
        if plugin is not None:
            ingestion_plugin.stop_plugin(plugin)
        if retained_plugin is not None:
            ingestion_plugin.stop_plugin(retained_plugin)
        for name, original in configs.items():
            (stack.directory / name).write_text(original)
        stack.stop_processes()
        stack.start_processes()
    (stack.directory / 'hosted-redeploy.json').write_text(json.dumps({'seconds': round(time.monotonic() - started, 1)}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--quivr', type=pathlib.Path, required=True)
    parser.add_argument('--out', type=pathlib.Path, required=True)
    args = parser.parse_args()
    certify(args.quivr.resolve(), args.out.resolve())
