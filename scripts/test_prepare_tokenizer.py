"""Pinned preparation owns downloads, cache integrity and the emitted settings."""
import contextlib
import hashlib
import io
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

import prepare_tokenizer


class PrepareTokenizerTests(unittest.TestCase):
    def test_hosted_pins_download_only_requested_model_and_reuse_verified_cache(self):
        data = b'{"model": "external tokenizer"}'
        digest = hashlib.sha256(data).hexdigest()
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(prepare_tokenizer, 'ROOT', pathlib.Path(tmp)):
            root = pathlib.Path(tmp)
            python = root / '.scratch/tokenizer/venv/bin/python'
            python.parent.mkdir(parents=True)
            python.touch()
            with mock.patch('subprocess.run', return_value=subprocess.CompletedProcess([], 0)), \
                    mock.patch('urllib.request.urlopen', return_value=io.BytesIO(data)) as download:
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    prepare_tokenizer.main(['--hosted', '--repository', 'example/model', '--revision', 'a' * 40,
                                            '--sha256', digest])
                config = json.loads(output.getvalue())
                self.assertEqual(config, {'python': str(python),
                                         'model': str(root / '.scratch/tokenizer' / (digest + '.json')),
                                         'sha256': digest})
                self.assertEqual(pathlib.Path(config['model']).read_bytes(), data)
                self.assertFalse((root / '.scratch/tokenizer/tokenizer.json').exists())
                download.assert_called_once_with('https://huggingface.co/example/model/resolve/' + 'a' * 40 + '/tokenizer.json', timeout=60)
            with mock.patch('subprocess.run', return_value=subprocess.CompletedProcess([], 0)), \
                    mock.patch('urllib.request.urlopen', side_effect=AssertionError('cache must be offline')):
                prepare_tokenizer.prepare_hosted('example/model', 'a' * 40, digest)
                prepare_tokenizer.prepare_hosted('model', 'a' * 40, digest)

    def test_bad_download_never_replaces_output_and_invalid_pins_fail_before_install(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(prepare_tokenizer, 'ROOT', pathlib.Path(tmp)):
            target = pathlib.Path(tmp) / 'custom/tokenizer.json'
            target.parent.mkdir()
            target.write_bytes(b'existing tokenizer')
            python = pathlib.Path(tmp) / '.scratch/tokenizer/venv/bin/python'
            python.parent.mkdir(parents=True)
            python.touch()
            with mock.patch('subprocess.run', return_value=subprocess.CompletedProcess([], 0)), \
                    mock.patch('urllib.request.urlopen', return_value=io.BytesIO(b'wrong bytes')):
                with self.assertRaisesRegex(RuntimeError, 'checksum'):
                    prepare_tokenizer.prepare_hosted('example/model', 'a' * 40, 'b' * 64, target)
            self.assertEqual(target.read_bytes(), b'existing tokenizer')
            with mock.patch('subprocess.run', side_effect=AssertionError('invalid pins must not install')):
                for repository, revision, digest in [('https://example.org/model', 'a' * 40, 'b' * 64),
                                                      ('../model', 'a' * 40, 'b' * 64),
                                                      ('example/model', 'main', 'b' * 64),
                                                      ('example/model', 'a' * 40, 'bad')]:
                    with self.subTest(repository=repository, revision=revision, digest=digest), self.assertRaises(ValueError):
                        prepare_tokenizer.prepare_hosted(repository, revision, digest)
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                prepare_tokenizer.main(['--hosted'])

    def test_no_arguments_emit_original_core_bytes(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(prepare_tokenizer, 'ROOT', pathlib.Path(tmp)):
            root = pathlib.Path(tmp)
            python = root / '.scratch/tokenizer/venv/bin/python'
            python.parent.mkdir(parents=True)
            python.touch()
            data = b'core tokenizer'
            profile = {**prepare_tokenizer.PROFILE, 'tokenizer_sha256': hashlib.sha256(data).hexdigest()}
            with mock.patch.object(prepare_tokenizer, 'PROFILE', profile), \
                    mock.patch('subprocess.run', return_value=subprocess.CompletedProcess([], 0)), \
                    mock.patch('urllib.request.urlopen', return_value=io.BytesIO(data)):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    prepare_tokenizer.main([])
            expected = '{"python": "' + str(python) + '", "model": "' + str(root / '.scratch/tokenizer/tokenizer.json') + '"}\n'
            self.assertEqual(output.getvalue(), expected)
