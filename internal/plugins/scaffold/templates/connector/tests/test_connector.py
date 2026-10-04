"""Run: python3 -m unittest discover -s tests."""
import json
from pathlib import Path
import unittest

from quivr_plugin import FetchRequest, SourceError

from __PLUGIN_MODULE__.connector import StaticSource

FIXTURE = Path(__file__).resolve().parent.parent / "fixtures" / "sample.json"


class Connector(unittest.TestCase):
    def setUp(self):
        fixture = json.loads(FIXTURE.read_text())
        self.request = FetchRequest.from_dict({
            "invocation_id": "test-page",
            "organization_id": "test-org",
            "configuration": {},
            "connector": {"instance_id": "test-source", **fixture["connector"]},
            "credential": fixture["credential"],
            "checkpoint": None,
            "now": "2026-01-01T00:00:00Z",
            "page_in_run": 0,
            "reads_today": 0,
        })
        self.connector = StaticSource()

    def test_pages_and_resume(self):
        for expected_keys, expected_offset, more in [
            (["note-1", "note-2"], 2, True),
            (["note-3", "note-4"], 4, True),
            (["note-5"], 5, False),
            ([], 5, False),
        ]:
            with self.subTest(offset=self.request.checkpoint):
                page = self.connector.fetch(self.request)
                self.assertEqual([item.record_key for item in page.items], expected_keys)
                self.assertEqual(page.checkpoint, {"offset": expected_offset})
                self.assertEqual(page.more, more)
                self.assertEqual(page.reads, len(expected_keys))
                if self.request.checkpoint is None:
                    self.assertEqual(page.items[0].content.to_dict(), {
                        "kind": "manifest",
                        "parts": [
                            {"key": "title", "role": "title", "content": {"kind": "text", "text": "Harbour opens"}},
                            {"key": "body", "role": "body", "content": {"kind": "text", "text": "The harbour opened on Monday."}},
                        ],
                    })
                    self.assertEqual(page.items[0].extensions["__PLUGIN_ID__.source"].data, {"author": "Desk"})
                    self.assertIsNone(page.items[1].extensions)
                self.request.checkpoint = page.checkpoint

    def test_invalid_checkpoint(self):
        for checkpoint in [{}, {"offset": -1}, {"offset": True}, {"offset": "2"}, [2]]:
            with self.subTest(checkpoint=checkpoint):
                self.request.checkpoint = checkpoint
                with self.assertRaises(SourceError) as raised:
                    self.connector.fetch(self.request)
                self.assertEqual(raised.exception.code, "invalid_checkpoint")


if __name__ == "__main__":
    unittest.main()
