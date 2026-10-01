"""The subscription Contribution: routing, request checks, response checks and fixture helpers."""
import hashlib
import unittest
from pathlib import Path

from quivr_plugin import (
    Decision,
    Plugin,
    RetryableError,
    SubscriptionInvocation,
    SubscriptionRequest,
    match,
    no_match,
    not_ready,
    record_field,
)
from quivr_plugin.testing import build_subscription_requests, expected_decisions, invoke_subscription_fixture
from quivr_plugin.manifest import negotiate_plugin_api

DATA = Path(__file__).resolve().parent / "data"
CONTRACT = Path(__file__).resolve().parents[3] / "contracts" / "plugins" / "v0" / "fixtures"
STRIKE = CONTRACT / "subscriptions" / "strike.json"


def make_plugin() -> Plugin:
    plugin = Plugin(DATA / "subscription-plugin.yaml")

    @plugin.subscription
    def evaluate(invocation: SubscriptionInvocation):
        mode = invocation.configuration.get("mode", "ok")
        if mode == "crash":
            raise RuntimeError("boom")
        if mode == "retry":
            raise RetryableError("backend_busy", "try again later")
        decisions = []
        for evaluation in invocation.evaluations:
            sensitive = evaluation.configuration.get("case_sensitive", False)
            needle = evaluation.expression["text"]
            keys = [p.key for p in invocation.parts
                    if (needle in p.text if sensitive else needle.lower() in p.text.lower())]
            if not keys:
                decisions.append(no_match(evaluation))
            elif mode == "unknown-part":
                decisions.append(match(evaluation, "found", part_keys=["nowhere"]))
            elif mode == "no-evidence":
                decisions.append(Decision(id=evaluation.id, decision="match"))
            elif mode == "huge-details":
                decisions.append(match(evaluation, "found", details={"padding": "<" * 4000}))
            elif mode == "long-explanation":
                decisions.append(match(evaluation, "é" * 4097))
            elif mode == "dicts":
                decisions.append({"id": evaluation.id, "decision": "match", "evidence": {"explanation": "found"}})
            else:
                decisions.append(match(evaluation, f"{needle!r} appears.", part_keys=keys, details={"needle": needle}))
        if mode == "missing":
            decisions = decisions[:-1]
        return decisions

    return plugin


def request(mode: str = "ok", *, expression=None, evaluation_configuration=None) -> dict:
    return {
        "invocation_id": "inv-1",
        "idempotency_key": "key-1",
        "contribution": "subscription",
        "organization_id": "org",
        "record": {"corpus_id": "c", "record_id": "r", "record_version_id": "v", "enriched": False,
                   "parts": [{"key": "title", "role": "title", "text": "Dockers vote to strike"}]},
        "evaluations": [{"id": "e1", "expression": expression or {"kind": "substring", "text": "strike"},
                         "configuration": evaluation_configuration or {},
                         "subscriptions": [{"subscription_id": "s", "subscription_version_id": "sv", "saved_query_id": "q", "saved_query_version_id": "qv"}]}],
        "configuration": {"mode": mode},
    }


