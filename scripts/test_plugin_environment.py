"""Local plugin subprocesses inherit ordinary settings and plugin provider bindings."""
import os
import unittest
from unittest import mock

import plugin_environment


class PluginEnvironment(unittest.TestCase):
    def test_engine_secrets_are_filtered_from_child_environment(self):
        plugin_env = {'PROVIDER_KEY': 'provider-setting', 'PROVIDER_KEY_FILE': '/fixture/provider',
                      'PATH': '/usr/bin'}
        engine_env = {
            'QUIVR_ENGINE_PLUGIN_KEYS': 'engine-key-map',
            'QUIVR_ENGINE_PLUGIN_KEYS_FILE': '/fixture/engine-rings',
            'QUIVR_PLUGIN_SIGNING_KEYS': 'other-plugin-ring',
            'quivr_plugin_signing_keys_FILE': '/fixture/other-ring',
            'quivr_engine_plugin_keys': 'engine-alias',
            'quivr_plugin_signing_keys': 'plugin-alias',
            'DATABASE_URL_FILE': '/fixture/database-url',
            'S3_ACCESS_KEY_FILE': '/fixture/access-id',
            'S3_SECRET_KEY_FILE': '/fixture/storage-key',
            'WEAVIATE_API_KEY_FILE': '/fixture/index-key',
            'QUIVR_API_KEYS_FILE': '/fixture/api-keys',
            'QUIVR_API_KEY_FILE': '/fixture/api-key',
            'QUIVR_ADMIN_KEYS': 'admin-keys',
            'QUIVR_ADMIN_KEYS_FILE': '/fixture/admin-keys',
            'QUIVR_ADMIN_KEY': 'admin-key',
            'QUIVR_ADMIN_KEY_FILE': '/fixture/admin-key',
            'QUIVR_DEMO_ADMIN_KEY': 'demo-admin-key',
            'QUIVR_DEMO_ADMIN_KEY_FILE': '/fixture/demo-admin-key',
            'QUIVR_OPERATOR_KEY_FILE': '/fixture/operator-key',
            'QUIVR_QUEUE_KEY_FILE': '/fixture/queue-key',
            'QUIVR_CURSOR_KEY_FILE': '/fixture/cursor-key',
            'QUIVR_CREDENTIAL_KEY_FILE': '/fixture/credential-key',
            'QUIVR_TELEMETRY_HEADERS_FILE': '/fixture/telemetry',
            'QUIVR_LOCAL_WEBHOOK_0_FILE': '/fixture/webhook',
        }
        with mock.patch.dict(os.environ, {**plugin_env, **engine_env}, clear=True):
            self.assertEqual(plugin_environment.inherited(), plugin_env)
