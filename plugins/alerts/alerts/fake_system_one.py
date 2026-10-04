"""A deterministic stand-in for TypeSafe's System One API, for tests only.

It never calls TypeSafe. It answers Noul questions by topics: each topic is a
set of words in English and French, so a rephrased or translated article still
shares the topics of a description. The noul is 0.92 when the article has
every topic of the description, 0.35 when it has some, and 0.03 otherwise.
Descriptions without a known topic get 0.03.

Run it: ``python3 -m alerts.fake_system_one --port 8765 [--key test-key]``,
then point the plugin at it with ``TYPESAFE_API_URL=http://127.0.0.1:8765/v1/systemone``.

- ``POST /v1/systemone`` answers like System One. A wrong bearer key is 401.
- ``GET /requests`` lists what it received: for each request, the model, the
  start of the article text, the descriptions asked, and the body size in bytes.
- ``POST /fail`` with ``{"status": 429, "times": 1}`` makes the next calls
  answer that status.
"""
from __future__ import annotations

import argparse
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from .text import words

TOPICS = {
    "strike": {"strike", "strikes", "walkout", "stoppage", "greve", "greves", "debrayage"},
    "port": {"port", "ports", "harbour", "harbor", "docks", "dockers", "dockworkers"},
    "visa": {"visa", "visas", "consulate", "consulates", "consulat", "consulats"},
    "diplomacy": {"diplomatic", "diplomacy", "tensions", "tension", "ambassador", "diplomatique", "diplomatie", "ambassadeur"},
    "flood": {"flood", "floods", "flooding", "inondation", "inondations", "crue", "crues"},
    "football": {"football", "soccer", "goal", "goals", "championnat", "league"},
}


def topics(text: str) -> set[str]:
    found = set(words(text))
    return {name for name, vocabulary in TOPICS.items() if found & vocabulary}


def noul(article: str, description: str) -> float:
    wanted = topics(description)
    if not wanted:
        return 0.03
    have = topics(article) & wanted
    return 0.92 if have == wanted else 0.35 if have else 0.03


def _text(value: Any) -> str:
    if isinstance(value, dict):
        return " ".join(_text(v) for v in value.values())
    if isinstance(value, list):
        return " ".join(_text(v) for v in value)
    return str(value)


class FakeSystemOne:
    def __init__(self, key: str = "test-key") -> None:
        self.key = key
        self.requests: list[dict[str, Any]] = []
        self.failures: list[int] = []
        self.lock = threading.Lock()

    def answer(self, authorization: str, raw: bytes) -> tuple[int, dict[str, Any]]:
        with self.lock:
            if self.failures:
                return self.failures.pop(0), {"detail": {"error_type": "fake", "message": "a failure the test asked for"}}
        if authorization != "Bearer " + self.key:
            return 401, {"detail": {"error_type": "authentication_error", "message": "Cannot authenticate with the server."}}
        body = json.loads(raw)
        article = _text(body["state"])
        answers, asked = {}, []
        for qid, question in body["questions"].items():
            instructions = question["instructions"]
            description = instructions.get("alert", "") if isinstance(instructions, dict) else instructions
            asked.append(description)
            answers[qid] = {"type": "noul", "noul": noul(article, description)}
        with self.lock:
            self.requests.append({"model": body.get("model"), "article": article[:500], "descriptions": asked, "bytes": len(raw)})
        return 200, {"model": body.get("model"), "answers": answers, "usage": {"input_tokens": len(raw) // 4, "output_tokens": len(answers)}}

    def server(self, host: str = "127.0.0.1", port: int = 0) -> ThreadingHTTPServer:
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args: Any) -> None:
                pass

            def reply(self, status: int, payload: Any) -> None:
                data = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self) -> None:
                if self.path == "/requests":
                    with fake.lock:
                        self.reply(200, {"requests": list(fake.requests)})
                else:
                    self.reply(404, {"detail": "not found"})

            def do_POST(self) -> None:
                raw = self.rfile.read(int(self.headers.get("Content-Length") or 0))
                if self.path == "/fail":
                    order = json.loads(raw)
                    with fake.lock:
                        fake.failures.extend([int(order["status"])] * int(order.get("times", 1)))
                    self.reply(200, {})
                elif self.path == "/v1/systemone":
                    self.reply(*fake.answer(self.headers.get("Authorization", ""), raw))
                else:
                    self.reply(404, {"detail": "not found"})

        return ThreadingHTTPServer((host, port), Handler)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8765)
    parser.add_argument("--key", default="test-key", help="the bearer key it accepts (a test value, never a real key)")
    args = parser.parse_args()
    FakeSystemOne(args.key).server(args.host, args.port).serve_forever()


if __name__ == "__main__":
    main()
