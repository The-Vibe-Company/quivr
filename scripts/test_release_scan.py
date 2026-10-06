"""Owner checks for selecting a completed release and accepting pinned scan targets."""
import unittest

from release_scan import image_matrix, latest_release


class ReleaseScanTest(unittest.TestCase):
    def test_newest_completed_alpha_including_prereleases(self):
        releases = [
            {'tag_name': 'v2.0.0-alpha.9', 'assets': [{'name': 'images.txt'}]},
            {'tag_name': 'v2.0.0-alpha.11', 'assets': []},
            {'tag_name': 'v2.0.0-alpha.10', 'assets': [{'name': 'images.txt'}], 'prerelease': True},
            {'tag_name': 'v2.0.0-alpha.12', 'assets': [{'name': 'images.txt'}], 'draft': True},
            {'tag_name': 'v1.0.0', 'assets': [{'name': 'images.txt'}]},
        ]
        self.assertEqual('v2.0.0-alpha.10', latest_release(releases))
        self.assertEqual('', latest_release([{'tag_name': 'v2.0.0-alpha.1', 'assets': []}]))

    def test_scan_targets_require_owned_unique_immutable_digests(self):
        digest = 'a' * 64
        engine = f'ghcr.io/the-vibe-company/quivr@sha256:{digest}'
        plugin = f'ghcr.io/the-vibe-company/quivr-plugin-core.ingest@sha256:{digest}'
        self.assertEqual({'include': [{'image': engine, 'name': 'quivr'},
                                     {'image': plugin, 'name': 'quivr-plugin-core.ingest'}]},
                         image_matrix(engine + '\n' + plugin + '\n', 'The-Vibe-Company/quivr'))
        for text in ['', engine + '\n' + engine, engine.replace(digest, 'abc'),
                     engine.replace('the-vibe-company', 'other'), engine.replace('@sha256:' + digest, ':latest-alpha')]:
            with self.subTest(text=text), self.assertRaises(ValueError):
                image_matrix(text, 'The-Vibe-Company/quivr')
