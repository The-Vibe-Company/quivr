"""Text folding and word matching shared by keyword terms and field filters.

Text is compared folded: compatibility forms decomposed (NFKD), accents and
other combining marks removed, a few ligatures spelled out, then case folded.
A word is a run of Unicode letters and digits; everything else (spaces,
punctuation, apostrophes, hyphens) separates words.
"""
from __future__ import annotations

import re
import unicodedata
from collections import defaultdict

_WORD = re.compile(r"[^\W_]+")
# Ligatures NFKD keeps as single letters.
_LIGATURES = str.maketrans({"œ": "oe", "Œ": "OE", "æ": "ae", "Æ": "AE", "ø": "o", "Ø": "O", "đ": "d", "Đ": "D", "ł": "l", "Ł": "L"})


def fold(text: str) -> str:
    decomposed = unicodedata.normalize("NFKD", text.translate(_LIGATURES))
    return "".join(c for c in decomposed if not unicodedata.combining(c)).casefold()


def words(text: str) -> list[str]:
    """The folded words of a text, in order."""
    return _WORD.findall(fold(text))


def same_value(text: str) -> str:
    """A field value folded for whole-value comparison: folded, with runs of spaces collapsed."""
    return " ".join(fold(text).split())


class WordIndex:
    """Positions of every word of one text, to find words and phrases on word boundaries."""

    def __init__(self, text: str) -> None:
        self._words = words(text)
        self._positions: dict[str, list[int]] = defaultdict(list)
        for i, word in enumerate(self._words):
            self._positions[word].append(i)

    def contains(self, phrase: list[str]) -> bool:
        """True when the words of phrase appear consecutively, in order."""
        if not phrase:
            return False
        n = len(phrase)
        for start in self._positions.get(phrase[0], ()):
            if self._words[start:start + n] == phrase:
                return True
        return False
