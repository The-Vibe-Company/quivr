"""The keywords alert kind: a boolean query tree over an article's text and metadata.

A node is one of:

- ``{"term": "..."}``: a word or an exact phrase in a searched Part;
- ``{"field": "...", "equals": ...}``: a metadata field has a value;
- ``{"all": [nodes]}``, ``{"any": [nodes]}``, ``{"not": node}``.

The decision depends only on the article, the expression and the
configurations. Evidence lists the terms and field values that support the
match: those of the satisfied branches, never those under a ``not``.
"""
from __future__ import annotations

import json
import logging
from dataclasses import dataclass, field
from typing import Any

from quivr_plugin import record_field

from .text import WordIndex, same_value, words

log = logging.getLogger("alerts.keywords")

# Field names every installation knows: they read the metadata Quivr itself
# sends with every article. The plugin configuration adds names (for example
# author or category, mapped into an extension) and may replace these.
BUILT_IN_FIELDS = {
    "source": "/source/namespace",
    "producer": "/provenance/producer",
    "origin": "/provenance/origin",
    "connector": "/provenance/connector/instance_id",
    "connector_kind": "/provenance/connector/kind",
}
DEFAULT_TEXT_ROLES = ("title", "body")


@dataclass
class Article:
    """One Record Version, prepared once per request for every evaluation of the batch."""

    record: dict[str, Any]
    parts: list[tuple[str, WordIndex]]
    fields: dict[str, str]

    @classmethod
    def prepare(cls, record: dict[str, Any], configuration: dict[str, Any]) -> "Article":
        roles = set(configuration.get("text_roles") or DEFAULT_TEXT_ROLES)
        parts = [(p["key"], WordIndex(p["text"])) for p in record.get("parts", []) if p["role"] in roles]
        return cls(record=record, parts=parts, fields={**BUILT_IN_FIELDS, **configuration.get("fields", {})})

    def value(self, name: str) -> Any:
        pointer = name if name.startswith("/") else self.fields.get(name)
        if pointer is None:
            log.warning("field %r is not mapped; map it in the plugin configuration under fields", name)
            return None
        return record_field(self.record, pointer)


@dataclass
class Outcome:
    satisfied: bool
    # Supporting terms in query order, each with the Part keys where it matched.
    terms: dict[str, list[str]] = field(default_factory=dict)
    # Supporting field filters: (field, the article's value).
    fields: list[tuple[str, str]] = field(default_factory=list)

    def absorb(self, other: "Outcome") -> None:
        for term, keys in other.terms.items():
            self.terms.setdefault(term, keys)
        for item in other.fields:
            if item not in self.fields:
                self.fields.append(item)


def _scalar(value: Any) -> str | None:
    if isinstance(value, str):
        return same_value(value)
    if isinstance(value, (bool, int, float)):
        return json.dumps(value)
    return None


def _display(value: Any) -> str:
    return value if isinstance(value, str) else json.dumps(value)


def evaluate(node: dict[str, Any], article: Article) -> Outcome:
    if "term" in node:
        phrase = words(node["term"])
        keys = [key for key, index in article.parts if index.contains(phrase)]
        return Outcome(True, terms={node["term"]: keys}) if keys else Outcome(False)
    if "field" in node:
        value = article.value(node["field"])
        wanted = _scalar(node["equals"])
        for candidate in value if isinstance(value, list) else [value]:
            if wanted is not None and _scalar(candidate) == wanted:
                return Outcome(True, fields=[(node["field"], _display(candidate))])
        return Outcome(False)
    if "not" in node:
        return Outcome(not evaluate(node["not"], article).satisfied)
    if "all" in node:
        outcome = Outcome(True)
        for child in node["all"]:
            result = evaluate(child, article)
            if not result.satisfied:
                return Outcome(False)
            outcome.absorb(result)
        return outcome
    if "any" in node:
        outcome = Outcome(False)
        for child in node["any"]:
            result = evaluate(child, article)
            if result.satisfied:
                outcome.satisfied = True
                outcome.absorb(result)
        return outcome
    raise ValueError(f"unknown node {sorted(node)}")  # the expression schema admits no other node
