"""Local signing keys survive restarts and remain private on disk."""
import base64
import json
from pathlib import Path
import stat
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

import push_plugin


class LocalSigningKeys(unittest.TestCase):
    def test_start_provisions_private_keys_and_reuses_them_for_the_engine(self):
        with tempfile.TemporaryDirectory() as directory:
            stack = SimpleNamespace(directory=Path(directory), source=Path(__file__).resolve().parents[1],
                                    state={'push_source_port': 9901}, save=lambda: None)
            with mock.patch.object(push_plugin, 'stop'), \
                 mock.patch.object(push_plugin, 'healthy', return_value=True), \
                 mock.patch.object(push_plugin.normalizer_plugin, 'python', return_value=sys.executable), \
                 mock.patch.object(push_plugin.subprocess, 'Popen', return_value=SimpleNamespace(pid=12345)):
                push_plugin.start(stack)
                keys = stack.directory / 'push-source-signing.json'
                first = keys.read_bytes()
                self.assertEqual(stat.S_IMODE(keys.stat().st_mode), 0o600)
                ring = json.loads(first)
                active = next(key for key in ring['keys'] if key['id'] == ring['active'])
                self.assertGreaterEqual(len(base64.urlsafe_b64decode(active['secret'] + '=' * (-len(active['secret']) % 4))), 32)
                engine = json.loads(push_plugin.engine_environment(stack)['QUIVR_ENGINE_PLUGIN_KEYS'])
                self.assertEqual(engine['push-source'], ring)
                push_plugin.start(stack)
                self.assertEqual(keys.read_bytes(), first)
