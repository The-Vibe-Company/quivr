"""Local plugin subprocesses must not inherit another plugin's signing keys."""
import os
import unittest
from unittest import mock

import plugin_environment


class PluginEnvironment(unittest.TestCase):
    def test_signing_keys_are_scoped_to_the_launched_plugin(self):
        parent = {'QUIVR_ENGINE_PLUGIN_KEYS': 'engine-key-map',
                  'QUIVR_PLUGIN_SIGNING_KEYS': 'other-plugin-ring',
                  'quivr_engine_plugin_keys': 'engine-alias',
                  'quivr_plugin_signing_keys': 'plugin-alias',
                  'PROVIDER_KEY': 'provider-setting', 'PATH': '/usr/bin'}
        with mock.patch.dict(os.environ, parent, clear=True):
            inherited = plugin_environment.inherited()
            self.assertNotIn('QUIVR_PLUGIN_SIGNING_KEYS', inherited)
            self.assertNotIn('quivr_plugin_signing_keys', inherited)
            self.assertNotIn('quivr_engine_plugin_keys', inherited)
            child = {**inherited,
                     'QUIVR_PLUGIN_SIGNING_KEYS': 'own-plugin-ring'}
        self.assertNotIn('QUIVR_ENGINE_PLUGIN_KEYS', child)
        self.assertEqual(child['QUIVR_PLUGIN_SIGNING_KEYS'], 'own-plugin-ring')
        self.assertEqual(child['PROVIDER_KEY'], 'provider-setting')
        self.assertEqual(child['PATH'], '/usr/bin')
