"""Alert rubric and error mapping for the shared System One transport."""
from __future__ import annotations

import os
import time
from typing import Any

from quivr_plugin import RetryableError, TerminalError
from quivr_plugin.system_one import MODEL, URL, SystemOne

TIMEOUT_SECONDS = 15.0

QUESTION = "Is the article in `article` about what `alert` describes? The article may use other words or another language."
CRITERIA = {
    "true": "The article's main subject, or a substantial part of it, is what `alert` describes.",
    "false": "The article is about something else, or mentions it only in passing.",
}



class Jev:
    name = "Jev"
    model = MODEL

    def __init__(self, key: str, url: str = URL, *, timeout: float = TIMEOUT_SECONDS) -> None:
        self._client = SystemOne(key, url)
        self._timeout = timeout

    @classmethod
    def from_environment(cls, environ: dict[str, str] | None = None) -> "Jev | None":
        env = os.environ if environ is None else environ
        key = env.get("TYPESAFE_API_KEY", "").strip()
        if not key:
            return None
        return cls(key, env.get("TYPESAFE_API_URL", "").strip() or URL)

    def judge(self, state: dict[str, Any], descriptions: list[str]) -> dict[str, float]:
        questions = {f"q{i}": {"type": "noul", "instructions": {"alert": description, "question": QUESTION},
                                "criteria": CRITERIA} for i, description in enumerate(descriptions)}
        result = self._client.judge({"article": state}, questions, time.monotonic() + self._timeout)
        if result.reason:
            if result.status in (401, 403):
                raise TerminalError("classifier_unauthorized", "TypeSafe refused the API key: check TYPESAFE_API_KEY on the alerts plugin.")
            if result.retryable:
                raise RetryableError("classifier_unavailable", f"TypeSafe unavailable ({result.reason}); the core retries.")
            if result.reason in ("invalid answer", "response size bound"):
                raise TerminalError("classifier_invalid_answer", "TypeSafe answered without valid bounded Noul scores.")
            raise TerminalError("classifier_refused_request", f"TypeSafe request failed ({result.reason}).")
        return {description: result.scores[f"q{i}"] for i, description in enumerate(descriptions)}
