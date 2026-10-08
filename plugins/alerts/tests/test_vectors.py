"""Local-vector decisions at the subscription handler, without model or network calls."""
import math
import unittest
from pathlib import Path
from unittest.mock import patch

from quivr_plugin import TerminalError
from quivr_plugin.testing import invoke_subscription_fixture

from alerts.rule import plugin
from support import handler_decisions

SPACE = "text-space-1"
DESCRIPTION = "Dock workers going on strike at a harbour"
FIXTURES = Path(__file__).resolve().parent / "data"


def expression(kind="meaning", term="dockers"):
    value = {"kind": kind, "description": DESCRIPTION, "meaning_check": "vectors"}
    if kind in ("keywords_or_meaning", "keywords_and_meaning"):
        value["match"] = {"term": term}
    return value


def segment(vector, segment_id="body-1"):
    return {"segment_id": segment_id, "vector": vector}


class Vectors(unittest.TestCase):
    def setUp(self):
        factory = patch("alerts.rule.classifier", side_effect=AssertionError("local vectors initialized Jev"))
        factory.start()
        self.addCleanup(factory.stop)

    def test_sdk_fixtures_preserve_vectors_and_the_unready_state(self):
        for name in ("vectors.json", "vectors-not-ready.json"):
            with self.subTest(fixture=name):
                response = invoke_subscription_fixture(plugin, FIXTURES / name)
                if name == "vectors.json":
                    evidence = response.decisions[0].evidence
                    self.assertEqual(evidence.part_keys, ["body"])
                    self.assertEqual(evidence.details["vector_space_id"], SPACE)
                    self.assertEqual(evidence.details["segment_id"], "body-1")

    def ask(self, evaluations=None, *, parts=None, query_vectors=None, record=None, **kwargs):
        kwargs.setdefault("configuration", {"vectors": {"thresholds": {SPACE: 0.8}}})
        return handler_decisions(
            evaluations or [(expression(), {})],
            parts=parts if parts is not None else [
                {"key": "body", "role": "body", "text": "The dockers stopped work.", "vectors": [segment([1, 0])]},
            ],
            query_vectors=query_vectors if query_vectors is not None else [{"vector_space_id": SPACE, "vector": [1, 0]}],
            record={"vector_space_id": SPACE, "vectors_ready": True, **(record or {})}, **kwargs,
        )

    def test_the_best_segment_decides_each_query_and_identifies_its_evidence(self):
        parts = [
            {"key": "title", "role": "title", "text": "Labour news", "vectors": [segment([0, 1], "title-1")]},
            {"key": "body", "role": "body", "text": "The dockers stopped work.",
             "vectors": [segment([0, -1]), segment([3, 0], "body-2")]},
        ]
        decisions = self.ask([(expression(), {}), (expression("described"), {}), (expression(), {})], parts=parts, query_vectors=[
            {"vector_space_id": SPACE, "vector": [2, 0]}, {"vector_space_id": SPACE, "vector": [2, 0]},
            {"vector_space_id": SPACE, "vector": [-1, 0]},
        ])
        self.assertEqual([(item["id"], item["decision"]) for item in decisions], [("e1", "match"), ("e2", "match"), ("e3", "no_match")])
        evidence = decisions[0]["evidence"]
        self.assertEqual(evidence["part_keys"], ["body"])
        self.assertEqual(evidence["details"]["meaning_check"], "vectors")
        self.assertEqual(evidence["details"]["similarity"], 1.0)
        self.assertEqual(evidence["details"]["vector_space_id"], SPACE)
        self.assertEqual(evidence["details"]["segment_id"], "body-2")

    def test_missing_or_wrong_space_waits_and_a_ready_replay_can_match(self):
        cases = [
            {"record": {"vectors_ready": False}}, {"record": {"vector_space_id": "another-space"}},
            {"record": {"vector_space_id": ""}}, {"record": {"vectors_ready": None}},
            {"query_vectors": [None]}, {"query_vectors": [{"vector_space_id": "another-space", "vector": [1, 0]}]},
            {"parts": [{"key": "body", "role": "body", "text": "Dockers stopped work."}]},
        ]
        for options in cases:
            with self.subTest(options=options):
                self.assertEqual(self.ask(**options)[0]["decision"], "not_ready")
        self.assertEqual(self.ask(enriched=False)[0]["decision"], "not_ready")
        self.assertEqual(self.ask([(expression(), {"wait_for_enrichment": False})], enriched=False)[0]["decision"], "match")
        self.assertEqual(self.ask()[0]["decision"], "match")

    def test_invalid_vectors_never_match_and_do_not_hide_a_valid_segment(self):
        for invalid in ([], [0, 0], [1], [1, 0, 0], [math.nan, 0], [math.inf, 0], [True, 0], ["1", 0]):
            with self.subTest(vector=invalid):
                parts = [{"key": "body", "role": "body", "text": "Dockers stopped work.", "vectors": [segment(invalid)]}]
                self.assertEqual(self.ask(parts=parts)[0]["decision"], "not_ready")
                parts[0]["vectors"].append(segment([1, 0], "valid"))
                self.assertEqual(self.ask(parts=parts)[0]["decision"], "match")
                self.assertEqual(self.ask(query_vectors=[{"vector_space_id": SPACE, "vector": invalid}])[0]["decision"], "not_ready")
        for vector in ([1e308, 1e308], [1e-300, 0]):
            with self.subTest(vector=vector):
                self.assertEqual(self.ask([(expression(), {"threshold": 0.6})],
                                         query_vectors=[{"vector_space_id": SPACE, "vector": vector}])[0]["decision"], "match")

    def test_the_vector_threshold_is_inclusive_and_overridable(self):
        parts = [{"key": "body", "role": "body", "text": "Dockers stopped work.", "vectors": [segment([3, 4])]}]
        decisions = self.ask([(expression(), {"threshold": 0.6}), (expression(), {"threshold": 0.600001})],
                             parts=parts, query_vectors=[{"vector_space_id": SPACE, "vector": [1, 0]}] * 2)
        self.assertEqual([item["decision"] for item in decisions], ["match", "no_match"])
        self.assertEqual(self.ask(parts=parts, configuration={"vectors": {"thresholds": {SPACE: 0.6}}})[0]["decision"], "match")
        self.assertEqual(self.ask([(expression(), {"threshold": 0.7})], parts=parts,
                                 configuration={"vectors": {"thresholds": {SPACE: 0.6}}})[0]["decision"], "no_match")
        for configuration in ({}, {"vectors": {"thresholds": {"another-space": 0.6}}}, {"described": {"threshold": 0.2}}):
            with self.subTest(configuration=configuration):
                with self.assertRaises(TerminalError) as caught:
                    self.ask(configuration=configuration)
                self.assertEqual(caught.exception.code, "vector_threshold_required")
                self.assertIn(SPACE, str(caught.exception))
                self.assertEqual(self.ask([(expression(), {"threshold": 0.6})], configuration=configuration)[0]["decision"], "match")

    def test_mixed_modes_short_circuit_before_readiness_otherwise_use_meaning(self):
        cases = [
            ("keywords_or_meaning", "dockers", False, [0, 1], "match"),
            ("keywords_or_meaning", "unrelated", False, [1, 0], "not_ready"),
            ("keywords_or_meaning", "unrelated", True, [1, 0], "match"),
            ("keywords_or_meaning", "unrelated", True, [0, 1], "no_match"),
            ("keywords_and_meaning", "unrelated", False, [1, 0], "no_match"),
            ("keywords_and_meaning", "dockers", False, [1, 0], "not_ready"),
            ("keywords_and_meaning", "dockers", True, [1, 0], "match"),
            ("keywords_and_meaning", "dockers", True, [0, 1], "no_match"),
        ]
        for kind, term, ready, vector, expected in cases:
            with self.subTest(kind=kind, term=term, ready=ready, vector=vector):
                decision = self.ask([(expression(kind, term), {})], enriched=ready, record={"vectors_ready": ready},
                                    query_vectors=[{"vector_space_id": SPACE, "vector": vector}])[0]
                self.assertEqual(decision["decision"], expected)
                if expected == "match":
                    self.assertEqual(decision["evidence"]["details"]["kind"], kind)

    def test_sources_rule_out_meaning_even_when_vectors_are_unavailable(self):
        self.assertEqual(self.ask([({**expression(), "sources": ["elsewhere"]}, {})],
                                 record={"vectors_ready": False})[0]["decision"], "no_match")


if __name__ == "__main__":
    unittest.main()
