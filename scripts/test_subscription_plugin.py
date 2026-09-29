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
        self.assertIn('alerts (alerts@0.2.0)', sp.describe(self.stack))

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



class Described(unittest.TestCase):
    """Described alerts are offered only when the alerts plugin has a classifier (THE-725)."""

    def setUp(self):
        self.name = 'quivr-test-' + uuid.uuid4().hex[:10]
        self.stack = local.Stack(self.name)

    def tearDown(self):
        shutil.rmtree(local.ROOT / '.scratch' / self.name, ignore_errors=True)

    def alerts_pin(self, **kwargs):
        return next(pin for pin in sp.pins(self.stack, **kwargs) if pin['manifest'] == ALERTS_MANIFEST)

    def test_verification_judges_described_alerts_with_the_fake_server(self):
        sp.select(self.stack, True, sp.described_mode(True))
        self.stack.state['fake_system_one_port'] = 18765
        self.assertEqual(self.alerts_pin()['kinds'], ['keywords', 'described'])
        self.assertEqual(sp.classifier_environment(self.stack), {'TYPESAFE_API_KEY': 'test-key', 'TYPESAFE_API_URL': 'http://127.0.0.1:18765/v1/systemone'})

    def test_make_dev_offers_described_alerts_only_with_a_typesafe_key(self):
        with mock.patch.dict(os.environ, {'TYPESAFE_API_KEY': 'k'}):
            self.assertEqual(sp.described_mode(False), 'typesafe')
        with mock.patch.dict(os.environ, {'TYPESAFE_API_KEY': ''}):
            sp.select(self.stack, True, sp.described_mode(False))
        self.assertEqual(self.alerts_pin()['kinds'], ['keywords'])
        # The plugin process must not inherit a key the pin does not offer.
        self.assertEqual(sp.classifier_environment(self.stack), {'TYPESAFE_API_KEY': ''})
        self.assertIn('described alerts off', sp.describe(self.stack))

    def test_an_installation_without_a_key_pins_keywords_only(self):
        sp.select(self.stack, True, 'fake')
        self.assertEqual(self.alerts_pin(described='off')['kinds'], ['keywords'])


if __name__ == '__main__':
    unittest.main()
