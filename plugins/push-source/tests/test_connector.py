"""Own the source's mapping. Contract Runner owns transport and protocol checks."""
import unittest

from quivr_plugin import ReceiveRequest
from push_source.connector import publish


class PushMapping(unittest.TestCase):
    def test_source_identity_and_content(self):
        request = ReceiveRequest.from_dict({
            "invocation_id": "test", "organization_id": "test-org", "configuration": {},
            "connector": {"instance_id": "test-source", "kind": "events", "config": {}, "corpus_id": "test-corpus", "source_namespace": "test-source"},
            "credential": None, "checkpoint": None, "now": "2026-01-01T00:00:00Z",
            "reads_today": 0, "route": "publish",
            "request": {"method": "POST", "path": "records", "query": "", "headers": {}, "body_base64": ""},
            "body": {"key": "note-1", "revision": "7", "title": "Library opens", "text": "On Monday."},
        })
        item = publish(request).items[0]
        self.assertEqual((item.record_key, item.revision), ("note-1", "7"))
        self.assertEqual(item.content.to_dict(), {"kind": "manifest", "parts": [
            {"key": "title", "role": "title", "content": {"kind": "text", "text": "Library opens"}},
            {"key": "body", "role": "body", "content": {"kind": "text", "text": "On Monday."}},
        ]})
