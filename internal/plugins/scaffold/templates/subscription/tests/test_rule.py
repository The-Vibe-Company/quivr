"""Unit tests for the alert rule. Run: python3 -m unittest discover -s tests"""
import unittest
from pathlib import Path

from quivr_plugin import RecordPart
from quivr_plugin.testing import build_subscription_requests, invoke_subscription_fixture

from __PLUGIN_MODULE__.rule import matching_parts, plugin

FIXTURES = Path(__file__).resolve().parent.parent / "fixtures"
PARTS = [RecordPart(key="title", role="title", text="Strike vote"), RecordPart(key="body", role="body", text="Workers may strike.")]


class MatchingParts(unittest.TestCase):
    def test_case_is_ignored_by_default(self):
        self.assertEqual(matching_parts(PARTS, "STRIKE"), ["title", "body"])

    def test_case_sensitive(self):
        self.assertEqual(matching_parts(PARTS, "Strike", case_sensitive=True), ["title"])

    def test_absent(self):
        self.assertEqual(matching_parts(PARTS, "election"), [])


class Rule(unittest.TestCase):
    def test_sample_fixture(self):
        response = invoke_subscription_fixture(plugin, FIXTURES / "sample.json")
        first, second, third = response.decisions
        self.assertEqual(first.decision, "match")
        self.assertEqual(first.evidence.part_keys, ["title", "body"])
        self.assertEqual(second.decision, "no_match")
        self.assertEqual(third.evidence.part_keys, ["body"])

    def test_invalid_expression_is_rejected(self):
        request = build_subscription_requests(FIXTURES / "sample.json")[0].to_dict()
        request["evaluations"][0]["expression"] = {"kind": "substring", "text": ""}
        reply = plugin.evaluate(request)
        self.assertEqual(reply.status, 400)
        self.assertEqual(reply.body["code"], "invalid_expression")
        self.assertFalse(reply.body["retryable"])


if __name__ == "__main__":
    unittest.main()
