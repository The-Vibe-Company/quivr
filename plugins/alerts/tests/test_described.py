"""Described alerts decided through the plugin's subscription route, with Jev
talking to the fake System One server (alerts.fake_system_one), never TypeSafe.

Run: python3 -m unittest discover -s tests
"""
import json
import os
import threading
import unittest

from alerts import described, jev, rule
from alerts.fake_system_one import FakeSystemOne
from support import reply

STRIKE = "Dock workers going on strike at a harbour"
VISAS = "Diplomatic tensions over visas"
EN_PARTS = [
    {"key": "title", "role": "title", "text": "Harbour staff walk out over pay"},
    {"key": "body", "role": "body", "text": "Dockers at the northern harbour began a walkout on Tuesday."},
]
FR_PARTS = [
    {"key": "title", "role": "title", "text": "Les dockers votent la grève au port"},
    {"key": "body", "role": "body", "text": "Au port du Havre, les dockers ont voté une grève de 48 heures."},
]


def D(text):
    return {"kind": "described", "description": text}


class FakeServer(unittest.TestCase):
    """Each test runs the plugin against a fresh fake System One server."""

    def setUp(self):
        self.fake = FakeSystemOne("test-key")
        server = self.fake.server()
        threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        self.url = f"http://127.0.0.1:{server.server_address[1]}/v1/systemone"
        self.key = "test-key"
        previous = rule.classifier
        rule.classifier = lambda: jev.Jev(self.key, self.url) if self.key else None
        self.addCleanup(setattr, rule, "classifier", previous)

    def ask(self, evaluations, **kwargs):
        kwargs.setdefault("enriched", True)
        return reply(evaluations, **kwargs)

    def decisions(self, evaluations, **kwargs):
        answer = self.ask(evaluations, **kwargs)
        self.assertEqual(answer.status, 200, answer.body)
        return [d["decision"] for d in answer.body["decisions"]]


class Decisions(FakeServer):
    def test_mixed_checks_share_jev_and_preserve_order(self):
        evaluations = [
            (D(STRIKE), {}), ({**D(STRIKE), "meaning_check": "jev"}, {}),
            ({"kind": "keywords_or_meaning", "match": {"term": "unrelated"}, "description": STRIKE, "meaning_check": "jev"}, {}),
            ({"kind": "keywords_or_meaning", "match": {"term": "unrelated"}, "description": VISAS, "meaning_check": "jev"}, {}),
            ({"kind": "keywords_and_meaning", "match": {"term": "dockers"}, "description": STRIKE, "meaning_check": "jev"}, {}),
            ({"kind": "keywords_and_meaning", "match": {"term": "dockers"}, "description": VISAS, "meaning_check": "jev"}, {}),
        ]
        answer = self.ask(evaluations, parts=FR_PARTS)
        self.assertEqual(answer.status, 200, answer.body)
        decisions = answer.body["decisions"]
        self.assertEqual([item["decision"] for item in decisions], ["match", "match", "match", "no_match", "match", "no_match"])
        self.assertEqual([item["id"] for item in decisions], [f"e{index}" for index in range(1, 7)])
        self.assertEqual([sorted(item["descriptions"]) for item in self.fake.requests], [sorted([STRIKE, VISAS])])
        details = decisions[4]["evidence"]["details"]
        self.assertEqual((details["kind"], details["meaning_check"]), ("keywords_and_meaning", "jev"))
        self.assertEqual(details["keywords"]["terms"], [{"term": "dockers", "part_keys": ["title", "body"]}])

    def test_mixed_keywords_decide_without_a_key_or_enrichment(self):
        self.key = None
        expressions = [
            {"kind": "keywords_or_meaning", "match": {"term": "dockers"}, "description": STRIKE, "meaning_check": "jev"},
            {"kind": "keywords_and_meaning", "match": {"term": "unrelated"}, "description": STRIKE, "meaning_check": "jev"},
        ]
        self.assertEqual(self.decisions([(item, {}) for item in expressions], parts=FR_PARTS, enriched=False), ["match", "no_match"])
        self.assertEqual(self.fake.requests, [])

    def test_the_evidence_names_the_classifier_its_score_and_the_parts_it_saw(self):
        decision = self.ask([(D(STRIKE), {})], parts=FR_PARTS).body["decisions"][0]
        evidence = decision["evidence"]
        self.assertEqual(evidence["explanation"], "Jev (jev-1.13.0) judged that the article fits the description: score 0.92, threshold 0.50.")
        self.assertEqual(evidence["part_keys"], ["title", "body"])
        self.assertEqual(evidence["details"], {"kind": "described", "classifier": "Jev", "model": "jev-1.13.0", "score": 0.92, "threshold": 0.5, "truncated": False})

    def test_the_threshold_decides_and_a_subscription_may_override_it(self):
        # The fake scores 0.35 when the article has only some of the description's topics.
        partial = D("Dock strikes and floods")
        self.assertEqual(self.decisions([(partial, {}), (partial, {"threshold": 0.3}), (D(STRIKE), {"threshold": 0.95})], parts=EN_PARTS),
                         ["no_match", "match", "no_match"])
        self.assertEqual(self.decisions([(partial, {})], parts=EN_PARTS, configuration={"described": {"threshold": 0.3}}), ["match"])


