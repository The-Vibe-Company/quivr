"""The alerts plugin: decides every evaluation of a batch for one article.

Quivr sends one Record Version (its text Parts and metadata) with a batch of
distinct evaluations, each a saved search's expression and a Subscription's
configuration, and expects one decision per evaluation. The expression's
``kind`` picks the alert kind: ``keywords`` is decided here, and every
``described`` evaluation of the batch is decided by one classifier call.
"""
from __future__ import annotations

from pathlib import Path
from typing import Callable

from quivr_plugin import Decision, Evaluation, Plugin, SubscriptionInvocation, TerminalError, match, no_match, not_ready

from . import described, evidence
from .jev import Jev
from .keywords import Article, evaluate as evaluate_tree

MANIFEST = Path(__file__).resolve().parent.parent / "quivr-plugin.yaml"

plugin = Plugin(MANIFEST)

# Whether a kind waits for enrichment when the Subscription does not say.
WAITS_BY_DEFAULT = {"keywords": False, "described": True}

# The classifier of described alerts: Jev when TYPESAFE_API_KEY is set, else
# None. Another classifier (described.Classifier) can be installed here.
classifier: Callable[[], described.Classifier | None] = Jev.from_environment


def decide_keywords(article: Article, evaluation: Evaluation) -> Decision:
    outcome = evaluate_tree(evaluation.expression["match"], article)
    if not outcome.satisfied:
        return no_match(evaluation)
    keys = evidence.part_keys(outcome, [key for key, _ in article.parts])
    return match(evaluation, evidence.explanation(outcome), part_keys=keys or None, details=evidence.details(outcome))


def decide_described(record: dict, configuration: dict, evaluations: list[Evaluation]) -> list[Decision]:
    judge = classifier()
    if judge is None:
        raise TerminalError("described_unavailable", "described alerts need TYPESAFE_API_KEY on the alerts plugin; "
                            "an installation without it leaves described out of the pin's kinds")
    return described.decide(evaluations, record, configuration, judge)


@plugin.subscription
def evaluate(invocation: SubscriptionInvocation) -> list[Decision]:
    record = invocation.record.to_dict()
    article = Article.prepare(record, invocation.configuration)
    decisions: dict[str, Decision] = {}
    ready_described = []
    for evaluation in invocation.evaluations:
        kind = evaluation.expression.get("kind")
        if kind not in WAITS_BY_DEFAULT:
            # The expression schema admits only the implemented kinds.
            raise TerminalError("unsupported_kind", f"evaluation {evaluation.id} has the unsupported kind {kind!r}")
        if evaluation.configuration.get("wait_for_enrichment", WAITS_BY_DEFAULT[kind]) and not invocation.enriched:
            decisions[evaluation.id] = not_ready(evaluation, "Waiting for the article to be enriched.")
        elif kind == "keywords":
            decisions[evaluation.id] = decide_keywords(article, evaluation)
        else:
            ready_described.append(evaluation)
    if ready_described:
        for decision in decide_described(record, invocation.configuration, ready_described):
            decisions[decision.id] = decision
    ordered = [decisions[evaluation.id] for evaluation in invocation.evaluations]
    invocation.logger.info("evaluated", extra={"evaluations": len(ordered), "described": len(ready_described),
                                               "matches": sum(d.decision == "match" for d in ordered)})
    return ordered
