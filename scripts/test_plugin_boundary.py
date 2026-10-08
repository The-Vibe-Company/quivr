import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "plugin_boundary.py"
INTERNAL = "github.com/The-Vibe-Company/quivr/internal"


class PluginBoundaryTest(unittest.TestCase):
    def tree(self, files: dict[str, str]) -> Path:
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root, True)
        for name, text in files.items():
            (root / name).parent.mkdir(parents=True, exist_ok=True)
            (root / name).write_text(text)
        return root

    def run_check(self, root: Path) -> subprocess.CompletedProcess:
        return subprocess.run([sys.executable, str(SCRIPT), str(root)], capture_output=True, text=True)

    def test_a_deliberate_internal_import_fails(self):
        root = self.tree({
            "plugins/feeds/main.go": f'package main\n\nimport (\n\t"fmt"\n\tcontent "{INTERNAL}/content"\n)\n',
            "sdks/go/x/x.go": f'//go:build linux\n\npackage x\n\nimport _ "{INTERNAL}/plugins"\n',
            "plugins/feeds/go.mod": "module example.com/feeds\n\nreplace github.com/The-Vibe-Company/quivr => ../..\n",
        })
        result = self.run_check(root)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn(f"plugins/feeds/main.go: imports {INTERNAL}/content", result.stderr)
        self.assertIn(f"sdks/go/x/x.go: imports {INTERNAL}/plugins", result.stderr)
        self.assertIn("plugins/feeds/go.mod: requires or replaces the engine module", result.stderr)

    def test_public_imports_comments_and_the_sdk_module_pass(self):
        root = self.tree({
            "plugins/feeds/main.go": (
                "package main\n\n// Never import \"" + INTERNAL + "/content\" here.\n"
                'import (\n\t"fmt"\n\t"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"\n)\n'
            ),
            "plugins/feeds/go.mod": "module example.com/feeds\n\nrequire github.com/The-Vibe-Company/quivr/sdks/go v0.0.0\n\nreplace github.com/The-Vibe-Company/quivr/sdks/go => ../../sdks/go\n",
        })
        result = self.run_check(root)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
