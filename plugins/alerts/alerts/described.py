"""The described alert kind: a plain-language description judged by a classifier.

Every described evaluation of a batch is decided by one classifier call: the
article is sent once as the state, with one yes/no question per distinct
description. There is no keyword pre-filter. A match is a score at or above
the threshold; the classifier's own confidence, when it has one, is never used.

The classifier sits behind the small ``Classifier`` protocol so another one
(for example an in-house model) can replace TypeSafe's Jev without touching the
rule. It raises the SDK's ``RetryableError`` or ``TerminalError`` when it
cannot decide; unavailability never becomes a ``no_match``.
"""
from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any, Protocol

from quivr_plugin import Decision, Evaluation, match, no_match, record_field

from .keywords import BUILT_IN_FIELDS, DEFAULT_TEXT_ROLES

# Calibrated on calibration/set.json (see README.md, "Described alerts").
DEFAULT_THRESHOLD = 0.5
# The serialized state stays under this many bytes: far under TypeSafe's
# 32k-token budget for the state plus the longest question, and small enough
# that one request holds the state with many descriptions.
MAX_STATE_BYTES = 48_000
MAX_TITLE_CHARACTERS = 1_000
MAX_FIELD_CHARACTERS = 256
MAX_FIELD_ITEMS = 16
MAX_PART_KEYS = 100


class Classifier(Protocol):
    """Judges how well an article fits each description."""

    name: str
    model: str

    def judge(self, state: dict[str, Any], descriptions: list[str]) -> dict[str, float]:
        """A score in [0, 1] (the probability that the article fits) for every description."""
        ...


@dataclass
class State:
    """What the classifier sees of one article, and which Parts that is."""

    value: dict[str, Any]
    part_keys: list[str]
    truncated: bool


def _size(value: Any) -> int:
    return len(json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))


def _cut(text: str, budget: int) -> str:
    """The longest prefix of text, ending on a word boundary, whose JSON string takes at most budget bytes."""
    if _size(text) <= budget:
        return text
    lo, hi = 0, len(text)
    while lo < hi:
        mid = (lo + hi + 1) // 2
        if _size(text[:mid]) <= budget:
            lo = mid
        else:
            hi = mid - 1
    prefix = text[:lo]
    boundary = prefix.rstrip().rfind(" ")
    return prefix[:boundary].rstrip() if boundary > 0 else prefix


def _metadata(value: Any) -> Any:
    if isinstance(value, str):
        return value[:MAX_FIELD_CHARACTERS] or None
    if isinstance(value, (bool, int, float)):
        return value
    if isinstance(value, list):
        items = [_metadata(v) for v in value if not isinstance(v, (list, dict))][:MAX_FIELD_ITEMS]
        return [v for v in items if v is not None] or None
    return None


def article_state(record: dict[str, Any], configuration: dict[str, Any]) -> State:
    """The article as the classifier sees it: title, source and mapped metadata, then the text.

    Text comes from the Parts whose role is in ``text_roles``, in Part order.
    Title Parts go to ``title``; the others are joined into ``text``, which is
    cut at a word boundary so the state stays under ``MAX_STATE_BYTES``.
    """
    roles = set(configuration.get("text_roles") or DEFAULT_TEXT_ROLES)
    parts = [p for p in record.get("parts", []) if p["role"] in roles and p["text"].strip()]
    titles = [p for p in parts if p["role"] == "title"]
    bodies = [p for p in parts if p["role"] != "title"]
    value: dict[str, Any] = {}
    title = " ".join(" ".join(p["text"].split()) for p in titles)
    if title:
        value["title"] = title[:MAX_TITLE_CHARACTERS]
    # The source and every field the installer mapped (such as author or category).
    for name, pointer in {"source": BUILT_IN_FIELDS["source"], **configuration.get("fields", {})}.items():
        field = _metadata(record_field(record, pointer))
        if field is not None:
            value[name] = field
    keys = [p["key"] for p in titles]
    truncated = False
    text = ""
    budget = MAX_STATE_BYTES - _size({**value, "text": ""})
    for part in bodies:
        joined = (text + "\n\n" + part["text"]) if text else part["text"]
        if _size(joined) <= budget:
            text = joined
            keys.append(part["key"])
            continue
        kept = _cut(joined, budget)
        if len(kept) > len(text) + 2:
            keys.append(part["key"])
            text = kept
        truncated = True
        break
    if text:
        value["text"] = text
    return State(value=value, part_keys=keys[:MAX_PART_KEYS], truncated=truncated)


def description_of(evaluation: Evaluation) -> str:
    """The description as it is asked: runs of spaces collapsed, so trivially different spellings share a question."""
    return " ".join(evaluation.expression["description"].split())


def threshold_of(evaluation: Evaluation, configuration: dict[str, Any]) -> float:
    default = configuration.get("described", {}).get("threshold", DEFAULT_THRESHOLD)
    return float(evaluation.configuration.get("threshold", default))


def decide(evaluations: list[Evaluation], record: dict[str, Any], configuration: dict[str, Any], classifier: Classifier) -> list[Decision]:
    """Decide every described evaluation of a batch with one classifier judgement."""
    state = article_state(record, configuration)
    descriptions = sorted({description_of(e) for e in evaluations})
    scores = classifier.judge(state.value, descriptions)
    decisions = []
    for evaluation in evaluations:
        score = round(scores[description_of(evaluation)], 4)
        threshold = threshold_of(evaluation, configuration)
        who = f"{classifier.name} ({classifier.model})"
        if score < threshold:
            decisions.append(no_match(evaluation, f"{who} scored the article {score:.2f}, below the threshold {threshold:.2f}."))
            continue
        details = {"kind": "described", "classifier": classifier.name, "model": classifier.model, "score": score,
                   "threshold": threshold, "truncated": state.truncated}
        explanation = f"{who} judged that the article fits the description: score {score:.2f}, threshold {threshold:.2f}."
        decisions.append(match(evaluation, explanation, part_keys=state.part_keys or None, details=details))
    return decisions
