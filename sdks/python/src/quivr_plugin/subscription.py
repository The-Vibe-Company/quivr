"""The subscription Contribution (Plugin API 0.2): alert rules.

A subscription handler receives one Record Version's text Parts and a batch of
distinct evaluations (a Saved Query expression and a Subscription
configuration each) and returns one decision per evaluation::

    @plugin.subscription
    def evaluate(invocation: SubscriptionInvocation) -> list[Decision]:
        decisions = []
        for evaluation in invocation.evaluations:
            keys = [p.key for p in invocation.parts if evaluation.expression["text"] in p.text]
            if keys:
                decisions.append(match(evaluation, "The text appears.", part_keys=keys))
            else:
                decisions.append(no_match(evaluation))
        return decisions

A decision must depend only on the record, the expression and the
configurations: the core batches and deduplicates evaluations freely, and
replays with the same idempotency key must give the same answer.
"""
from __future__ import annotations

import json
import logging
from dataclasses import dataclass, field
from typing import Any

from .manifest import LoadedManifest
from .models import Decision, EvaluatedRecord, Evaluation, Evidence, RecordPart, SubscriptionRequest, SubscriptionResponse

# Evidence bounds, mirroring the monitoring engine's Match evidence bounds.
MAX_EXPLANATION_CHARACTERS = 4096
MAX_EVIDENCE_PART_KEYS = 100
MAX_EVIDENCE_DETAILS_BYTES = 16 << 10


@dataclass
class SubscriptionInvocation:
    """What a subscription handler receives for one batch."""

    request: SubscriptionRequest
    manifest: LoadedManifest
    logger: logging.Logger = field(default_factory=lambda: logging.getLogger("quivr_plugin.invocation"))

    @property
    def record(self) -> EvaluatedRecord:
        return self.request.record

    @property
    def parts(self) -> list[RecordPart]:
        """The text Parts of the evaluated Record Version, in Manifest order."""
        return self.request.record.parts

    @property
    def enriched(self) -> bool:
        """True when the Version has embedding coverage; a rule that needs it answers not_ready until then."""
        return self.request.record.enriched

    @property
    def evaluations(self) -> list[Evaluation]:
        """The distinct evaluations to decide; answer each exactly once."""
        return self.request.evaluations

    @property
    def configuration(self) -> dict[str, Any]:
        """Plugin (installer) configuration, already validated against the manifest configuration schema."""
        return self.request.configuration


def _id(evaluation: Evaluation | str) -> str:
    return evaluation if isinstance(evaluation, str) else evaluation.id


def match(evaluation: Evaluation | str, explanation: str, *, part_keys: list[str] | None = None,
          details: dict[str, Any] | None = None) -> Decision:
    """The Record Version satisfies the evaluation. The evidence is stored with the Match."""
    return Decision(id=_id(evaluation), decision="match",
                    evidence=Evidence(explanation=explanation, part_keys=part_keys, details=details))


def no_match(evaluation: Evaluation | str, explanation: str | None = None) -> Decision:
    """The Record Version does not satisfy the evaluation."""
    return Decision(id=_id(evaluation), decision="no_match",
                    evidence=Evidence(explanation=explanation) if explanation else None)


def not_ready(evaluation: Evaluation | str, explanation: str | None = None) -> Decision:
    """The evaluation cannot be decided yet (for example before enrichment); the core evaluates again later."""
    return Decision(id=_id(evaluation), decision="not_ready",
                    evidence=Evidence(explanation=explanation) if explanation else None)


def _go_json_size(value: Any) -> int:
    # Size of the value as Go's encoding/json marshals it (what the engine
    # measures): compact, UTF-8, with <, > and & escaped as \\u003c and so on,
    # and U+2028 and U+2029 escaped.
    text = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    size = len(text.encode("utf-8"))
    size += 5 * sum(text.count(c) for c in "<>&")
    size += 3 * sum(text.count(c) for c in "\u2028\u2029")
    return size


def _contains_nul(value: Any) -> bool:
    if isinstance(value, str):
        return "\x00" in value
    if isinstance(value, dict):
        return any(_contains_nul(k) or _contains_nul(v) for k, v in value.items())
    if isinstance(value, list):
        return any(_contains_nul(v) for v in value)
    return False


def response_problems(request: SubscriptionRequest, response: dict[str, Any]) -> list[str]:
    """Rules the response schema cannot express: exactly one decision per requested
    evaluation, evidence for every match, Part keys that exist, and details of at most 16 KiB."""
    problems = []
    requested = [e.id for e in request.evaluations]
    known_parts = {p.key for p in request.record.parts}
    answered: set[str] = set()
    for i, decision in enumerate(response.get("decisions", [])):
        ident = decision.get("id")
        if ident not in requested:
            problems.append(f"/decisions/{i}/id: evaluation {ident!r} was not requested")
        elif ident in answered:
            problems.append(f"/decisions/{i}/id: evaluation {ident!r} is answered more than once")
        answered.add(ident)
        evidence = decision.get("evidence")
        if decision.get("decision") == "match" and not evidence:
            problems.append(f"/decisions/{i}: a match needs evidence with an explanation")
        if evidence:
            for j, key in enumerate(evidence.get("part_keys") or []):
                if key not in known_parts:
                    problems.append(f"/decisions/{i}/evidence/part_keys/{j}: {key!r} is not a Part of the record")
            if "details" in evidence and _go_json_size(evidence["details"]) > MAX_EVIDENCE_DETAILS_BYTES:
                problems.append(f"/decisions/{i}/evidence/details: more than {MAX_EVIDENCE_DETAILS_BYTES} bytes of JSON")
            if _contains_nul(evidence):
                problems.append(f"/decisions/{i}/evidence: contains a NUL character")
    for ident in requested:
        if ident not in answered:
            problems.append(f"/decisions: evaluation {ident!r} is not answered")
    return problems


def as_response(result: Any) -> SubscriptionResponse:
    """Accept a SubscriptionResponse, a list of Decision models or dicts, or a response dict."""
    if isinstance(result, SubscriptionResponse):
        return result
    if isinstance(result, dict):
        return SubscriptionResponse.from_dict(result)
    decisions = [d if isinstance(d, Decision) else Decision.from_dict(d) for d in result]
    return SubscriptionResponse(decisions=decisions)
