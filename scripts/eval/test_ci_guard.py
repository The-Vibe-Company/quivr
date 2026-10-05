"""CI admission owner: environment values and executable work boundaries."""
import contextlib
import io
import os
import unittest
from unittest import mock

import ci_guard
import engine_confirmation as confirmation


class WorkReached(BaseException):
    """Stop before any paid work, even inside broad dependency catches."""


class Admission(unittest.TestCase):
    def test_environment_flags_share_one_false_value_policy(self):
        for flags, expected in (
            ({}, False), ({'CI': ''}, False), ({'CI': '0'}, False),
            ({'CI': 'false'}, False), ({'CI': 'FaLsE'}, False),
            ({'GITHUB_ACTIONS': ''}, False), ({'GITHUB_ACTIONS': '0'}, False),
            ({'GITHUB_ACTIONS': 'false'}, False), ({'GITHUB_ACTIONS': 'FALSE'}, False),
            ({'CI': 'true'}, True), ({'CI': 'TRUE'}, True), ({'CI': '1'}, True),
            ({'GITHUB_ACTIONS': 'true'}, True), ({'GITHUB_ACTIONS': '1'}, True),
            ({'CI': 'false', 'GITHUB_ACTIONS': 'true'}, True),
            ({'CI': 'true', 'GITHUB_ACTIONS': 'false'}, True),
        ):
            with self.subTest(flags=flags), mock.patch.dict(os.environ, flags, clear=True):
                self.assertEqual(ci_guard.in_ci(), expected)

    def test_confirmation_cli_and_adapter_refuse_ci_before_work(self):
        for flags, blocked in (({'CI': 'false', 'GITHUB_ACTIONS': ''}, False),
                               ({'CI': '', 'GITHUB_ACTIONS': 'true'}, True),
                               ({'CI': 'true', 'GITHUB_ACTIONS': ''}, True)):
            for adapter in (False, True):
                with self.subTest(flags=flags, adapter=adapter), \
                     mock.patch.dict(os.environ, {**flags, 'EVAL_CONTROL_DATABASE_URL': 'fixture'}, clear=True), \
                     contextlib.redirect_stderr(io.StringIO()) as diagnostic:
                    if adapter:
                        boundary = mock.patch.object(confirmation.subprocess, 'check_output', side_effect=WorkReached)
                        invoke = lambda: confirmation.ModalAdapter(None, 'unused')({}, None)
                    else:
                        boundary = mock.patch.object(confirmation.control_store, 'Store', side_effect=WorkReached)
                        invoke = lambda: confirmation.main(['--allow-paid', '--campaign', 'example', '--trial', '0',
                            '--candidate', 'unused.json', '--configuration', 'unused.json'])
                    with boundary:
                        expected = (PermissionError if adapter else SystemExit) if blocked else WorkReached
                        with self.assertRaises(expected) as refusal:
                            invoke()
                        if blocked:
                            self.assertIn('CI', diagnostic.getvalue() + str(refusal.exception))


if __name__ == '__main__':
    unittest.main()
