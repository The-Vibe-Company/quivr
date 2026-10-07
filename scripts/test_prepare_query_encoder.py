"""Owner-boundary tests for pinned query-encoder assets."""
import hashlib
import json
import os
import pathlib
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

from deploy.cpu import text_encoder
from scripts import prepare_query_encoder


class QueryEncoderAssetsTest(unittest.TestCase):
    def lock(self, root, files):
        lock = {
            'model': 'fixture/model',
            'model_revision': 'fixture-revision',
            'source_revision': 'fixture-revision-full',
            'dimensions': 768,
            'files': files,
        }
        path = root / 'model-lock.json'
        path.write_text(json.dumps(lock))
        return path

    def test_verify_only_never_downloads_and_reports_corrupt_asset(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            asset = root / 'model' / 'config.json'
            asset.parent.mkdir()
            asset.write_bytes(b'corrupt')
            lock = self.lock(root, {'config.json': {
                'sha256': hashlib.sha256(b'expected').hexdigest(), 'size': 7}})
            with patch.object(prepare_query_encoder.urllib.request, 'urlopen',
                              side_effect=AssertionError('verify-only downloaded')):
                with self.assertRaisesRegex(RuntimeError, 'checksum mismatch.*config.json'):
                    prepare_query_encoder.prepare(root / 'model', verify_only=True,
                                                  lock_path=lock)

    def test_fresh_preparation_leaves_downloaded_asset_readable(self):
        payload = b'fixture config\n'

        class Response:
            def __init__(self):
                self.remaining = payload

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read1(self, size):
                chunk, self.remaining = self.remaining[:size], self.remaining[size:]
                return chunk

        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            lock = self.lock(root, {'config.json': {
                'sha256': hashlib.sha256(payload).hexdigest(), 'size': len(payload)}})
            with patch.object(prepare_query_encoder.urllib.request, 'urlopen',
                              return_value=Response()):
                prepare_query_encoder.prepare(root / 'model', lock_path=lock)
            asset = root / 'model' / 'config.json'
            self.assertEqual(asset.stat().st_mode & 0o777, 0o644)
            self.assertEqual(asset.read_bytes(), payload)

    def test_missing_asset_fails_before_any_model_loader_can_use_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            lock = self.lock(root, {'model.safetensors': {
                'sha256': hashlib.sha256(b'weights').hexdigest(), 'size': 7}})
            with self.assertRaisesRegex(RuntimeError, 'missing pinned model asset.*model.safetensors'):
                prepare_query_encoder.verify_model(root / 'model', lock_path=lock)

    def test_runtime_verification_rejects_missing_asset_before_model_construction(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            lock = self.lock(root, {'model.safetensors': {
                'sha256': hashlib.sha256(b'weights').hexdigest(), 'size': 7}})
            constructed = []

            class FakeTorch:
                __version__ = 'fixture'
                float32 = object()

                @staticmethod
                def set_num_threads(_threads):
                    raise AssertionError('model dependencies loaded before asset verification')

                @staticmethod
                def set_num_interop_threads(_threads):
                    raise AssertionError('model dependencies loaded before asset verification')

            fake_sentence_transformers = types.SimpleNamespace(
                SentenceTransformer=lambda *args, **kwargs: constructed.append((args, kwargs)))
            with patch.dict(os.environ, {}, clear=False), patch.dict(sys.modules, {
                    'torch': FakeTorch, 'sentence_transformers': fake_sentence_transformers}):
                with self.assertRaisesRegex(RuntimeError, 'missing pinned model asset'):
                    text_encoder.load_model(root / 'model', lock_path=lock)
            self.assertEqual(constructed, [])

    def test_checked_in_lock_has_external_revision_hashes_and_sizes(self):
        lock = json.loads(prepare_query_encoder.LOCK_PATH.read_text())
        self.assertEqual(lock['model'], 'google/embeddinggemma-2')
        self.assertEqual(lock['source_revision'],
                         '914f7f89142e33e77833254d9c9b90c3cef7303b')
        self.assertEqual(lock['files']['model.safetensors'], {
            'sha256': '197a32965d4b1105faf060417baa899e193fb73cd401f42ec9295234d5553d79',
            'size': 1488915288,
        })
        self.assertEqual(lock['files']['tokenizer.json'], {
            'sha256': '4d777ef5bdc1aa36227abdfb77c3e49e7b9c892d16e1b6bda41c393504828be4',
            'size': 32170510,
        })


if __name__ == '__main__':
    unittest.main()
