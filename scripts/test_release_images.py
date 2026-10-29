"""Release inventory owns tag validation and first-party image selection."""
import json
import pathlib
import tempfile
import unittest

import release_images


class ReleaseImagesTests(unittest.TestCase):
    def test_release_includes_complete_deployment_runtimes(self):
        result = release_images.inventory(release_images.ROOT, 'example/quivr')['include']
        components = {entry['component']: entry['image'] for entry in result if entry.get('component')}
        self.assertEqual(components, {
            'core': 'ghcr.io/example/quivr-core', 'web': 'ghcr.io/example/quivr-web',
            'cpu-embeddings': 'ghcr.io/example/quivr-cpu-embeddings',
            'postgres': 'ghcr.io/example/quivr-postgres', 'seaweed': 'ghcr.io/example/quivr-seaweed',
            'tei': 'ghcr.io/example/quivr-tei'})

    def test_accepts_stable_2x_and_alpha_release_tags(self):
        self.assertEqual(release_images.version('v2.0.0'), '2.0.0')
        self.assertEqual(release_images.version('v2.13.4'), '2.13.4')
        self.assertEqual(release_images.version('v2.0.0-alpha.12'), '2.0.0-alpha.12')
        for tag in ['v1.0.0', 'v3.0.0', 'v2.0', 'v2.01.0', 'v2.0.0-beta.1', 'v2.0.0-alpha.01',
                    'v2.0.0-alpha.1-extra', '2.0.0', '../main']:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release_images.version(tag)

    def test_discovers_buildable_plugins_and_refuses_missing_runtime(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            plugin = root / 'plugins' / 'feed'
            plugin.mkdir(parents=True)
            (plugin / 'quivr-plugin.yaml').write_text('id: example.feed\n')
            with self.assertRaisesRegex(ValueError, 'runtime'):
                release_images.inventory(root, 'example/quivr')
            (plugin / 'go.mod').write_text('module example.invalid/feed\n')
            result = release_images.inventory(root, 'example/quivr')
            self.assertEqual(json.loads(json.dumps(result))['include'][1], {
                'plugin': 'feed', 'plugin_id': 'example.feed', 'image': 'ghcr.io/example/quivr-plugin-example.feed',
                'file': 'deploy/images/plugin.Dockerfile', 'target': 'go-plugin',
            })
            (plugin / 'go.mod').unlink()
            (plugin / 'pyproject.toml').write_text('[project]\nname="feed"\n')
            self.assertEqual(release_images.inventory(root, 'example/quivr')['include'][1]['target'], 'python-plugin')
            for folder, target in [('core-ingest', 'core-ingest'), ('hosted-embed', 'hosted-embed')]:
                with self.subTest(folder=folder):
                    runtime = root / 'plugins' / folder
                    runtime.mkdir()
                    (runtime / 'go.mod').write_text('module example.invalid/plugin\n')
                    (runtime / 'quivr-plugin.yaml').write_text('id: example.' + folder + '\n')
                    selected = next(image for image in release_images.inventory(root, 'example/quivr')['include']
                                    if image['plugin'] == folder)
                    self.assertEqual(selected['target'], target)
