"""Offline campaign cost, serving lifecycle and local-only dispatch contracts."""
import json
import pathlib
import tempfile
import unittest
from unittest import mock

import oss_bakeoff


class Campaign(unittest.TestCase):
    def test_dry_run_needs_no_modal_and_accounts_both_resource_types(self):
        # Owner boundary: an operator gets a finite resource estimate before dispatch.
        with tempfile.TemporaryDirectory() as directory:
            out = pathlib.Path(directory) / 'plan.json'
            with mock.patch.dict('sys.modules', {'modal': None}):
                code = oss_bakeoff.main(['plan', '--out', str(out), '--models', 'qwen3',
                                         '--hardware', 'cpu', 'L4', '--timeout', '600'])
            plan = json.loads(out.read_text())
            self.assertEqual(code, 0)
            self.assertEqual(len(plan['jobs']), 2)
            self.assertAlmostEqual(plan['jobs'][0]['hourly_usd'], .252576)
            self.assertAlmostEqual(plan['jobs'][1]['hourly_usd'], 1.051776)
            self.assertAlmostEqual(plan['estimated_compute_usd'], .217392)
            self.assertEqual(len(plan['sets']), 10)
            self.assertFalse(plan['include_restricted'])
            with self.assertRaises(FileExistsError):
                oss_bakeoff.main(['plan', '--out', str(out)])

    def test_server_cleanup_after_campaign_failure(self):
        process = mock.Mock()
        with mock.patch.object(oss_bakeoff.subprocess, 'Popen', return_value=process), \
             mock.patch.object(oss_bakeoff, 'wait_ready'), \
             mock.patch.object(oss_bakeoff.subprocess, 'run', side_effect=RuntimeError('fixture')):
            with self.assertRaisesRegex(RuntimeError, 'fixture'):
                oss_bakeoff.measure('qwen3', 'cpu', ['scifact'], 'a' * 40, 1000, 600, False)
        process.terminate.assert_called_once()
        process.wait.assert_called_once()

    def test_ci_cannot_dispatch_even_with_acknowledgment(self):
        with mock.patch.dict('os.environ', {'CI': 'true'}):
            with self.assertRaisesRegex(SystemExit, 'CI'):
                oss_bakeoff.main(['run', '--out', 'unused', '--acknowledge-cost'])


if __name__ == '__main__':
    unittest.main()
