#!/usr/bin/env python3

import importlib.util
import sys
import unittest
import tempfile
import subprocess
from unittest.mock import patch
from pathlib import Path


sys.dont_write_bytecode = True


SCRIPT = Path(__file__).with_name("collect_ship_context.py")
spec = importlib.util.spec_from_file_location("collect_ship_context", SCRIPT)
collector = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(collector)


class CollectShipContextTests(unittest.TestCase):
    # Armada additions: fail closed on missing bases and inventory all CI files.
    def test_unknown_or_stale_base_requires_explicit_ref(self):
        with patch.object(collector, "git", return_value=subprocess.CompletedProcess([], 1, "", "")):
            with self.assertRaisesRegex(SystemExit, "--base"):
                collector.detect_base(Path("/synthetic"))
        with patch.object(collector, "git", return_value=subprocess.CompletedProcess([], 1, "", "bad ref")):
            with self.assertRaisesRegex(SystemExit, "bad ref"):
                collector.required_git(Path("/synthetic"), ["diff", "missing...HEAD"])

    def test_circleci_inventory_includes_its_config(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".circleci").mkdir()
            (root / ".circleci/config.yml").write_text("version: 2.1\n")
            self.assertEqual(collector.find_project_files(root)[1], [".circleci/config.yml"])

    def test_agent_skill_paths_do_not_trigger_application_risk(self):
        impact = collector.classify_impact(
            [
                ".agents/skills/review-code-dev/scripts/worker.py",
                ".agents/skills/review-code-dev/references/reviewers/security.md",
                ".claude/skills/ship-pr-dev",
            ]
        )
        self.assertTrue(impact["agent_workflow"])
        self.assertFalse(impact["backend"])
        self.assertFalse(impact["security_or_privacy"])

    def test_application_paths_still_trigger_risk(self):
        impact = collector.classify_impact(["apps/api/auth/token.py"])
        self.assertFalse(impact["agent_workflow"])
        self.assertTrue(impact["backend"])
        self.assertTrue(impact["security_or_privacy"])

    def test_merge_keeps_uncommitted_and_untracked_paths(self):
        merged = collector.merge_changed_files(
            [{"status": "M", "path": "src/committed.py"}],
            [{"status": "M", "path": "src/local.py"}],
            [{"status": "??", "path": "src/new.py"}],
        )
        self.assertEqual(
            ["src/committed.py", "src/local.py", "src/new.py"],
            [item["path"] for item in merged],
        )

    def test_merge_combines_status_without_duplicate_path(self):
        merged = collector.merge_changed_files(
            [{"status": "M", "path": "src/shared.py"}],
            [{"status": "A", "path": "src/shared.py"}],
            [{"status": "M", "path": "src/shared.py"}],
        )
        self.assertEqual([{"status": "M+A", "path": "src/shared.py"}], merged)

    def test_untracked_status_keeps_collapsed_directory(self):
        parsed = collector.parse_untracked_status("?? infra/\n?? new.txt\n M tracked.py\n")
        self.assertEqual(
            [{"status": "??", "path": "infra/"}, {"status": "??", "path": "new.txt"}],
            parsed,
        )


if __name__ == "__main__":
    unittest.main()
