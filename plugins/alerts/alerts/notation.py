"""The text notation of keyword queries, translated to the JSON expression Quivr stores.

    "Airbus" AND (grève OR strike) NOT sport
    author:"Jane Doe" OR source:wire

Grammar (NOT binds tighter than AND, which binds tighter than OR)::

    query   := or
    or      := and ( "OR" and )*
    and     := unary ( [ "AND" ] unary )*      juxtaposed items are all required
    unary   := "NOT" unary | primary
    primary := "(" or ")" | PHRASE | WORD | FIELD
    PHRASE  := '"' characters '"'              \\" and \\\\ escape a quote and a backslash
    FIELD   := name ":" ( WORD | PHRASE )      name is [a-z][a-z0-9_]*

Operators are upper case; ``and``, ``or`` and ``not`` in lower case are words.
A word shaped ``name:value`` is a field filter (a value starting with ``/``, as
in a URL, is not); quote it to search it as text: ``"re:Invent"``.
Use it as a library (``parse(query)``) or on the command line::

    python3 -m alerts.notation '"Airbus" AND (grève OR strike) NOT sport'

which prints the expression to pin in a saved search, or the mistake and exit status 1.
"""
from __future__ import annotations

import json
import re
import sys
from dataclasses import dataclass
from typing import Any

from .text import words

MAX_DEPTH = 6
MAX_ITEMS = 64
MAX_TEXT = 256
_FIELD = re.compile(r"([a-z][a-z0-9_]{0,63}):(.*)", re.DOTALL)
_OPERATORS = {"AND", "OR", "NOT"}


class NotationError(ValueError):
    """The query does not follow the notation; the message says why."""


@dataclass
class Token:
    kind: str  # "(", ")", "AND", "OR", "NOT", "term", "field", "end"
    text: str = ""
    name: str = ""


def _phrase(query: str, i: int) -> tuple[str, int]:
    """Read a quoted phrase starting at the quote at i; return it and the index after the closing quote."""
    out, i = [], i + 1
    while i < len(query):
        c = query[i]
        if c == "\\" and i + 1 < len(query) and query[i + 1] in '"\\':
            out.append(query[i + 1])
            i += 2
        elif c == '"':
            return "".join(out), i + 1
        else:
            out.append(c)
            i += 1
    raise NotationError("a phrase has no closing quote")


def _checked(text: str, what: str) -> str:
    if len(text) > MAX_TEXT:
        raise NotationError(f"{what} {text[:40]!r}… is longer than {MAX_TEXT} characters")
    if not words(text):
        raise NotationError(f"{what} {text!r} has no letters or digits")
    return text


def tokens(query: str) -> list[Token]:
    out, i = [], 0
    while i < len(query):
        c = query[i]
        if c.isspace():
            i += 1
        elif c in "()":
            out.append(Token(c))
            i += 1
        elif c == '"':
            text, i = _phrase(query, i)
            out.append(Token("term", _checked(text, "the phrase")))
        else:
            start = i
            while i < len(query) and not query[i].isspace() and query[i] not in '()"':
                i += 1
            word = query[start:i]
            if word in _OPERATORS:
                out.append(Token(word))
                continue
            if word.startswith("-"):
                raise NotationError(f"{word!r}: use NOT to exclude a term, as in NOT {word.lstrip('-')}")
            field = _FIELD.fullmatch(word)
            if field is None or field.group(2).startswith("/"):
                # A URL such as https://example.com is text, not a field filter.
                out.append(Token("term", _checked(word, "the word")))
                continue
            name, value = field.groups()
            if not value and i < len(query) and query[i] == '"':
                value, i = _phrase(query, i)
            if not value:
                raise NotationError(f"{name}: needs a value, as in {name}:value or {name}:\"two words\"")
            if len(value) > MAX_TEXT:
                raise NotationError(f"the value of {name}: is longer than {MAX_TEXT} characters")
            out.append(Token("field", value, name))
    out.append(Token("end"))
    return out


