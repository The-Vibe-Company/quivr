"""The install CLI owns release validation, secrets and stable deployment identity."""
import importlib.util
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
import shutil

ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'deploy/single-machine/setup.py'


class SingleMachineTest(unittest.TestCase):
    def test_release_identity_accepts_stable_and_alpha_versions_only(self):
        spec = importlib.util.spec_from_file_location('single_machine_setup', SCRIPT)
        setup = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(setup)
        with tempfile.TemporaryDirectory() as temporary:
            release = pathlib.Path(temporary) / 'release.json'
            for version, valid in [('2.0.0', True), ('2.4.1', True), ('2.0.0-alpha.7', True),
                                   ('3.0.0', False), ('2.0.0-rc.1', False), ('2.0', False)]:
                release.write_text(json.dumps({'version': version, 'revision': 'b' * 40, 'models': {}, 'images': {}}))
                with self.subTest(version=version):
                    if valid:
                        with self.assertRaisesRegex(Exception, 'model|gemma|KeyError') as raised:
                            setup.release_images(release)
                        self.assertNotIn('release identity', str(raised.exception))
                    else:
                        with self.assertRaisesRegex(ValueError, 'invalid release identity'):
                            setup.release_images(release)

    def test_install_cli_preserves_secrets_and_refuses_model_switch_and_mutable_images(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = pathlib.Path(temporary)
            manifest = {'version': '2.0.0', 'revision': 'b' * 40,
                'models': {'gemma': 'c' * 40, 'e5': 'd' * 40}, 'images': {
                name: {'reference': f'ghcr.io/example/quivr-{name}@sha256:' + 'a' * 64,
                       'version': '2.0.0', 'revision': 'b' * 40}
                for name in ('core', 'web', 'cpu-embeddings', 'postgres', 'seaweed', 'tei')}}
            release = directory / 'release.json'
            release.write_text(json.dumps(manifest))
            # A downloaded bundle already contains its default Compose recipe.
            shutil.copyfile(ROOT / 'deploy/single-machine/compose.yaml', directory / 'compose.yaml')
            command = [sys.executable, str(SCRIPT), 'init', '--directory', str(directory),
                       '--release', str(release), '--project', 'example-evaluation']
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            env = (directory / '.env').read_text()
            self.assertNotIn(env.split('QUIVR_API_KEY=')[1].splitlines()[0], result.stdout)
            self.assertEqual((directory / '.env').stat().st_mode & 0o777, 0o600)
            compose = json.loads((directory / 'compose.yaml').read_text())
            self.assertEqual(compose['name'], 'example-evaluation')
            self.assertEqual(len(compose['volumes']), 4)
            for model in (compose, json.loads((ROOT / 'deploy/single-machine/compose.yaml').read_text())):
                self.assertEqual(model['services']['weaviate']['environment']['GRPC_PORT'], '50051')
                for process in ('api', 'worker', 'migrate'):
                    self.assertEqual(model['services'][process]['environment']['WEAVIATE_GRPC_ADDRESS'],
                                     'weaviate:50051')
            self.assertNotIn('temporal-data', compose['volumes'])
            temporal = compose['services']['temporal']
            self.assertIn('temporalio/auto-setup:', temporal['image'])
            self.assertEqual(temporal['environment']['DB'], 'postgres12')
            self.assertEqual(temporal['environment']['DBNAME'], 'temporal')
            self.assertEqual(temporal['environment']['VISIBILITY_DBNAME'], 'temporal_visibility')
            self.assertEqual(temporal['environment']['POSTGRES_SEEDS'], 'postgres')
            self.assertEqual(temporal['environment']['POSTGRES_PWD'], '${QUIVR_DB_PASSWORD:?}')
            self.assertEqual(temporal['depends_on'], {'postgres': {'condition': 'service_healthy'}})
            self.assertEqual(temporal['healthcheck']['test'], [
                'CMD-SHELL',
                'temporal operator namespace describe --address 127.0.0.1:7233 --namespace "$${DEFAULT_NAMESPACE:-default}"',
            ])
            self.assertNotIn('volumes', temporal)
            self.assertNotIn('command', temporal)
            for rendered in ((directory / 'compose.yaml').read_text(),
                             (ROOT / 'deploy/single-machine/compose.yaml').read_text()):
                self.assertNotIn('start-dev', rendered)
                self.assertNotIn('temporal.db', rendered)
            self.assertEqual(compose['services']['worker']['depends_on']['migrate']['condition'],
                             'service_completed_successfully')
            self.assertEqual(compose['services']['demo']['profiles'], ['demo'])
            self.assertEqual(compose['services']['api']['environment']['QUIVR_ADMIN_LISTEN'], '0.0.0.0:8082')
            self.assertEqual(compose['services']['api']['ports'], [
                '127.0.0.1:${QUIVR_API_PORT:-8080}:8080',
                '127.0.0.1:${QUIVR_ADMIN_PORT:-8082}:8082',
            ])
            self.assertEqual(compose['services']['demo']['environment']['QUIVR_ADMIN_URL'], 'http://api:8082')
            self.assertEqual(compose['services']['demo']['environment']['QUIVR_ADMIN_KEY'], '${QUIVR_DEMO_ADMIN_KEY:?}')
            self.assertNotIn('build', compose['services']['api'])
            self.assertTrue(all('@sha256:' in s['image'] for s in compose['services'].values()))
            self.assertEqual(subprocess.run(command, capture_output=True).returncode, 0)
            self.assertEqual((directory / '.env').read_text(), env)
            # Two protected files must not collapse application and operator/admin permissions.
            duplicated = env.replace(env.split('QUIVR_OPERATOR_KEY=')[1].splitlines()[0],
                                     env.split('QUIVR_API_KEY=')[1].splitlines()[0])
            (directory / '.env').write_text(duplicated)
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('must differ', result.stderr)
            self.assertEqual(json.loads((directory / 'compose.yaml').read_text()), compose)
            dup_admin = env.replace(env.split('QUIVR_ADMIN_KEY=')[1].splitlines()[0],
                                    env.split('QUIVR_API_KEY=')[1].splitlines()[0])
            (directory / '.env').write_text(dup_admin)
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('must differ', result.stderr)
            self.assertEqual(json.loads((directory / 'compose.yaml').read_text()), compose)
            (directory / '.env').write_text(env)
            switched = env.replace('QUIVR_EMBEDDING=gemma', 'QUIVR_EMBEDDING=e5')
            (directory / '.env').write_text(switched)
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('model', result.stderr)
            self.assertEqual(json.loads((directory / 'compose.yaml').read_text()), compose)
            (directory / '.env').write_text(env)
            manifest['models']['gemma'] = 'e' * 40
            release.write_text(json.dumps(manifest))
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('model revision', result.stderr)
            manifest['models']['gemma'] = 'c' * 40
            manifest['images']['core']['reference'] = 'ghcr.io/example/quivr-core:latest'
            release.write_text(json.dumps(manifest))
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('digest', result.stderr)
            # Loopback container bind is refused because host port publishing and demo require 0.0.0.0/[::].
            manifest['images']['core']['reference'] = 'ghcr.io/example/quivr-core@sha256:' + 'a' * 64
            release.write_text(json.dumps(manifest))
            for bad_bind in ('127.0.0.1:8082', 'localhost:8082'):
                (directory / '.env').write_text(env + f'QUIVR_ADMIN_LISTEN={bad_bind}\n')
                result = subprocess.run(command, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('0.0.0.0', result.stderr)
            # QUIVR_ADMIN_LISTEN=off omits the admin port and demo admin routing.
            (directory / '.env').write_text(env + 'QUIVR_ADMIN_LISTEN=off\n')
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            combined = json.loads((directory / 'compose.yaml').read_text())
            self.assertEqual(combined['services']['api']['ports'], ['127.0.0.1:${QUIVR_API_PORT:-8080}:8080'])
            self.assertEqual(combined['services']['api']['environment']['QUIVR_ADMIN_LISTEN'], 'off')
            for non_api in ('migrate', 'worker'):
                for key in ('QUIVR_ADMIN_KEY', 'QUIVR_DEMO_ADMIN_KEY', 'QUIVR_ADMIN_LISTEN'):
                    self.assertNotIn(key, combined['services'][non_api]['environment'])
            self.assertNotIn('QUIVR_ADMIN_URL', combined['services']['demo']['environment'])
            self.assertNotIn('QUIVR_ADMIN_KEY', combined['services']['demo']['environment'])

    def test_upgrade_reports_exact_legacy_temporal_volume_without_deleting_it(self):
        import io
        from unittest.mock import patch
        spec = importlib.util.spec_from_file_location('single_machine_setup', SCRIPT)
        setup = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(setup)
        with tempfile.TemporaryDirectory() as temporary:
            directory = pathlib.Path(temporary)
            manifest = {'version': '2.0.0', 'revision': 'b' * 40,
                'models': {'gemma': 'c' * 40, 'e5': 'd' * 40}, 'images': {
                name: {'reference': f'ghcr.io/example/quivr-{name}@sha256:' + 'a' * 64,
                       'version': '2.0.0', 'revision': 'b' * 40}
                for name in ('core', 'web', 'cpu-embeddings', 'postgres', 'seaweed', 'tei')}}
            release = directory / 'release.json'
            release.write_text(json.dumps(manifest))
            setup.initialize(directory, release, 'custom-stack', 'gemma', 'small', {})
            calls = []
            listed = ['custom-stack-temporal-data-backup\n',
                      'custom-stack-temporal-data-backup\ncustom-stack-temporal-data\n']
            def fake_docker(args):
                calls.append(args)
                if args[:2] == ['volume', 'ls']:
                    self.assertIn('name=^custom-stack-temporal-data$', args)
                    return listed.pop(0)
                return ''
            # A near-miss volume name alone must not trigger a retirement notice.
            output = io.StringIO()
            with patch.object(setup, 'docker', side_effect=fake_docker), \
                 patch.object(sys, 'argv', ['setup.py', 'upgrade', '--directory', str(directory), '--release', str(release)]), \
                 patch('sys.stdout', output):
                self.assertEqual(setup.main(), 0)
            self.assertNotIn('docker volume rm', output.getvalue())
            # When both a near-miss and the exact volume appear, only the exact volume is reported.
            output = io.StringIO()
            with patch.object(setup, 'docker', side_effect=fake_docker), \
                 patch.object(sys, 'argv', ['setup.py', 'upgrade', '--directory', str(directory), '--release', str(release)]), \
                 patch('sys.stdout', output):
                code = setup.main()
            self.assertEqual(code, 0)
            self.assertIn('docker volume rm custom-stack-temporal-data', output.getvalue())
            self.assertNotIn('custom-stack-temporal-data-backup', output.getvalue())
            self.assertNotIn('quivr-temporal-data', output.getvalue())
            self.assertFalse(any(args[:2] == ['volume', 'rm'] for args in calls))
            self.assertTrue(any('--verify-only' in args for args in calls))

            # If model cache verification fails (e.g. PyTorch -> ONNX upgrade), upgrade aborts before compose up.
            fail_calls = []
            def failing_verify(args):
                fail_calls.append(args)
                if '--verify-only' in args:
                    raise RuntimeError('Docker operation failed')
                return ''
            err_buf = io.StringIO()
            with patch.object(setup, 'docker', side_effect=failing_verify), \
                 patch.object(sys, 'argv', ['setup.py', 'upgrade', '--directory', str(directory), '--release', str(release)]), \
                 patch('sys.stderr', err_buf):
                self.assertEqual(setup.main(), 1)
            self.assertIn('setup.py prepare', err_buf.getvalue())
            self.assertFalse(any('up' in args for args in fail_calls))
