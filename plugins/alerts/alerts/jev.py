"""TypeSafe's Jev as the classifier of described alerts (System One API).

One request carries the article once, as the state, and one Noul question (a
yes/no question answered with the probability of yes) per description. A
request is split only when its body would exceed ``MAX_REQUEST_BYTES``, under
TypeSafe's request size limit.

The API key is read from ``TYPESAFE_API_KEY`` only. Error messages never
include TypeSafe's response body or the key.
"""
from __future__ import annotations

import http.client
import json
import os
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Callable

from quivr_plugin import RetryableError, TerminalError

DEFAULT_URL = "https://api.typesafe.ai/v1/systemone"
# Pinned: an alias such as jev-latest could change answers, and the threshold, under us.
MODEL = "jev-1.13.0"
# TypeSafe caps requests at about 128 KB; stay under it.
MAX_REQUEST_BYTES = 120_000
# Split requests run in parallel, so a batch stays within the plugin's declared timeout_ms.
TIMEOUT_SECONDS = 15.0
MAX_PARALLEL_REQUESTS = 4

QUESTION = "Is the article in `article` about what `alert` describes? The article may use other words or another language."
CRITERIA = {
    "true": "The article's main subject, or a substantial part of it, is what `alert` describes.",
    "false": "The article is about something else, or mentions it only in passing.",
}


def _body(state: dict[str, Any], descriptions: list[str]) -> dict[str, Any]:
    questions = {f"q{i}": {"type": "noul", "instructions": {"alert": d, "question": QUESTION}, "criteria": CRITERIA}
                 for i, d in enumerate(descriptions)}
    return {"model": MODEL, "state": {"article": state}, "questions": questions}


def _encode(body: dict[str, Any]) -> bytes:
    return json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def chunks(state: dict[str, Any], descriptions: list[str]) -> list[list[str]]:
    """Descriptions grouped into as few requests as the size limit allows, in order."""
    out: list[list[str]] = []
    for description in descriptions:
        if out and len(_encode(_body(state, out[-1] + [description]))) <= MAX_REQUEST_BYTES:
            out[-1].append(description)
        else:
            out.append([description])
    return out


class Jev:
    """The System One client. ``judge`` returns the Noul of every description."""

    name = "Jev"
    model = MODEL

    def __init__(self, key: str, url: str = DEFAULT_URL, *, timeout: float = TIMEOUT_SECONDS,
                 opener: Callable[..., Any] = urllib.request.urlopen) -> None:
        self._key = key
        self._url = url
        self._timeout = timeout
        self._open = opener

    @classmethod
    def from_environment(cls, environ: dict[str, str] | None = None) -> "Jev | None":
        """A client for TYPESAFE_API_KEY (and TYPESAFE_API_URL, default the TypeSafe API), or None without a key."""
        env = os.environ if environ is None else environ
        key = env.get("TYPESAFE_API_KEY", "").strip()
        if not key:
            return None
        return cls(key, env.get("TYPESAFE_API_URL", "").strip() or DEFAULT_URL)

    def judge(self, state: dict[str, Any], descriptions: list[str]) -> dict[str, float]:
        groups = chunks(state, descriptions)
        if len(groups) == 1:
            return self._ask(state, groups[0])
        scores: dict[str, float] = {}
        with ThreadPoolExecutor(max_workers=min(len(groups), MAX_PARALLEL_REQUESTS)) as pool:
            for answer in pool.map(lambda group: self._ask(state, group), groups):
                scores.update(answer)
        return scores

    def _ask(self, state: dict[str, Any], descriptions: list[str]) -> dict[str, float]:
        request = urllib.request.Request(self._url, data=_encode(_body(state, descriptions)), method="POST", headers={
            "Authorization": "Bearer " + self._key, "Content-Type": "application/json", "Accept": "application/json"})
        try:
            with self._open(request, timeout=self._timeout) as response:
                answer = json.loads(response.read())
        except urllib.error.HTTPError as error:
            error.close()
            raise _classify(error.code) from None
        except (OSError, http.client.HTTPException) as error:
            # URLError, timeouts, TLS and connection failures, and answers cut short.
            reason = getattr(error, "reason", error)
            raise RetryableError("classifier_unavailable", f"TypeSafe could not be reached ({type(reason).__name__}); the core retries.") from None
        except ValueError:
            raise TerminalError("classifier_invalid_answer", "TypeSafe answered with a body that is not JSON.") from None
        return _scores(answer, descriptions)


def _classify(status: int) -> Exception:
    if status in (401, 403):
        return TerminalError("classifier_unauthorized",
                             f"TypeSafe refused the API key (HTTP {status}): check TYPESAFE_API_KEY on the alerts plugin.")
    if status in (408, 429) or status >= 500:
        return RetryableError("classifier_unavailable", f"TypeSafe answered HTTP {status}; the core retries.")
    return TerminalError("classifier_refused_request", f"TypeSafe refused the request (HTTP {status}).")


def _scores(answer: Any, descriptions: list[str]) -> dict[str, float]:
    answers = answer.get("answers") if isinstance(answer, dict) else None
    scores = {}
    for i, description in enumerate(descriptions):
        item = answers.get(f"q{i}") if isinstance(answers, dict) else None
        noul = item.get("noul") if isinstance(item, dict) else None
        if isinstance(noul, bool) or not isinstance(noul, (int, float)) or not 0 <= noul <= 1:
            raise TerminalError("classifier_invalid_answer", f"TypeSafe's answer has no valid noul for question q{i}.")
        scores[description] = float(noul)
    return scores