def _describe(token: Token) -> str:
    if token.kind == "end":
        return "end of the query"
    if token.kind in _OPERATORS:
        return token.kind
    return f"'{token.kind}'" if token.kind in "()" else repr(token.text)


class _Parser:
    def __init__(self, query: str) -> None:
        self.items = tokens(query)
        self.at = 0
        self.nesting = 0

    def enter(self) -> None:
        # Bounds recursion on pathological input; the depth rule itself is checked on the tree.
        self.nesting += 1
        if self.nesting > 4 * MAX_DEPTH + 8:
            raise NotationError(f"parentheses and NOT nest more than {MAX_DEPTH} levels deep; simplify the query")

    def peek(self) -> Token:
        return self.items[self.at]

    def take(self) -> Token:
        token = self.items[self.at]
        self.at += 1
        return token

    def or_(self) -> dict[str, Any]:
        items = [self.and_()]
        while self.peek().kind == "OR":
            self.take()
            items.append(self.and_())
        return _group("any", items)

    def and_(self) -> dict[str, Any]:
        items = [self.unary()]
        while self.peek().kind not in ("OR", ")", "end"):
            if self.peek().kind == "AND":
                self.take()
            items.append(self.unary())
        return _group("all", items)

    def unary(self) -> dict[str, Any]:
        if self.peek().kind == "NOT":
            self.take()
            self.enter()
            node = {"not": self.unary()}
            self.nesting -= 1
            return node
        return self.primary()

    def primary(self) -> dict[str, Any]:
        token = self.take()
        if token.kind == "(":
            self.enter()
            node = self.or_()
            self.nesting -= 1
            if self.take().kind != ")":
                raise NotationError("a group has no closing parenthesis")
            return node
        if token.kind == "term":
            return {"term": token.text}
        if token.kind == "field":
            return {"field": token.name, "equals": token.text}
        if token.kind == "end":
            previous = self.items[self.at - 2].kind if self.at >= 2 else ""
            raise NotationError(f"unexpected end of the query after {previous}; add a term" if previous else "the query is empty")
        raise NotationError(f"unexpected {_describe(token)}")


def _group(operator: str, items: list[dict[str, Any]]) -> dict[str, Any]:
    if len(items) == 1:
        return items[0]
    flat: list[dict[str, Any]] = []
    for item in items:
        flat.extend(item[operator] if operator in item else [item])
    if len(flat) > MAX_ITEMS:
        raise NotationError(f"a group has {len(flat)} items; at most {MAX_ITEMS} are allowed")
    return {operator: flat}


def _depth(node: dict[str, Any]) -> int:
    if "not" in node:
        return 1 + _depth(node["not"])
    for operator in ("all", "any"):
        if operator in node:
            return 1 + max(_depth(child) for child in node[operator])
    return 0


def parse(query: str) -> dict[str, Any]:
    """Translate a query in the text notation to a keywords expression; raise NotationError on a mistake."""
    parser = _Parser(query)
    if parser.peek().kind == "end":
        raise NotationError("the query is empty")
    node = parser.or_()
    if parser.peek().kind != "end":
        raise NotationError(f"unexpected {_describe(parser.peek())}")
    if _depth(node) > MAX_DEPTH:
        raise NotationError(f"groups and NOT nest more than {MAX_DEPTH} levels deep; simplify the query")
    return {"kind": "keywords", "match": node}


def main(argv: list[str]) -> int:
    if len(argv) != 1:
        print('usage: python3 -m alerts.notation \'<query>\', for example \'"Airbus" AND (grève OR strike) NOT sport\'', file=sys.stderr)
        return 2
    try:
        expression = parse(argv[0])
    except NotationError as error:
        print(f"invalid query: {error}", file=sys.stderr)
        return 1
    print(json.dumps(expression, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
