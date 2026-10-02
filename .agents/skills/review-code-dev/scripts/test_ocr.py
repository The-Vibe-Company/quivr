"""Bootstrap trust boundary and portable dispatch tests; no network required."""
import hashlib
import importlib.util
import io
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("review_ocr", Path(__file__).with_name("ocr.py"))
ocr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ocr)


class BootstrapTests(unittest.TestCase):
    # Armada addition: doctor must never install or execute OCR.
    def test_check_is_read_only_and_verifies_cached_content(self):
        data = b"synthetic binary"
        with tempfile.TemporaryDirectory() as tmp, \
             patch.dict(os.environ, {"REVIEW_CODE_OCR_HOME": tmp}), \
             patch.object(ocr.platform, "system", return_value="Linux"), \
             patch.object(ocr.platform, "machine", return_value="x86_64"), \
             patch.dict(ocr.CHECKSUMS, {"linux-amd64": hashlib.sha256(data).hexdigest()}), \
             patch.object(ocr.urllib.request, "urlopen") as download, \
             patch.object(ocr.subprocess, "run") as execute, \
             patch("sys.stdout", new_callable=io.StringIO):
            self.assertEqual(ocr.main(["check"]), 2)
            self.assertEqual(list(Path(tmp).iterdir()), [])
            binary = Path(tmp) / ocr.VERSION / "linux-amd64" / "ocr"
            binary.parent.mkdir(parents=True)
            binary.write_bytes(data)
            binary.chmod(0o755)
            self.assertEqual(ocr.main(["check"]), 0)
            binary.write_bytes(b"corrupt")
            with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
                ocr.main(["check"])
            download.assert_not_called()
            execute.assert_not_called()

    def test_platforms(self):
        for system, machine, expected in [
            ("Darwin", "arm64", "darwin-arm64"), ("Darwin", "x86_64", "darwin-amd64"),
            ("Linux", "aarch64", "linux-arm64"), ("Linux", "x86_64", "linux-amd64"),
        ]:
            self.assertEqual(ocr.target(system, machine), expected)
        with self.assertRaises(RuntimeError):
            ocr.target("Linux", "riscv64")

    def test_install_cache_and_corruption(self):
        data = b"test executable fixture"
        with tempfile.TemporaryDirectory() as tmp, \
             patch.dict(os.environ, {"REVIEW_CODE_OCR_HOME": tmp}), \
             patch.object(ocr.platform, "system", return_value="Linux"), \
             patch.object(ocr.platform, "machine", return_value="aarch64"), \
             patch.dict(ocr.CHECKSUMS, {"linux-arm64": hashlib.sha256(data).hexdigest()}), \
             patch.object(ocr.urllib.request, "urlopen", return_value=io.BytesIO(data)) as download:
            binary = ocr.ensure_binary()
            self.assertEqual(binary.read_bytes(), data)
            self.assertTrue(os.access(binary, os.X_OK))
            self.assertEqual(ocr.ensure_binary(), binary)
            download.assert_called_once()
            binary.write_bytes(b"corrupted")
            with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
                ocr.ensure_binary()

    def test_bad_download_never_installed(self):
        with tempfile.TemporaryDirectory() as tmp, \
             patch.dict(os.environ, {"REVIEW_CODE_OCR_HOME": tmp}), \
             patch.object(ocr.urllib.request, "urlopen", return_value=io.BytesIO(b"bad")):
            with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
                ocr.ensure_binary()
            self.assertEqual(list(Path(tmp).rglob("ocr")), [])
            self.assertEqual(list(Path(tmp).rglob(".download-*")), [])

    def test_reject_llm_and_config_commands_before_install(self):
        with patch.object(ocr, "ensure_binary") as install:
            for command in (["review"], ["config", "provider"], ["delegate", "exec"]):
                with self.assertRaises(RuntimeError):
                    ocr.main(command)
            install.assert_not_called()

    def test_arguments_are_not_shell_code_and_status_propagates(self):
        args = ["delegate", "rule", "--format", "json", "--", "src/$(echo bad) file.ts"]
        with patch.object(ocr, "ensure_binary", return_value=Path("/tmp/pinned-ocr")), \
             patch.object(ocr.subprocess, "run") as run:
            run.return_value.returncode = 7
            self.assertEqual(ocr.main(args), 7)
            self.assertEqual(run.call_args.args[0], ["/tmp/pinned-ocr", *args])
            self.assertNotIn("shell", run.call_args.kwargs)
            self.assertEqual(run.call_args.kwargs["env"]["OCR_NO_UPDATE"], "1")


if __name__ == "__main__":
    unittest.main()
