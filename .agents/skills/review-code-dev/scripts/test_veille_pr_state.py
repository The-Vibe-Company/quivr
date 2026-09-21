#!/usr/bin/env python3

import argparse
import importlib.util
import os
import sys
import tempfile
import unittest
from pathlib import Path


sys.dont_write_bytecode = True


SCRIPT = Path(__file__).with_name("veille_pr_state.py")
spec = importlib.util.spec_from_file_location("veille_pr_state", SCRIPT)
ledger = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(ledger)


class LedgerTests(unittest.TestCase):
    def test_default_uses_configured_or_current_home(self):
        expected_home = Path(os.environ.get("HERMES_HOME", str(Path.home() / ".hermes")))
        self.assertEqual(expected_home / "state/veille-pr.json", ledger.DEFAULT_STATE)

    def test_claim_is_deduplicated_and_state_is_private(self):
        with tempfile.TemporaryDirectory() as temp:
            state_path = Path(temp) / "state.json"
            init_args = argparse.Namespace(state=str(state_path), input="-", replace=False)
            original = ledger.read_candidates
            ledger.read_candidates = lambda _: []
            try:
                self.assertTrue(ledger.cmd_init(init_args)["initialized"])
            finally:
                ledger.read_candidates = original

            args = argparse.Namespace(
                state=str(state_path), repo="The-Vibe-Company/demo", number=2,
                url="https://github.com/The-Vibe-Company/demo/pull/2", run_id="test",
            )
            self.assertTrue(ledger.cmd_claim(args)["claimed"])
            self.assertEqual("already_in_progress", ledger.cmd_claim(args)["reason"])
            self.assertEqual(0o600, state_path.stat().st_mode & 0o777)

    def test_malformed_state_fails_closed(self):
        with tempfile.TemporaryDirectory() as temp:
            state_path = Path(temp) / "state.json"
            state_path.write_text('{"schema_version": 2}', encoding="utf-8")
            with self.assertRaises(ValueError):
                with ledger.exclusive_state(state_path):
                    pass


if __name__ == "__main__":
    unittest.main()