class Batching(FakeServer):
    def test_one_call_decides_every_described_alert_and_identical_descriptions_are_asked_once(self):
        evaluations = [(D(STRIKE), {}), (D(VISAS), {}), (D("  Dock workers going on  strike at a harbour "), {"threshold": 0.4}),
                       (D(STRIKE), {"threshold": 0.9}), ({"kind": "keywords", "match": {"term": "grève"}}, {}),
                       ({"kind": "meaning", "description": "A separate local topic", "meaning_check": "vectors"}, {})]
        parts = [FR_PARTS[0], {**FR_PARTS[1], "vectors": [{"segment_id": "body-1", "vector": [0, 1]}]}]
        self.assertEqual(self.decisions(evaluations, parts=parts,
                                       record={"vector_space_id": "text-space-1", "vectors_ready": True},
                                       query_vectors=[None] * 5 + [{"vector_space_id": "text-space-1", "vector": [0, 1]}]),
                         ["match", "no_match", "match", "match", "match", "match"])
        self.assertEqual(len(self.fake.requests), 1)
        self.assertEqual(sorted(self.fake.requests[0]["descriptions"]), sorted([STRIKE, VISAS]))
        self.assertEqual(self.fake.requests[0]["model"], "jev-1.13.0")

    def test_keyword_alerts_alone_never_call_the_classifier(self):
        self.key = None
        self.assertEqual(self.decisions([({"kind": "keywords", "match": {"term": "grève"}}, {})], parts=FR_PARTS), ["match"])
        self.assertEqual(self.fake.requests, [])

    def test_described_alerts_wait_for_enrichment_by_default(self):
        evaluations = [(D(STRIKE), {}), (D(STRIKE), {"wait_for_enrichment": False})]
        self.assertEqual(self.decisions(evaluations, parts=FR_PARTS, enriched=False), ["not_ready", "match"])
        self.assertEqual(len(self.fake.requests), 1)

    def test_a_request_is_split_only_past_the_size_limit(self):
        # 200 long distinct descriptions do not fit one request.
        many = [(D(f"{STRIKE}, case {i}: " + "detail " * 130), {}) for i in range(200)]
        self.assertEqual(set(self.decisions(many, parts=FR_PARTS)), {"match"})
        self.assertGreater(len(self.fake.requests), 1)
        self.assertLess(len(self.fake.requests), 10)
        self.assertEqual(sum(len(r["descriptions"]) for r in self.fake.requests), 200)
        self.assertTrue(all(r["bytes"] <= jev.MAX_REQUEST_BYTES for r in self.fake.requests))


