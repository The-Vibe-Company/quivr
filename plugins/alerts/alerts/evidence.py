"""Match evidence of a keyword alert, kept within the protocol's evidence bounds."""
from __future__ import annotations

import json
from typing import Any

from .keywords import Outcome

MAX_EXPLANATION = 4096
MAX_PART_KEYS = 100
# The protocol bound is 16 KiB as Go serializes JSON; stay well under it.
MAX_DETAILS_BYTES = 12 << 10


def _size(value: Any) -> int:
    # Go escapes <, > and & as 6 bytes and U+2028/U+2029 as 6 bytes.
    text = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    return len(text.encode("utf-8")) + 5 * sum(text.count(c) for c in "<>&") + 3 * sum(text.count(c) for c in "\u2028\u2029")


def _quote(text: str) -> str:
    return '"' + text + '"'


def explanation(outcome: Outcome) -> str:
    items = [f"{_quote(term)} in {', '.join(keys)}" for term, keys in outcome.terms.items()]
    items += [f"{name} {_quote(value)}" for name, value in outcome.fields]
    if not items:
        return "Matched: none of the excluded terms appear."
    text = "Matched " + "; ".join(items) + "."
    return text if len(text) <= MAX_EXPLANATION else text[:MAX_EXPLANATION - 1] + "…"


def part_keys(outcome: Outcome, order: list[str]) -> list[str]:
    """Parts where a supporting term matched, in the article's Part order, at most 100."""
    matched = {key for keys in outcome.terms.values() for key in keys}
    return [key for key in order if key in matched][:MAX_PART_KEYS]


def details(outcome: Outcome) -> dict[str, Any]:
    """Structured evidence: the supporting terms with their Parts and the matched field values."""
    body = {
        "kind": "keywords",
        "terms": [{"term": term, "part_keys": keys[:MAX_PART_KEYS]} for term, keys in outcome.terms.items()],
        "fields": [{"field": name, "value": value[:256]} for name, value in outcome.fields],
    }
    # Trim the longest Part-key lists first, then drop trailing items, until the bound holds.
    while _size(body) > MAX_DETAILS_BYTES:
        longest = max(body["terms"], key=lambda t: len(t["part_keys"]), default=None)
        if longest is not None and len(longest["part_keys"]) > 1:
            longest["part_keys"] = longest["part_keys"][: len(longest["part_keys"]) // 2]
        elif body["terms"]:
            body["terms"].pop()
        else:
            body["fields"].pop()
        body["truncated"] = True
    return body
