"""Input Blob reading and SHA-256 verification."""
import hashlib
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from quivr_plugin import FileReference, InputBlob, RetryableError, SignedUrlReference, TerminalError, read_input

BODY = b"%PDF-1.7 not really a pdf\n"
SHA = hashlib.sha256(BODY).hexdigest()


def blob(reference, *, size=len(BODY), sha=SHA):
    return InputBlob(blob_id="b", media_type="application/pdf", size_bytes=size, sha256=sha, reference=reference)


class FileReferences(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.path = Path(tmp.name) / "input file.pdf"
        self.path.write_bytes(BODY)
        self.ref = FileReference(url=self.path.as_uri())

    def test_reads_and_verifies(self):
        self.assertEqual(read_input(blob(self.ref)), BODY)

    def test_checksum_mismatch_is_terminal(self):
        with self.assertRaises(TerminalError) as caught:
            read_input(blob(self.ref, sha="0" * 64))
        self.assertEqual(caught.exception.code, "input_checksum_mismatch")

    def test_size_mismatch_is_terminal(self):
        for size in (len(BODY) - 1, len(BODY) + 1):
            with self.subTest(size), self.assertRaises(TerminalError) as caught:
                read_input(blob(self.ref, size=size))
            self.assertEqual(caught.exception.code, "input_size_mismatch")

    def test_missing_file_is_terminal(self):
        with self.assertRaises(TerminalError) as caught:
            read_input(blob(FileReference(url=(self.path.parent / "missing.pdf").as_uri())))
        self.assertEqual(caught.exception.code, "input_not_found")


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802
        status = int(self.path.strip("/").split("?")[0] or 200)
        payload = BODY if status == 200 else b"error"
        self.send_response(status)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *args):
        pass


class SignedURLs(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base = f"http://127.0.0.1:{cls.server.server_port}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def ref(self, status):
        return SignedUrlReference(url=f"{self.base}/{status}?sig=abc", expires_at="2030-01-01T00:00:00Z")

    def test_reads_and_verifies(self):
        self.assertEqual(read_input(blob(self.ref(200))), BODY)
        with self.assertRaises(TerminalError):
            read_input(blob(self.ref(200), sha="1" * 64))

    def test_transient_failures_are_retryable(self):
        for status in (403, 429, 500, 503):
            with self.subTest(status), self.assertRaises(RetryableError) as caught:
                read_input(blob(self.ref(status)))
            self.assertEqual(caught.exception.code, "input_unavailable")

    def test_missing_blob_is_terminal(self):
        with self.assertRaises(TerminalError):
            read_input(blob(self.ref(404)))

    def test_connection_refused_is_retryable(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        port = server.server_port
        server.server_close()
        with self.assertRaises(RetryableError):
            read_input(blob(SignedUrlReference(url=f"http://127.0.0.1:{port}/200", expires_at="2030-01-01T00:00:00Z")), timeout=2)


if __name__ == "__main__":
    unittest.main()