class State(FakeServer):
    def test_a_long_article_is_cut_at_a_word_boundary_and_the_evidence_says_so(self):
        parts = [{"key": "title", "role": "title", "text": "Harbour strike"},
                 {"key": "lead", "role": "body", "text": "Dockers stopped work at the harbour."},
                 {"key": "long", "role": "body", "text": "grève " * 20_000},
                 {"key": "after", "role": "body", "text": "Never sent."}]
        decision = self.ask([(D(STRIKE), {})], parts=parts).body["decisions"][0]
        self.assertEqual(decision["evidence"]["part_keys"], ["title", "lead", "long"])
        self.assertTrue(decision["evidence"]["details"]["truncated"])
        state = described.article_state({"parts": parts}, {})
        self.assertLessEqual(len(json.dumps(state.value, ensure_ascii=False, separators=(",", ":")).encode()), described.MAX_STATE_BYTES)
        self.assertTrue(state.value["text"].endswith("grève"))

    def test_the_classifier_sees_the_source_and_the_mapped_metadata(self):
        record = {"parts": FR_PARTS, "source": {"namespace": "wire", "record_key": "k"},
                  "extensions": {"example.news": {"schema_version": "1", "data": {"author": "Jane Doe", "categories": ["Economy", "Labour"]}}}}
        configuration = {"fields": {"author": "/extensions/example.news/data/author", "category": "/extensions/example.news/data/categories",
                                    "desk": "/extensions/example.news/data/desk"}}
        state = described.article_state(record, configuration).value
        self.assertEqual({k: state[k] for k in ("title", "source", "author", "category")},
                         {"title": "Les dockers votent la grève au port", "source": "wire", "author": "Jane Doe", "category": ["Economy", "Labour"]})
        self.assertNotIn("desk", state)


class Errors(FakeServer):
    def fail(self, status):
        self.fake.failures.append(status)
        answer = self.ask([(D(STRIKE), {}), ({"kind": "keywords", "match": {"term": "grève"}}, {})], parts=FR_PARTS)
        self.assertNotIn("a failure the test asked for", json.dumps(answer.body))
        return answer.status, answer.body["code"], answer.body["retryable"]

    def test_a_forbidden_key_is_terminal(self):
        self.assertEqual(self.fail(403), (422, "classifier_unauthorized", False))

    def test_an_answer_without_scores_is_terminal(self):
        # The fake answers 200 with a body that has no answers.
        self.assertEqual(self.fail(200), (422, "classifier_invalid_answer", False))

    def test_a_refused_key_is_terminal_with_a_clear_diagnostic(self):
        self.key = "wrong-key"
        answer = self.ask([(D(STRIKE), {})], parts=FR_PARTS)
        self.assertEqual((answer.status, answer.body["code"], answer.body["retryable"]), (422, "classifier_unauthorized", False))
        self.assertIn("TYPESAFE_API_KEY", answer.body["message"])
        self.assertNotIn("wrong-key", answer.body["message"])

    def test_rate_limits_and_server_errors_are_retryable(self):
        for status in (408, 429, 500, 503, 529):
            with self.subTest(status=status):
                self.assertEqual(self.fail(status), (503, "classifier_unavailable", True))

    def test_a_refused_request_is_terminal(self):
        self.assertEqual(self.fail(422), (422, "classifier_refused_request", False))

    def test_an_unreachable_classifier_is_retryable(self):
        rule.classifier = lambda: jev.Jev("test-key", "http://127.0.0.1:9/v1/systemone", timeout=2)
        answer = self.ask([(D(STRIKE), {})], parts=FR_PARTS)
        self.assertEqual((answer.status, answer.body["code"], answer.body["retryable"]), (503, "classifier_unavailable", True))

    def test_without_a_key_described_alerts_cannot_be_decided(self):
        self.key = None
        answer = self.ask([(D(STRIKE), {})], parts=FR_PARTS)
        self.assertEqual((answer.status, answer.body["code"], answer.body["retryable"]), (422, "described_unavailable", False))


