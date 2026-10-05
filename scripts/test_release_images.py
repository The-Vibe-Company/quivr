"""Release inventory owns tag validation and first-party image selection."""
import json
import pathlib
import tempfile
import unittest

import release_images


class ReleaseImagesTests(unittest.TestCase):
    def test_accepts_only_the_alpha_release_line(self):
        self.assertEqual(release_images.version('v2.0.0-alpha.12'), '2.0.0-alpha.12')
        for tag in ['v1.0.0', 'v2.0.0', 'v2.0.0-alpha.01', 'v2.0.0-alpha.1-extra', '../main']:
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
                'plugin': 'feed', 'image': 'ghcr.io/example/quivr-plugin-example.feed',
                'file': 'deploy/images/plugin.Dockerfile', 'target': 'go-plugin',
            })
            (plugin / 'go.mod').unlink()
            (plugin / 'pyproject.toml').write_text('[project]\nname="feed"\n')
            self.assertEqual(release_images.inventory(root, 'example/quivr')['include'][1]['target'], 'python-plugin')
