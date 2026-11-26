"""Armada regression: shipping artifacts cannot escape through linked directories."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class PrepareShipRunTests(unittest.TestCase):
    def test_linked_artifact_components_never_write_outside_checkout(self):
        script = Path(__file__).with_name("prepare_ship_run.py")
        for relative in ("plans", "plans/ship-pr-dev", "plans/ship-pr-dev/runs"):
            with self.subTest(path=relative), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp) / "repo"
                outside = Path(tmp) / "outside"
                root.mkdir()
                outside.mkdir()
                subprocess.run(["git", "init", "--quiet"], cwd=root, check=True)
                (root / ".gitignore").write_text("plans/ship-pr-dev/\n")
                linked = root / relative
                linked.parent.mkdir(parents=True, exist_ok=True)
                linked.symlink_to(outside, target_is_directory=True)
                result = subprocess.run(["python3", str(script.resolve()), "--cwd", str(root)], capture_output=True, text=True, env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"))
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(list(outside.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