class Sources(FakeServer):
    def test_only_articles_from_the_chosen_sources_reach_the_classifier(self):
        # The request's article comes from the Source Namespace "wire".
        elsewhere = {**D(STRIKE), "sources": ["feed-b"]}
        here = {**D(VISAS), "sources": ["feed-b", "WIRE"]}
        answer = self.ask([(elsewhere, {}), (here, {}), (D(STRIKE), {})], parts=FR_PARTS)
        self.assertEqual([d["decision"] for d in answer.body["decisions"]], ["no_match", "no_match", "match"])
        self.assertEqual(answer.body["decisions"][0]["evidence"]["explanation"], 'The article\'s source "wire" is not one of the alert\'s sources.')
        # Only the alerts watching this source were asked about the article.
        self.assertEqual([sorted(r["descriptions"]) for r in self.fake.requests], [sorted([STRIKE, VISAS])])

    def test_an_article_from_another_source_is_decided_without_a_call_or_enrichment(self):
        self.key = None
        self.assertEqual(self.decisions([({**D(STRIKE), "sources": ["feed-b"]}, {})], parts=FR_PARTS, enriched=False), ["no_match"])
        self.assertEqual(self.fake.requests, [])


class Schema(unittest.TestCase):
    def test_malformed_described_alerts_are_refused(self):
        for expression, configuration in [(D("  "), {}), (D("ab"), {}), (D("x" * 1001), {}), ({"kind": "described"}, {}),
                                          ({**D(STRIKE), "match": {"term": "a"}}, {}), (D(STRIKE), {"threshold": 0.1}), (D(STRIKE), {"threshold": 1}),
                                          ({**D(STRIKE), "sources": []}, {}), ({**D(STRIKE), "sources": [" "]}, {}), ({**D(STRIKE), "sources": ["a", "a"]}, {})]:
            with self.subTest(expression=expression, configuration=configuration):
                self.assertEqual(reply([(expression, configuration)]).status, 400)

    def test_malformed_meaning_selectors_and_mixed_expressions_are_refused(self):
        for expression in [
            {**D(STRIKE), "meaning_check": "unknown"}, {**D(STRIKE), "meaning_check": None},
            {**D(STRIKE), "kind": "keywords_or_meaning"},
            {"kind": "keywords_and_meaning", "match": {"term": "dockers"}},
            {**D(STRIKE), "kind": "keywords_and_meaning", "match": {"all": []}},
            {**D(STRIKE), "kind": "keywords_or_meaning", "match": {"term": "dockers"}, "meaning_check": "other"},
            {**D(STRIKE), "kind": "keywords_or_meaning", "match": {"term": "dockers"}},
            {**D(STRIKE), "kind": "meaning"}, {**D(STRIKE), "kind": "meaning", "meaning_check": "jev"},
        ]:
            with self.subTest(expression=expression):
                self.assertEqual(reply([(expression, {})]).status, 400)


class Environment(FakeServer):
    def test_the_key_and_the_endpoint_come_from_the_environment(self):
        self.assertIsNone(jev.Jev.from_environment({}))
        self.assertIsNone(jev.Jev.from_environment({"TYPESAFE_API_KEY": "  "}))
        client = jev.Jev.from_environment({"TYPESAFE_API_KEY": "test-key", "TYPESAFE_API_URL": self.url})
        self.assertEqual(client.judge({"text": "Dockers on strike at the harbour"}, [STRIKE]), {STRIKE: 0.92})


@unittest.skipUnless(os.environ.get("TYPESAFE_API_KEY") and os.environ.get("QUIVR_ALERTS_LIVE") == "1",
                     "live TypeSafe test: set QUIVR_ALERTS_LIVE=1 and TYPESAFE_API_KEY")
class Live(unittest.TestCase):
    """Opt-in: one real Jev call. Never runs in CI, which has no key."""

    def test_jev_tells_a_fitting_article_from_an_unrelated_one(self):
        client = jev.Jev.from_environment()
        state = described.article_state({"parts": FR_PARTS}, {}).value
        scores = client.judge(state, [STRIKE, "Football transfer news"])
        self.assertGreaterEqual(scores[STRIKE], described.DEFAULT_THRESHOLD)
        self.assertLess(scores["Football transfer news"], described.DEFAULT_THRESHOLD)


if __name__ == "__main__":
    unittest.main()
