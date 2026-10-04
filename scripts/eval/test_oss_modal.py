"""Offline Modal import/layout and CLI failure regressions; never start an app."""
import contextlib
import importlib.util
import io
import json
import os
import pathlib
import queue
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import oss_bakeoff


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
        add_dir, add_file = modal.Image.add_local_dir, modal.Image.add_local_file

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
             mock.patch.object(modal.App, 'run', side_effect=RuntimeError('offline')):
            with self.assertRaises(oss_modal.JobFailed):
                oss_modal.dispatch('qwen3', 'cpu', ['scifact'], 'a' * 40, 1000, 30, False)

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
    try:
        namespace['remote_measure']('qwen3', 'cpu', ['scifact'], 'a' * 40, 1000, 30, False, started)
    except RuntimeError as error:
        assert str(error) == 'offline server'
    else:
        raise AssertionError('server creation was not reached')
assert started.get_nowait() is True
assert public_sets.max_source_bytes() > 0
'''],
                cwd=evaluation, capture_output=True, text=True, timeout=10)
            self.assertEqual(child.returncode, 0, child.stderr)

    def test_remote_failure_and_result_deadline_exit_with_safe_evidence(self):
        import modal
        import oss_modal
        for startup_failure, error in ((False, RuntimeError('private remote input')),
                                      (False, TimeoutError('private remote input')),
                                      (False, modal.exception.FunctionTimeoutError('private remote input')),
                                      (True, RuntimeError('private remote input')),
                                      (True, TimeoutError('private remote input'))):
            with self.subTest(startup=startup_failure, error=type(error).__name__), \
                 tempfile.TemporaryDirectory() as temporary:
                out = pathlib.Path(temporary) / 'campaign'
                call = mock.Mock()
                call.get.side_effect = error
                started = mock.Mock()
                if startup_failure:
                    started.get.side_effect = queue.Empty()
                with mock.patch.dict(os.environ, {'CI': '', 'GITHUB_ACTIONS': ''}), \
                     mock.patch.object(modal.App, 'run', return_value=contextlib.nullcontext()), \
                     mock.patch.object(modal.Queue, 'ephemeral', return_value=contextlib.nullcontext(started)), \
                     mock.patch.object(modal.Function, 'spawn', return_value=call), \
                     mock.patch.object(modal.Function, 'remote', side_effect=AssertionError('unbounded wait')), \
                     mock.patch('sys.stderr', new_callable=io.StringIO) as stderr:
                    code = oss_bakeoff.main(['run', '--out', str(out), '--models', 'qwen3',
                        '--hardware', 'cpu', '--sets', 'scifact', '--timeout', '30', '--acknowledge-cost'])
                self.assertEqual(code, 2)
                evidence = json.loads((out / 'qwen3-cpu-campaign.json').read_text())
                self.assertEqual(evidence['status'], 'failed')
                self.assertIn(type(error).__name__, stderr.getvalue())
                self.assertNotIn('private remote input', stderr.getvalue() + json.dumps(evidence))
                self.assertLessEqual(started.get.call_args.kwargs['timeout'], 60)
                self.assertEqual(call.get.call_args.kwargs['timeout'], 0 if startup_failure else 30)
                if isinstance(error, TimeoutError):
                    call.cancel.assert_called_once_with(terminate_containers=True)


if __name__ == '__main__':
    unittest.main()
