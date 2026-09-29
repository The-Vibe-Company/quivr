"""Which alert-rule plugins the local harness pins (THE-723). Runs without Docker."""
import os, shutil, unittest, uuid
from unittest import mock

import local
import subscription_plugin as sp

ALERTS_MANIFEST = str(local.ROOT / 'plugins/alerts/quivr-plugin.yaml')


class Selection(unittest.TestCase):
    def setUp(self):
        self.name = 'quivr-test-' + uuid.uuid4().hex[:10]
        self.stack = local.Stack(self.name)

    def tearDown(self):
        shutil.rmtree(local.ROOT / '.scratch' / self.name, ignore_errors=True)

    def manifests(self):
        return [pin['manifest'] for pin in sp.pins(self.stack)]

    def test_keyword_alerts_are_pinned_by_default(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop('QUIVR_ALERTS', None)
            sp.select(self.stack, sp.from_environment())
        self.assertEqual(self.manifests(), [ALERTS_MANIFEST])
        self.assertIn('alerts (alerts@0.1.0)', sp.describe(self.stack))

    def test_quivr_alerts_off_leaves_them_unpinned(self):
        with mock.patch.dict(os.environ, {'QUIVR_ALERTS': 'off'}):
            sp.select(self.stack, sp.from_environment())
        self.assertEqual(self.manifests(), [])
        self.assertIn('none', sp.describe(self.stack))

    def test_an_unknown_value_is_refused(self):
        with mock.patch.dict(os.environ, {'QUIVR_ALERTS': 'maybe'}):
            with self.assertRaises(ValueError):
                sp.from_environment()

    def test_the_scaffolded_template_is_pinned_beside_them(self):
        sp.select(self.stack, True)
        sp.directory(self.stack).mkdir(parents=True)
        sp.manifest(self.stack).write_text('id: alert-rules\n')
        self.assertEqual(self.manifests(), [str(sp.manifest(self.stack)), ALERTS_MANIFEST])
        ports = {pin['endpoint'] for pin in sp.pins(self.stack)}
        self.assertEqual(len(ports), 2, 'each plugin needs its own port')


if __name__ == '__main__':
    unittest.main()
