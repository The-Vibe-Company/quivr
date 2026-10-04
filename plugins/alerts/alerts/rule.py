"""The alerts plugin: decides every evaluation of a batch for one article.

Quivr sends one Record Version (its text Parts and metadata) with a batch of
distinct evaluations, each a saved search's expression and a Subscription's
configuration, and expects one decision per evaluation. The expression's
``kind`` combines keyword and meaning checks. Meaning uses local vectors or
one batched Jev classifier call, selected by ``meaning_check``.
"""
from __future__ import annotations

from pathlib import Path
from typing import Callable

from quivr_plugin import Decision, Evaluation, Plugin, SubscriptionInvocation, TerminalError, match, no_match, not_ready

from . import described, evidence, vectors
from .jev import Jev
from .keywords import Article, Outcome, evaluate as evaluate_tree

MANIFEST = Path(__file__).resolve().parent.parent / "quivr-plugin.yaml"

plugin = Plugin(MANIFEST)

# Whether a kind waits for enrichment when the Subscription does not say.
WAITS_BY_DEFAULT = {"keywords": False, "described": True, "meaning": True, "keywords_or_meaning": True, "keywords_and_meaning": True}

# The classifier of described alerts: Jev when TYPESAFE_API_KEY is set, else
# None. Another classifier (described.Classifier) can be installed here.
classifier: Callable[[], described.Classifier | None] = Jev.from_environment


def decide_keywords(article: Article, evaluation: Evaluation, outcome: Outcome | None = None) -> Decision:
    if outcome is None:
        outcome = evaluate_tree(evaluation.expression["match"], article)
    if not outcome.satisfied:
        return no_match(evaluation)
    keys = evidence.part_keys(outcome, [key for key, _ in article.parts])
    details = evidence.details(outcome)
    if evaluation.expression["kind"] != "keywords":
        details.update(kind=evaluation.expression["kind"], meaning_check=evaluation.expression.get("meaning_check", "jev"),
                       matched_by="keywords")
    return match(evaluation, evidence.explanation(outcome), part_keys=keys or None, details=details)


def with_keywords(decision: Decision, article: Article, evaluation: Evaluation, outcome: Outcome | None) -> Decision:
    if decision.decision != "match" or evaluation.expression["kind"] in ("described", "meaning"):
        return decision
    meaning = decision.evidence
    details = {**meaning.details, "kind": evaluation.expression["kind"],
               "meaning_check": evaluation.expression.get("meaning_check", "jev")}
    explanation = meaning.explanation
    keys = meaning.part_keys or []
    if evaluation.expression["kind"] == "keywords_and_meaning" and outcome is not None:
        details["keywords"] = evidence.details(outcome)
        explanation = (explanation + " " + evidence.explanation(outcome))[:evidence.MAX_EXPLANATION]
        supported = set(keys) | set(evidence.part_keys(outcome, [key for key, _ in article.parts]))
        keys = [part["key"] for part in article.record["parts"] if part["key"] in supported][:evidence.MAX_PART_KEYS]
    return match(evaluation, explanation, part_keys=keys or None, details=details)


def decide_described(record: dict, configuration: dict, evaluations: list[Evaluation]) -> list[Decision]:
    judge = classifier()
    if judge is None:
        raise TerminalError("described_unavailable", "Jev meaning checks need TYPESAFE_API_KEY on the alerts plugin; "
                            "use meaning_check: vectors for a local decision")
    return described.decide(evaluations, record, configuration, judge)


@plugin.subscription
def evaluate(invocation: SubscriptionInvocation) -> list[Decision]:
    record = invocation.record.to_dict()
    article = Article.prepare(record, invocation.configuration)
    decisions: dict[str, Decision] = {}
    ready_described = []
    keyword_outcomes = {}
    for evaluation in invocation.evaluations:
        kind = evaluation.expression.get("kind")
        if kind not in WAITS_BY_DEFAULT:
            # The expression schema admits only the implemented kinds.
            raise TerminalError("unsupported_kind", f"evaluation {evaluation.id} has the unsupported kind {kind!r}")
        backend = evaluation.expression.get("meaning_check", "jev" if kind == "described" else None)
        if kind != "keywords" and (backend not in ("jev", "vectors") or kind == "meaning" and backend != "vectors"):
            raise TerminalError("unsupported_meaning_check", f"evaluation {evaluation.id} has unsupported meaning_check {backend!r}")
        outside = described.outside_sources(evaluation, record) if kind != "keywords" else None
        outcome = evaluate_tree(evaluation.expression["match"], article) if kind in ("keywords_or_meaning", "keywords_and_meaning") else None
        keyword_outcomes[evaluation.id] = outcome
        if outside is not None:
            # Decided before enrichment and before the classifier: the source alone rules it out.
            decisions[evaluation.id] = no_match(evaluation, f'The article\'s source "{outside}" is not one of the alert\'s sources.')
        elif kind == "keywords_or_meaning" and outcome.satisfied:
            decisions[evaluation.id] = decide_keywords(article, evaluation, outcome)
        elif kind == "keywords_and_meaning" and not outcome.satisfied:
            decisions[evaluation.id] = no_match(evaluation, "The keyword query is not satisfied.")
        elif evaluation.configuration.get("wait_for_enrichment", WAITS_BY_DEFAULT[kind]) and not invocation.enriched:
            decisions[evaluation.id] = not_ready(evaluation, "Waiting for the article to be enriched.")
        elif kind == "keywords":
            decisions[evaluation.id] = decide_keywords(article, evaluation)
        elif backend == "vectors":
            decisions[evaluation.id] = with_keywords(vectors.decide(record, evaluation, invocation.configuration), article, evaluation, outcome)
        else:
            ready_described.append(evaluation)
    if ready_described:
        for evaluation, decision in zip(ready_described, decide_described(record, invocation.configuration, ready_described)):
            decisions[decision.id] = with_keywords(decision, article, evaluation, keyword_outcomes[evaluation.id])
    ordered = [decisions[evaluation.id] for evaluation in invocation.evaluations]
    invocation.logger.info("evaluated", extra={"evaluations": len(ordered), "described": len(ready_described),
                                               "matches": sum(d.decision == "match" for d in ordered)})
    return ordered
