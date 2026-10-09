"""Only a failed download may be attempted once more; tests never enter this wrapper."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class Fetch(unittest.TestCase):
    def test_cli_retries_network_failures_once_and_refuses_tests(self):
        script = Path(__file__).with_name('ci_fetch.py')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            npm = root / 'npm'
            npm.write_text('#!/usr/bin/env python3\nimport os,pathlib,sys\np=pathlib.Path(os.environ["COUNT"])\nn=int(p.read_text())+1 if p.exists() else 1\np.write_text(str(n))\nassert os.environ["PIP_RETRIES"] == "0" and os.environ["npm_config_fetch_retries"] == "0"\nprint(os.environ["ERROR"])\nsys.exit(1)\n')
            npm.chmod(0o755)
            count = root / 'count'
            for error, expected in [('npm ERR! code ECONNRESET', '2'), ('package integrity mismatch', '1'), ('fixture.go:8: undefined: ECONNRESET', '1'), ('parser: unexpected EOF', '1'), ('Get https://example.invalid/module: unexpected EOF', '2'), ('Get "https://example.invalid/module": unexpected EOF', '2')]:
                count.unlink(missing_ok=True)
                result = subprocess.run([sys.executable, str(script), '--', 'npm', 'ci'],
                                        env={**os.environ, 'PATH':str(root)+os.pathsep+os.environ['PATH'], 'COUNT':str(count), 'ERROR':error}, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(count.read_text(), expected)
            count.unlink()
            result = subprocess.run([sys.executable, str(script), '--', 'npm', 'test'],
                                    env={**os.environ, 'PATH':str(root)+os.pathsep+os.environ['PATH'], 'COUNT':str(count), 'ERROR':'ECONNRESET'}, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(count.exists())


if __name__ == '__main__':
    unittest.main()
