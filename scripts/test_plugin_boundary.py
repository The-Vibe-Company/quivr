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
            "deploy/plugins/go.mod": "module example.com/multicall\n\nrequire github.com/The-Vibe-Company/quivr v0.0.0\n",
        })
        result = self.run_check(root)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn(f"plugins/feeds/main.go: imports {INTERNAL}/content", result.stderr)
        self.assertIn(f"sdks/go/x/x.go: imports {INTERNAL}/plugins", result.stderr)
        self.assertIn("plugins/feeds/go.mod: requires or replaces the engine module", result.stderr)
        self.assertIn("deploy/plugins/go.mod: requires or replaces the engine module", result.stderr)

    def test_public_imports_comments_and_the_sdk_module_pass(self):
        root = self.tree({
            "plugins/feeds/main.go": (
                "package main\n\n// Never import \"" + INTERNAL + "/content\" here.\n"
                'import (\n\t"fmt"\n\t"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"\n)\n'
                f'const snippet = `package sample; import "{INTERNAL}/foo"\nimport "{INTERNAL}/bar"`\n'
                f'var πimport = "{INTERNAL}/not_an_import"\n'
            ),
            "plugins/feeds/go.mod": "module example.com/feeds\n\nrequire github.com/The-Vibe-Company/quivr/sdks/go v0.0.0\n\nreplace github.com/The-Vibe-Company/quivr/sdks/go => ../../sdks/go\n",
        })
        result = self.run_check(root)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_semicolon_backtick_quoted_replace_rhs_and_gowork_are_caught(self):
        root = self.tree({
            "go.mod": "module github.com/The-Vibe-Company/quivr\n",
            "plugins/semi/main.go": f'package main; import "{INTERNAL}/auth"\n',
            "plugins/raw/main.go": f"package main\n\nimport `{INTERNAL}/search`\n",
            "plugins/rawblock/main.go": f"package main\n\nimport (\n\t_ `{INTERNAL}/ingest`\n)\n",
            "plugins/quoted/go.mod": 'module example.com/quoted\n\nrequire "github.com/The-Vibe-Company/quivr" v0.0.0\n',
            "plugins/rhs/go.mod": 'module example.com/rhs\n\nreplace example.com/alias => "github.com/The-Vibe-Company/quivr" v0.0.0\n',
            "plugins/rhsdir/go.mod": "module example.com/rhsdir\n\nreplace example.com/alias => ../..\n",
            "plugins/space dir/go.mod": 'module example.com/spacedir\n\nreplace example.com/alias => "../../"\n',
            "plugins/work/go.work": "go 1.24\n\nuse (\n\t../..\n)\n",
        })
        # Also test a quoted replace path that itself contains spaces pointing to the engine root.
        spaced_engine = root / "engine dir"
        spaced_engine.mkdir()
        (spaced_engine / "go.mod").write_text("module github.com/The-Vibe-Company/quivr\n")
        (root / "plugins/spacepath").mkdir(parents=True)
        (root / "plugins/spacepath/go.mod").write_text('module example.com/sp\n\nreplace example.com/alias => "../../engine dir"\n')
        result = self.run_check(root)
        self.assertEqual(result.returncode, 1, result.stdout)
        for expected in (
            f"plugins/semi/main.go: imports {INTERNAL}/auth",
            f"plugins/raw/main.go: imports {INTERNAL}/search",
            f"plugins/rawblock/main.go: imports {INTERNAL}/ingest",
            "plugins/quoted/go.mod: requires or replaces the engine module",
            "plugins/rhs/go.mod: requires or replaces the engine module",
            "plugins/rhsdir/go.mod: requires or replaces the engine module",
            "plugins/space dir/go.mod: requires or replaces the engine module",
            "plugins/spacepath/go.mod: requires or replaces the engine module",
            "plugins/work/go.work: uses or replaces the engine module",
        ):
            self.assertIn(expected, result.stderr)


if __name__ == "__main__":
    unittest.main()
