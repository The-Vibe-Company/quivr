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
            self.assertEqual(len(compose['volumes']), 5)
            self.assertEqual(compose['services']['worker']['depends_on']['migrate']['condition'],
                             'service_completed_successfully')
            self.assertEqual(compose['services']['demo']['profiles'], ['demo'])
            self.assertNotIn('build', compose['services']['api'])
            self.assertTrue(all('@sha256:' in s['image'] for s in compose['services'].values()))
            self.assertEqual(subprocess.run(command, capture_output=True).returncode, 0)
            self.assertEqual((directory / '.env').read_text(), env)
            # Two protected files must not collapse application and operator permissions.
            duplicated = env.replace(env.split('QUIVR_OPERATOR_KEY=')[1].splitlines()[0],
                                     env.split('QUIVR_API_KEY=')[1].splitlines()[0])
            (directory / '.env').write_text(duplicated)
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