class SubscriptionTest(unittest.TestCase):
    def setUp(self) -> None:
        self.plugin = make_plugin()

    def assertEnvelope(self, reply, status: int, code: str, retryable: bool) -> None:
        self.assertEqual(reply.status, status, reply.body)
        self.assertEqual((reply.body["code"], reply.body["retryable"]), (code, retryable), reply.body)

    def test_discovery_lists_the_subscription_and_negotiates_plugin_api(self) -> None:
        body = self.plugin.handle("GET", "/v0/discovery").body
        self.assertEqual(body["contributions"], ["subscription"])
        self.assertEqual(body["plugin_api"], "0.2.0")

        for minor in range(1, 11):
            with self.subTest(minor=minor):
                expected = "0.3.1" if minor == 3 else f"0.{minor}.0"
                self.assertEqual(negotiate_plugin_api(f">=0.{minor}.0 <0.{minor + 1}.0"), expected)

    def test_decides_each_evaluation(self) -> None:
        reply = self.plugin.evaluate(request())
        self.assertEqual(reply.status, 200, reply.body)
        self.assertEqual(reply.body["decisions"][0]["decision"], "match")
        self.assertEqual(reply.body["decisions"][0]["evidence"]["part_keys"], ["title"])
        self.assertEqual(self.plugin.evaluate(request("dicts")).status, 200)
        model = SubscriptionRequest.from_dict(request())
        self.assertEqual(self.plugin.evaluate(model).status, 200)

    def test_refuses_invalid_requests_terminally(self) -> None:
        self.assertEnvelope(self.plugin.handle("POST", "/v0/contributions/subscription", b"{"), 400, "invalid_request", False)
        broken = request()
        broken["unexpected"] = True
        self.assertEnvelope(self.plugin.evaluate(broken), 400, "invalid_request", False)
        self.assertEnvelope(self.plugin.evaluate(request(expression={"kind": "regex", "text": "a"})), 400, "invalid_expression", False)
        self.assertEnvelope(self.plugin.evaluate(request(evaluation_configuration={"case_sensitive": "yes"})), 400,
                            "invalid_subscription_configuration", False)
        self.assertEnvelope(self.plugin.evaluate({**request(), "configuration": {"mode": "bogus"}}), 400, "invalid_configuration", False)
        # A subscription-only plugin has no normalizer route.
        self.assertEqual(self.plugin.handle("POST", "/v0/contributions/normalizer", b"{}").status, 501)

    def test_checks_the_response_before_sending_it(self) -> None:
        for mode, fragment in {
            "missing": "is not answered",
            "unknown-part": "is not a Part",
            "no-evidence": "needs evidence",
            "huge-details": "bytes of JSON",
            "long-explanation": "explanation",
        }.items():
            with self.subTest(mode=mode):
                reply = self.plugin.evaluate(request(mode))
                self.assertEnvelope(reply, 500, "invalid_response", False)
                self.assertIn(fragment, reply.body["message"])

    def test_maps_handler_errors_to_the_envelope(self) -> None:
        self.assertEnvelope(self.plugin.evaluate(request("retry")), 503, "backend_busy", True)
        self.assertEnvelope(self.plugin.evaluate(request("crash")), 500, "internal_error", False)

    def test_decision_helpers(self) -> None:
        self.assertEqual(not_ready("e1").to_dict(), {"id": "e1", "decision": "not_ready"})
        self.assertEqual(no_match("e2", "absent").to_dict(), {"id": "e2", "decision": "no_match", "evidence": {"explanation": "absent"}})

    def test_fixture_helpers_match_the_go_tooling(self) -> None:
        digest = hashlib.sha256(STRIKE.read_bytes()).hexdigest()
        batches = build_subscription_requests(STRIKE, max_batch_size=1)
        self.assertEqual([b.idempotency_key for b in batches], [f"dev:{digest}:1", f"dev:{digest}:2"])
        self.assertEqual(batches[1].invocation_id, f"dev-invocation-{digest[:16]}-2")
        self.assertEqual(batches[1].evaluations[0].id, "e2")
        self.assertEqual(batches[1].evaluations[0].subscriptions[0].subscription_id, "dev-subscription-2")
        self.assertEqual(batches[0].record.record_version_id, f"dev-version-{digest[:16]}")
        self.assertEqual(expected_decisions(STRIKE), {"e1": "match", "e2": "no_match"})
        # The plugin declares max_batch_size 1: two batches, merged decisions.
        response = invoke_subscription_fixture(self.plugin, STRIKE)
        self.assertEqual([d.decision for d in response.decisions], ["match", "no_match"])
        # Metadata a fixture omits takes the development defaults of quivr plugin dev.
        record = batches[0].record
        self.assertEqual((record.source.namespace, record.source.record_key), ("dev-namespace", f"dev-record-{digest[:16]}"))
        self.assertEqual((record.accepted_at, record.provenance.origin), ("2026-01-01T00:00:00Z", "client"))

    def test_record_metadata_reaches_the_handler(self) -> None:
        body = request()
        body["record"].update({
            "source": {"namespace": "wire", "record_key": "story-42", "position": "7"},
            "accepted_at": "2026-09-29T08:00:03Z",
            "provenance": {"origin": "connector", "producer": "connector-7", "producer_version": "rss/1",
                           "connector": {"instance_id": "connector-7", "kind": "rss"}},
            "extensions": {"example.editorial": {"schema_version": "1", "data": {"author": "Jane Doe"}}},
        })
        invocation = SubscriptionInvocation(request=SubscriptionRequest.from_dict(body), manifest=self.plugin.manifest)
        self.assertEqual(invocation.source.record_key, "story-42")
        self.assertEqual(invocation.provenance.connector.kind, "rss")
        self.assertEqual(invocation.accepted_at, "2026-09-29T08:00:03Z")
        self.assertEqual(invocation.field("/provenance/origin"), "connector")
        self.assertEqual(invocation.field("/extensions/example.editorial/data/author"), "Jane Doe")
        self.assertIsNone(invocation.field("/provenance/normalization/plugin_id"))
        self.assertEqual(record_field({"a/b": {"~": 1}}, "/a~1b/~0"), 1)
        # The handler accepts the request with metadata and still decides.
        reply = self.plugin.evaluate(body)
        self.assertEqual(reply.status, 200, reply.body)
        # An origin other than client or connector is refused as an invalid request.
        body["record"]["provenance"]["origin"] = "robot"
        self.assertEqual(self.plugin.evaluate(body).status, 400)


if __name__ == "__main__":
    unittest.main()
