"""Build subscription requests the way the core sends them and read the decisions back."""
import logging
from types import SimpleNamespace

from alerts.rule import plugin

TITLE_AND_BODY = [
    {"key": "title", "role": "title", "text": "Airbus : les salariés votent la grève"},
    {"key": "body", "role": "body", "text": "À Toulouse, l'intersyndicale d'Airbus appelle à la grève jeudi. Les syndicats demandent 5 % de hausse."},
    {"key": "caption", "role": "caption", "text": "Photo : manifestation de sport"},
]


def request(evaluations, *, parts=None, enriched=True, record=None, configuration=None, query_vectors=None):
    """A subscription request with one evaluation per (expression, configuration) pair."""
    body = {
        "corpus_id": "c1", "record_id": "r1", "record_version_id": "v1", "enriched": enriched,
        "parts": TITLE_AND_BODY if parts is None else parts,
        "source": {"namespace": "wire", "record_key": "story-1"},
        "accepted_at": "2026-01-01T00:00:00Z",
        "provenance": {"origin": "client", "producer": "newsdesk"},
    }
    body.update(record or {})
    document = {
        "invocation_id": "i1", "idempotency_key": "k1", "contribution": "subscription", "organization_id": "o1",
        "configuration": configuration or {}, "record": body,
        "evaluations": [
            {"id": f"e{i}", "expression": expression, "configuration": config,
             "subscriptions": [{"subscription_id": f"s{i}", "subscription_version_id": f"sv{i}", "saved_query_id": f"q{i}", "saved_query_version_id": f"qv{i}"}]}
            for i, (expression, config) in enumerate(evaluations, 1)
        ],
    }
    for evaluation, vector in zip(document["evaluations"], query_vectors or []):
        if vector is not None:
            evaluation["query_vector"] = vector
    return document


def handler_decisions(evaluations, **kwargs):
    """Call the owning handler with to_dict fixtures, independent of SDK field generation."""
    from alerts.rule import evaluate

    document = request(evaluations, **kwargs)
    record = document["record"]
    invocation = SimpleNamespace(
        record=SimpleNamespace(to_dict=lambda: record), enriched=record["enriched"],
        configuration=document["configuration"], logger=logging.getLogger("alerts.tests"),
        evaluations=[SimpleNamespace(**item, to_dict=lambda item=item: item) for item in document["evaluations"]],
    )
    return [decision.to_dict() for decision in evaluate(invocation)]


def reply(evaluations, **kwargs):
    return plugin.evaluate(request(evaluations, **kwargs))


def decide(match, *, subscription=None, **kwargs):
    """The decision (a dict) for one keyword query."""
    answer = reply([({"kind": "keywords", "match": match}, subscription or {})], **kwargs)
    assert answer.status == 200, answer.body
    return answer.body["decisions"][0]


def decision(match, **kwargs):
    return decide(match, **kwargs)["decision"]
