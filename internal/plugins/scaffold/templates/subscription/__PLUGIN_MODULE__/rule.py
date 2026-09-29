"""An alert rule: an article matches when a phrase appears in one of its text Parts.

Quivr sends one article (a Record Version's text Parts) with a batch of
distinct evaluations, each a saved search's expression and a Subscription's
configuration, and expects one decision per evaluation. A match carries
evidence: an explanation, the keys of the Parts that support it and small
structured details. Quivr stores that evidence with the Match and sends it in
the alert.

A decision must depend only on the article, the expression and the
configurations: Quivr batches and deduplicates evaluations freely and may
replay a batch.
"""
from __future__ import annotations

from pathlib import Path

from quivr_plugin import Decision, Evaluation, Plugin, RecordPart, SubscriptionInvocation, TerminalError, match, no_match

MANIFEST = Path(__file__).resolve().parent.parent / "quivr-plugin.yaml"

plugin = Plugin(MANIFEST)


def matching_parts(parts: list[RecordPart], text: str, *, case_sensitive: bool = False) -> list[str]:
    """Keys of the Parts whose text contains the phrase."""
    if case_sensitive:
        return [p.key for p in parts if text in p.text]
    needle = text.casefold()
    return [p.key for p in parts if needle in p.text.casefold()]


def decide(parts: list[RecordPart], evaluation: Evaluation) -> Decision:
    """Decide one evaluation for one article."""
    expression = evaluation.expression
    if expression.get("kind") != "substring":
        # The expression schema only admits substring; a new kind needs a new branch here.
        raise TerminalError("unsupported_kind", f"evaluation {evaluation.id} has the unsupported kind {expression.get('kind')!r}")
    text = expression["text"]
    case_sensitive = evaluation.configuration.get("case_sensitive", False)
    keys = matching_parts(parts, text, case_sensitive=case_sensitive)
    if not keys:
        return no_match(evaluation)
    return match(
        evaluation,
        f"{text!r} appears in {len(keys)} of {len(parts)} text Parts.",
        part_keys=keys[:100],
        details={"kind": "substring", "text": text, "case_sensitive": case_sensitive},
    )


@plugin.subscription
def evaluate(invocation: SubscriptionInvocation) -> list[Decision]:
    decisions = [decide(invocation.parts, evaluation) for evaluation in invocation.evaluations]
    invocation.logger.info("evaluated", extra={"evaluations": len(decisions), "matches": sum(d.decision == "match" for d in decisions)})
    return decisions
