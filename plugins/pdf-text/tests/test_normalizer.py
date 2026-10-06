"""Unit tests for pdf-text. Run from plugins/pdf-text: python3 -m unittest discover -s tests"""
import importlib.util
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from pypdf import PdfReader, PdfWriter
from quivr_plugin.testing import expect_response, invoke_fixture

from pdf_text.normalizer import plugin

ROOT = Path(__file__).resolve().parent.parent
FIXTURES = ROOT / "fixtures"

_spec = importlib.util.spec_from_file_location("make_fixtures", ROOT / "scripts" / "make_fixtures.py")
make_fixtures = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(make_fixtures)


class Fixtures(unittest.TestCase):
    def test_committed_fixtures_match_the_generator(self):
        for relative, build in make_fixtures.FILES.items():
            self.assertEqual((ROOT / relative).read_bytes(), build(), f"{relative}: run python3 scripts/make_fixtures.py")


class Normalizer(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)

    def fixture(self, data: bytes, configuration=None) -> Path:
        (self.dir / "doc.pdf").write_bytes(data)
        fixture = {"input": {"path": "doc.pdf", "media_type": "application/pdf"}}
        if configuration is not None:
            fixture["configuration"] = configuration
        path = self.dir / "fixture.json"
        path.write_text(json.dumps(fixture))
        return path

    def invoke(self, data: bytes, configuration=None):
        return invoke_fixture(plugin, self.fixture(data, configuration))

    def test_sample_fixture_has_one_body_part_per_page_with_text(self):
        reply = invoke_fixture(plugin, FIXTURES / "sample.json")
        response = expect_response(reply)
        parts = response.manifest.parts
        self.assertEqual([(p.key, p.role) for p in parts], [("page-1", "body"), ("page-2", "body"), ("source", "source")])
        self.assertIn("observatory", parts[0].content.text)
        self.assertIn("heliotrope aurora", parts[1].content.text)
        self.assertNotIn("heliotrope", parts[0].content.text)
        self.assertEqual(parts[2].content.kind, "blob")
        self.assertEqual(parts[2].content.media_type, "application/pdf")
        self.assertEqual([w.code for w in response.warnings], ["empty_pages"])
        self.assertIn("3", response.warnings[0].message)

    def test_document_extension_counts_pages(self):
        response = expect_response(invoke_fixture(plugin, FIXTURES / "sample.json"))
        entry = response.extensions["pdf-text.document"]
        self.assertEqual(entry.schema_version, "1")
        self.assertEqual(entry.data, {"page_count": 3, "text_pages": 2})

    def test_common_metadata_uses_pdf_author_when_present(self):
        source = make_fixtures.build_pdf([["Metadata page"]])
        writer = PdfWriter(clone_from=PdfReader(io.BytesIO(source)))
        writer.add_metadata({"/Author": "Ada Example"})
        output = io.BytesIO()
        writer.write(output)

        response = expect_response(self.invoke(output.getvalue(), {"include_source": False}))
        entry = response.extensions["quivr.metadata"]
        self.assertEqual(entry.schema_version, "1")
        self.assertEqual(entry.data, {"source_type": "document", "author": ["Ada Example"]})

        # Normalization adds document metadata without relabeling its source.
        from quivr_plugin.testing import build_request
        from quivr_plugin.models import ExtensionEntry
        request = build_request(self.fixture(output.getvalue()))
        request.extensions = {"quivr.metadata": ExtensionEntry(schema_version="1", data={"source_type": "mail"})}
        reply = plugin.handle("POST", "/v0/contributions/normalizer", json.dumps(request.to_dict()).encode())
        self.assertEqual(expect_response(reply).extensions["quivr.metadata"].data["source_type"], "mail")

    def test_broken_pdf_info_is_advisory(self):
        class Page:
            def extract_text(self):
                return "Readable page"

        class Reader:
            pages = [Page()]

            @property
            def metadata(self):
                raise ValueError("malformed Info dictionary")

        with patch("pdf_text.normalizer._open", return_value=Reader()):
            response = expect_response(self.invoke(b"placeholder", {"include_source": False}))
        self.assertEqual(response.extensions["quivr.metadata"].data, {"source_type": "document"})
        self.assertEqual(response.manifest.parts[0].content.text, "Readable page")

    def test_source_part_references_the_input_blob(self):
        from quivr_plugin.testing import build_request
        request = build_request(FIXTURES / "sample.json")
        response = expect_response(invoke_fixture(plugin, FIXTURES / "sample.json"))
        self.assertEqual(response.manifest.parts[-1].content.blob_id, request.input.blob_id)

    def test_source_part_can_be_left_out(self):
        response = expect_response(invoke_fixture(plugin, FIXTURES / "sample.json", configuration={"include_source": False}))
        self.assertEqual([p.key for p in response.manifest.parts], ["page-1", "page-2"])

    def test_pages_beyond_the_limit_are_merged_into_the_last_part(self):
        data = make_fixtures.build_pdf([[f"Page number {i} text"] for i in range(1, 6)])
        response = expect_response(self.invoke(data, {"max_page_parts": 2, "include_source": False}))
        parts = response.manifest.parts
        self.assertEqual([p.key for p in parts], ["page-1", "page-2"])
        self.assertIn("Page number 5", parts[1].content.text)
        self.assertEqual([w.code for w in response.warnings], ["pages_merged"])

    def test_text_beyond_the_budget_is_dropped(self):
        data = make_fixtures.build_pdf([["A" * 80] * 20 for _ in range(3)])
        response = expect_response(self.invoke(data, {"max_text_bytes": 2000, "include_source": False}))
        total = sum(len(p.content.text.encode()) for p in response.manifest.parts)
        self.assertLessEqual(total, 2000)
        self.assertIn("text_truncated", [w.code for w in response.warnings])

    def test_a_pdf_without_text_keeps_only_its_source_with_a_warning(self):
        response = expect_response(self.invoke(make_fixtures.build_pdf([[], []])))
        self.assertEqual([p.key for p in response.manifest.parts], ["source"])
        self.assertEqual([w.code for w in response.warnings], ["empty_pages", "no_text"])

    def test_empty_page_warning_stays_bounded(self):
        data = make_fixtures.build_pdf([["Only text"]] + [[] for _ in range(600)])
        response = expect_response(self.invoke(data))
        self.assertEqual(len(response.warnings), 1)
        self.assertLessEqual(len(response.warnings[0].message), 1024)
        self.assertIn("600", response.warnings[0].message)

    def test_a_pdf_without_text_and_without_source_is_a_terminal_error(self):
        reply = self.invoke(make_fixtures.build_pdf([[]]), {"include_source": False})
        self.assertEqual(reply.status, 422)
        self.assertEqual(reply.body["code"], "no_text")
        self.assertFalse(reply.body["retryable"])

    def test_corrupt_pdf_is_a_terminal_error(self):
        for data in [b"%PDF-1.4\nthis is not a PDF body\n", b"not a pdf at all", make_fixtures.build_pdf([["cut"]])[:60]]:
            reply = self.invoke(data)
            self.assertEqual(reply.status, 422, data)
            self.assertEqual(reply.body["code"], "corrupt_pdf")
            self.assertFalse(reply.body["retryable"])

    @staticmethod
    def encrypted(text: str, user_password: str, algorithm: str) -> bytes:
        writer = PdfWriter(clone_from=PdfReader(io.BytesIO(make_fixtures.build_pdf([[text]]))))
        writer.encrypt(user_password=user_password, owner_password="owner", algorithm=algorithm)
        buffer = io.BytesIO()
        writer.write(buffer)
        return buffer.getvalue()

    def test_encrypted_pdf_is_a_terminal_error(self):
        for algorithm in ["RC4-128", "AES-128", "AES-256"]:
            reply = self.invoke(self.encrypted("Secret page", "not-empty", algorithm))
            self.assertEqual(reply.status, 422, algorithm)
            self.assertEqual(reply.body["code"], "encrypted_pdf", algorithm)

    def test_pdf_encrypted_with_an_empty_user_password_is_read(self):
        # Owner-password-only PDFs, often AES-encrypted, open without a password.
        for algorithm in ["RC4-128", "AES-128", "AES-256"]:
            response = expect_response(self.invoke(self.encrypted("Readable page", "", algorithm), {"include_source": False}))
            self.assertIn("Readable page", response.manifest.parts[0].content.text, algorithm)

    def test_text_is_valid_for_indexing(self):
        from pdf_text.normalizer import clean
        self.assertEqual(clean("a\x00b\ud800c  \n\n\n\nd "), "abc\n\nd")

    def test_invalid_configuration_is_rejected(self):
        reply = invoke_fixture(plugin, FIXTURES / "sample.json", configuration={"max_page_parts": 0})
        self.assertEqual(reply.status, 400)
        self.assertEqual(reply.body["code"], "invalid_configuration")


if __name__ == "__main__":
    unittest.main()
