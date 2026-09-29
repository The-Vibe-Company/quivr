"""Unit tests for the normalizer. Run: python3 -m unittest discover -s tests"""
import json
import tempfile
import unittest
from pathlib import Path

from quivr_plugin.testing import expect_response, invoke_fixture

from __PLUGIN_MODULE__.normalizer import plugin, split_markdown

FIXTURES = Path(__file__).resolve().parent.parent / "fixtures"


class SplitMarkdown(unittest.TestCase):
    def test_title_and_sections(self):
        title, sections = split_markdown("# Report\n\nIntro.\n\n## One\n\nFirst.\n\n## Two\n\nSecond.\n")
        self.assertEqual(title, "Report")
        self.assertEqual(sections, ["Intro.", "## One\n\nFirst.", "## Two\n\nSecond."])

    def test_closing_hashes_need_whitespace(self):
        self.assertEqual(split_markdown("# Intro to C#\n")[0], "Intro to C#")
        self.assertEqual(split_markdown("# Closed ##\n")[0], "Closed")

    def test_headings_inside_code_blocks_are_ignored(self):
        _, sections = split_markdown("## Code\n\n```\n# not a heading\n```\n")
        self.assertEqual(len(sections), 1)

    def test_no_title_when_text_precedes_the_first_heading(self):
        title, sections = split_markdown("Preface.\n\n# Heading\n\nBody.\n")
        self.assertIsNone(title)
        self.assertEqual(sections, ["Preface.", "# Heading\n\nBody."])


class Normalizer(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)

    def fixture(self, text, configuration=None):
        (self.dir / "doc.md").write_text(text, encoding="utf-8")
        path = self.dir / "fixture.json"
        fixture = {"input": {"path": "doc.md", "media_type": "text/markdown"}}
        if configuration is not None:
            fixture["configuration"] = configuration
        path.write_text(json.dumps(fixture))
        return path

    def test_sample_fixture(self):
        response = expect_response(invoke_fixture(plugin, FIXTURES / "sample.json"))
        parts = response.manifest.parts
        self.assertEqual(parts[0].role, "title")
        self.assertEqual(parts[0].content.text, "Quarterly field report")
        self.assertEqual([p.role for p in parts[1:]], ["body", "body", "body"])
        self.assertIsNone(response.warnings)

    def test_sections_beyond_the_limit_are_merged(self):
        text = "\n".join(f"## Section {i}\n\nText {i}.\n" for i in range(5))
        response = expect_response(invoke_fixture(plugin, self.fixture(text, {"max_sections": 2})))
        self.assertEqual(len(response.manifest.parts), 2)
        self.assertIn("Section 4", response.manifest.parts[1].content.text)
        self.assertEqual(response.warnings[0].code, "sections_merged")

    def test_empty_document_is_a_terminal_error(self):
        reply = invoke_fixture(plugin, self.fixture("\n\n"))
        self.assertEqual(reply.status, 422)
        self.assertEqual(reply.body, {"code": "empty_document", "message": "the document has no text", "retryable": False})

    def test_invalid_configuration_is_rejected(self):
        reply = invoke_fixture(plugin, self.fixture("# Title\n", {"max_sections": 0}))
        self.assertEqual(reply.status, 400)
        self.assertEqual(reply.body["code"], "invalid_configuration")


if __name__ == "__main__":
    unittest.main()
