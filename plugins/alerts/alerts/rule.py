"""The alerts plugin: decides every evaluation of a batch for one article.

Quivr sends one Record Version (its text Parts and metadata) with a batch of
distinct evaluations, each a saved search's expression and a Subscription's
configuration, and expects one decision per evaluation. The expression's
``kind`` picks the alert kind; this version implements ``keywords``.
"""
from __future__ import annotations

from pathlib import Path

from quivr_plugin import Decision, Evaluation, Plugin, SubscriptionInvocation, TerminalError, match, no_match, not_ready

from . import evidence
from .keywords import Article, evaluate as evaluate_tree

MANIFEST = Path(__file__).resolve().parent.parent / "quivr-plugin.yaml"

plugin = Plugin(MANIFEST)

# Whether a kind waits for enrichment when the Subscription does not say.
WAITS_BY_DEFAULT = {"keywords": False}


def decide_keywords(article: Article, evaluation: Evaluation) -> Decision:
    outcome = evaluate_tree(evaluation.expression["match"], article)
    if not outcome.satisfied:
        return no_match(evaluation)
    keys = evidence.part_keys(outcome, [key for key, _ in article.parts])
    return match(evaluation, evidence.explanation(outcome), part_keys=keys or None, details=evidence.details(outcome))


KINDS = {"keywords": decide_keywords}


def decide(article: Article, evaluation: Evaluation, enriched: bool) -> Decision:
    kind = evaluation.expression.get("kind")
    if kind not in KINDS:
        # The expression schema admits only the implemented kinds.
        raise TerminalError("unsupported_kind", f"evaluation {evaluation.id} has the unsupported kind {kind!r}")
    if evaluation.configuration.get("wait_for_enrichment", WAITS_BY_DEFAULT[kind]) and not enriched:
        return not_ready(evaluation, "Waiting for the article to be enriched.")
    return KINDS[kind](article, evaluation)


@plugin.subscription
def evaluate(invocation: SubscriptionInvocation) -> list[Decision]:
    article = Article.prepare(invocation.record.to_dict(), invocation.configuration)
    decisions = [decide(article, evaluation, invocation.enriched) for evaluation in invocation.evaluations]
    invocation.logger.info("evaluated", extra={"evaluations": len(decisions), "matches": sum(d.decision == "match" for d in decisions)})
    return decisions
