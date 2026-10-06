"""Offline Modal import/layout and CLI failure regressions; never start an app."""
import contextlib
import importlib.util
import json
import pathlib
import queue
import shutil
import subprocess
import sys
import tempfile
import unittest
import types
from unittest import mock



@unittest.skipUnless(importlib.util.find_spec('modal'), 'requires the Modal SDK')
class ModalTransport(unittest.TestCase):
    def test_container_import_and_real_definition_find_measurement_files(self):
        import modal
        source = pathlib.Path(__file__).with_name('oss_modal.py').read_text()
        # The SDK imports this standalone module from /root, not scripts/eval.
        namespace = {'__name__': 'oss_modal', '__file__': '/root/oss_modal.py'}
        exec(compile(source, namespace['__file__'], 'exec'), namespace)

        import oss_modal
        mounts = []
        wheels = []
        commands = []
        add_dir, add_file = modal.Image.add_local_dir, modal.Image.add_local_file
        install = modal.Image.pip_install
        run = modal.Image.run_commands

        def run_commands(image, *args, **kwargs):
            commands.extend(args)
            return run(image, *args, **kwargs)

        def pip_install(image, *packages, **kwargs):
            wheels.append((packages, kwargs.get('index_url')))
            return install(image, *packages, **kwargs)

        def directory(image, local, remote, **kwargs):
            mounts.append((pathlib.Path(local), remote, kwargs.get('ignore', [])))
            return add_dir(image, local, remote, **kwargs)

        def file(image, local, remote, **kwargs):
            mounts.append((pathlib.Path(local), remote, []))
            return add_file(image, local, remote, **kwargs)

        # Only app execution is refused. The SDK builds the real image/function
        # definition and validates its arguments before reaching this boundary.
        with mock.patch.object(modal.Image, 'add_local_dir', directory), \
             mock.patch.object(modal.Image, 'add_local_file', file), \
             mock.patch.object(modal.Image, 'pip_install', pip_install), \
             mock.patch.object(modal.Image, 'run_commands', run_commands), \
             mock.patch.object(modal.App, 'run', side_effect=RuntimeError('offline')):
            with self.assertRaises(oss_modal.JobFailed):
                oss_modal.dispatch('e5-small-reference', 'cpu', ['scifact'], 'a' * 40, 1000, 30, False)
            granite_mounts = list(mounts)
            mounts.clear()
            with self.assertRaises(oss_modal.JobFailed):
                oss_modal.dispatch('embeddinggemma-2', 'L4', ['scifact'], 'a' * 40, 1000, 30, False)
        self.assertEqual(mounts, granite_mounts)
        # CPU E5 and CUDA Gemma share the ST/Transformers-compatible version.
        self.assertIn((('torch==2.8.0',), 'https://download.pytorch.org/whl/cpu'), wheels)
        # EmbeddingGemma2Processor imports torchvision and pillow even for text-only use.
        self.assertIn((('torch==2.8.0', 'torchvision==0.23.0'), 'https://download.pytorch.org/whl/cu126'), wheels)
        self.assertIn((('transformers==5.19.0', 'sentence-transformers==6.1.0', 'pillow==11.3.0'), None), wheels)
        self.assertEqual(commands.count('python -m pip install -r /workspace/scripts/eval/requirements-oss.txt'), 2)
        self.assertEqual(commands.count('HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 python /workspace/scripts/eval/oss_image_check.py'), 2)
        self.assertIn((('transformers==5.19.0', 'sentence-transformers==6.1.0'), None), wheels)

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            for local, remote, ignored in mounts:
                target = root / remote.lstrip('/')
                target.parent.mkdir(parents=True, exist_ok=True)
                if local.is_dir():
                    shutil.copytree(local, target, ignore=shutil.ignore_patterns(*ignored))
                else:
                    shutil.copyfile(local, target)
            # A fresh interpreter sees only the mounted evaluation tree.
            evaluation = root / 'workspace/scripts/eval'
            child = subprocess.run([sys.executable, '-c',
                '''import pathlib, queue
from unittest import mock
import oss_bakeoff, public_sets
namespace = {'__name__': 'oss_modal', '__file__': '/root/oss_modal.py'}
exec(compile(pathlib.Path('oss_modal.py').read_text(), namespace['__file__'], 'exec'), namespace)
started = queue.Queue()
# Execute the real remote entry and measurement setup against the copied files.
# Refuse only process creation, before TEI or any model/data download can start.
with mock.patch.object(oss_bakeoff.subprocess, 'Popen', side_effect=RuntimeError('offline server')):
    output = namespace['remote_measure']('granite-r2', 'cpu', ['scifact'], 'a' * 40, 1000, 30, False, started)
    assert output['campaign']['reason']['kind'] == 'provider_error'
    assert output['campaign']['status'] == 'failed'
assert started.get_nowait() is True
assert public_sets.max_source_bytes() > 0
'''],
                cwd=evaluation, capture_output=True, text=True, timeout=10)
            self.assertEqual(child.returncode, 0, child.stderr)

    def test_tei_stderr_stays_private_through_remote_and_local_transport(self):
        # Real measurement plus SDK dispatch; fake the server, child and cloud.
        import modal
        import oss_modal
        import oss_bakeoff
        process = mock.Mock()
        process.poll.return_value = None
        marker = 'private diagnostic detail'
        def popen(command, **kwargs):
            kwargs['stderr'].write((marker + '\n').encode())
            kwargs['stderr'].flush()
            return process
        def child(command, **kwargs):
            out = pathlib.Path(command[command.index('--out') + 1])
            out.write_text(json.dumps({'set': 'scifact', 'status': 'complete', 'results': {}}))
            process.poll.return_value = 17
            return types.SimpleNamespace(returncode=0)
        with mock.patch.object(oss_bakeoff.subprocess, 'Popen', side_effect=popen), \
             mock.patch.object(oss_bakeoff.subprocess, 'run', side_effect=child), \
             mock.patch.object(oss_bakeoff, 'wait_ready'), \
             mock.patch.object(modal, 'current_function_call_id', return_value='fixture-call'):
            output = oss_modal.remote_measure('granite-r2', 'cpu', ['scifact'], 'a' * 40,
                                              1000, 30, False, mock.Mock())
        call = mock.Mock()
        call.get.return_value = output
        with mock.patch.object(modal.App, 'run', return_value=contextlib.nullcontext()), \
             mock.patch.object(modal.Queue, 'ephemeral', return_value=contextlib.nullcontext(mock.Mock())), \
             mock.patch.object(modal.Function, 'spawn', return_value=call):
            result = oss_modal.dispatch('granite-r2', 'cpu', ['scifact'], 'a' * 40, 1000, 30, False)
        self.assertIn(marker, json.dumps(result['campaign']))
        self.assertNotIn(marker, json.dumps(result['reports']))
        self.assertEqual(result['reports'][0]['serving_campaign']['reason']['exit_code'], 17)

    def test_remote_failure_and_result_deadline_exit_with_safe_evidence(self):
        import modal
        import oss_modal
        missing = ModuleNotFoundError('private remote input', name='sentencepiece')
        wrapped = ModuleNotFoundError("Could not import module 'PreTrainedModel'. private remote input")
        wrapped.__cause__ = RuntimeError('private remote input')
        for startup_failure, error in ((False, missing), (False, wrapped), (False, RuntimeError('private remote input')),
                                      (False, TimeoutError('private remote input')),
                                      (False, modal.exception.FunctionTimeoutError('private remote input')),
                                      (True, RuntimeError('private remote input')),
                                      (True, TimeoutError('private remote input'))):
            with self.subTest(startup=startup_failure, error=type(error).__name__), \
                 tempfile.TemporaryDirectory() as temporary:
                call = mock.Mock()
                call.get.side_effect = error
                started = mock.Mock()
                if startup_failure:
                    started.get.side_effect = queue.Empty()
                with mock.patch.object(modal.App, 'run', return_value=contextlib.nullcontext()), \
                     mock.patch.object(modal.Queue, 'ephemeral', return_value=contextlib.nullcontext(started)), \
                     mock.patch.object(modal.Function, 'spawn', return_value=call), \
                     mock.patch.object(modal.Function, 'remote', side_effect=AssertionError('unbounded wait')):
                    with self.assertRaises(oss_modal.JobFailed) as raised:
                        oss_modal.dispatch('granite-r2', 'cpu', ['scifact'], 'a' * 40, 1000, 30, False)
                evidence = raised.exception.reason
                self.assertEqual(evidence['error_type'], type(error).__name__)
                self.assertEqual(evidence['kind'], 'timeout' if isinstance(error, (TimeoutError, modal.exception.FunctionTimeoutError)) else 'provider_error')
                if error is missing:
                    self.assertEqual(evidence['missing_module'], 'sentencepiece')
                if error is wrapped:
                    self.assertEqual(evidence['import_target'], 'PreTrainedModel')
                    self.assertEqual(evidence['cause_error_type'], 'RuntimeError')
                self.assertNotIn('private remote input', str(raised.exception) + json.dumps(evidence))
                self.assertLessEqual(started.get.call_args.kwargs['timeout'], 60)
                self.assertEqual(call.get.call_args.kwargs['timeout'], 0 if startup_failure else 30)
                if isinstance(error, (TimeoutError, modal.exception.FunctionTimeoutError)):
                    call.cancel.assert_called_once_with(terminate_containers=True)


if __name__ == '__main__':
    unittest.main()
