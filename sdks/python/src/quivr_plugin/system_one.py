"""Bounded System One Noul transport shared by classification and retrieval plugins."""
from __future__ import annotations

import datetime
import email.utils
import http.client
import ipaddress
import json
import math
import socket
import subprocess
import sys
import threading
import time
import urllib.parse
from dataclasses import dataclass, field

MODEL = "jev-1.13.0"
URL = "https://api.typesafe.ai/v1/systemone"
CENTS_PER_TOKEN = 0.042 * 100 / 1_000_000
MAX_TOKENS = 65536
MAX_REQUEST_BYTES = 120_000
MAX_RESPONSE_BYTES = 128_000


@dataclass
class Result:
    scores: dict[str, float] = field(default_factory=dict)
    paid_calls: int = 0
    input_tokens: int = 0
    estimated_tokens: int = 0
    reason: str = ""
    model: str = ""
    status: int = 0
    retryable: bool = False

    @property
    def cost_cents(self) -> float:
        return self.input_tokens * CENTS_PER_TOKEN


def payload(state: dict, questions: dict) -> bytes:
    return json.dumps({"model": MODEL, "state": state, "questions": questions},
                      ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def batches(state: dict, questions: dict) -> list[dict]:
    """Fit the complete UTF-8 body under the provider cap, preserving question IDs."""
    groups: list[dict] = []
    for name, question in questions.items():
        if len(payload(state, {name: question})) > MAX_REQUEST_BYTES:
            raise ValueError("single question exceeds request size bound")
        if groups and len(payload(state, {**groups[-1], name: question})) <= MAX_REQUEST_BYTES:
            groups[-1][name] = question
        else:
            groups.append({name: question})
    return groups


def retry_after(value: str | None) -> float:
    if not value:
        return 0.05
    try:
        seconds = float(value)
        return max(0, seconds) if math.isfinite(seconds) else math.inf
    except ValueError:
        try:
            date = email.utils.parsedate_to_datetime(value)
            return max(0, (date - datetime.datetime.now(datetime.timezone.utc)).total_seconds())
        except (ValueError, TypeError, OverflowError):
            return math.inf


# Isolate libc DNS in a cancellable process: socket timeouts do not bound getaddrinfo.
# Resolver stdin carries only the hostname and port, never credentials or request text.
_DNS = "import json,socket,sys; h,p=json.load(sys.stdin); print(json.dumps(socket.getaddrinfo(h,p,type=socket.SOCK_STREAM)))"


def connect_socket(address: tuple, deadline: float, connected: list, source_address=None):
    host, port = address
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError
    try:
        ipaddress.ip_address(host)
    except ValueError:
        try:
            answer = subprocess.run([sys.executable, "-I", "-c", _DNS], input=json.dumps([host, port]),
                                    capture_output=True, text=True, check=True, timeout=remaining)
            addresses = json.loads(answer.stdout)
        except subprocess.TimeoutExpired:
            raise TimeoutError from None  # subprocess.run kills and reaps the resolver.
        except (subprocess.CalledProcessError, ValueError):
            raise OSError("DNS resolution failed") from None
    else:
        addresses = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM, flags=socket.AI_NUMERICHOST)
    error = OSError("DNS returned no addresses")
    for family, kind, protocol, _, destination in addresses:
        transport = socket.socket(family, kind, protocol)
        connected[:] = [transport]
        try:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError
            transport.settimeout(remaining)
            if source_address:
                transport.bind(source_address)
            transport.connect(tuple(destination))
            # HTTPSConnection uses this timeout for its TLS handshake as well.
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError
            transport.settimeout(remaining)
            return transport
        except OSError as failed:
            transport.close()
            error = failed
    raise error


def _allowed_endpoint(url) -> bool:
    try:
        if not url.hostname or url.username or url.password or url.fragment or url.port == 0:
            return False
        if url.scheme == "https":
            return True
        return url.scheme == "http" and ipaddress.ip_address(url.hostname).is_loopback
    except ValueError:
        return False


class SystemOne:
    slots = threading.BoundedSemaphore(8)

    def __init__(self, key: str, url: str = URL) -> None:
        self.key = key
        self.url = urllib.parse.urlsplit(url)

    def judge(self, state: dict, questions: dict, deadline: float, cost_limit: float | None = None) -> Result:
        if not _allowed_endpoint(self.url):
            return Result(reason="invalid endpoint")
        if time.monotonic() >= deadline:
            return Result(reason="deadline", retryable=True)
        if not self.slots.acquire(blocking=False):
            return Result(reason="transport concurrency bound", retryable=True)
        done = threading.Event()
        result = Result()

        def run() -> None:
            try:
                self._judge(state, questions, deadline, result, cost_limit)
            except Exception:
                result.reason = "transport failure"
                result.retryable = True
            finally:
                self.slots.release()
                done.set()

        thread = threading.Thread(target=run, daemon=True)
        thread.start()
        if done.wait(max(0, deadline - time.monotonic())):
            return result
        attempted = result.paid_calls
        return Result(paid_calls=attempted, input_tokens=attempted * MAX_TOKENS,
                      estimated_tokens=attempted * MAX_TOKENS, reason="deadline", retryable=True)

    def _judge(self, state: dict, questions: dict, deadline: float, result: Result, cost_limit: float | None) -> None:
        try:
            groups = batches(state, questions)
        except ValueError:
            result.reason = "request size bound"
            return
        scores = {}
        for group in groups:
            self._ask(payload(state, group), group, deadline, result, cost_limit)
            if result.reason:
                return
            scores.update(result.scores)
            result.scores = {}
        result.scores = scores

    def _ask(self, raw: bytes, questions: dict, deadline: float, result: Result, cost_limit: float | None) -> None:
        for attempt in range(3):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                result.reason = "deadline"
                result.retryable = True
                return
            if cost_limit is not None and result.cost_cents + MAX_TOKENS * CENTS_PER_TOKEN > cost_limit:
                result.reason = "cost bound"
                return
            connection_type = http.client.HTTPSConnection if self.url.scheme == "https" else http.client.HTTPConnection
            connection = connection_type(self.url.hostname, self.url.port, timeout=remaining)

            connected_socket = []
            # Preserve HTTP Host and TLS SNI/certificate checks on the original hostname.
            connection._create_connection = lambda address, timeout, source_address=None: connect_socket(
                address, deadline, connected_socket, source_address)


            def expire(current=connection, connected=connected_socket) -> None:
                transport = connected[0] if connected else current.sock
                if transport is not None:
                    try:
                        transport.shutdown(socket.SHUT_RDWR)
                    except OSError:
                        pass
                current.close()

            timer = threading.Timer(remaining, expire)
            timer.daemon = True
            timer.start()
            try:
                connection.connect()
                if time.monotonic() >= deadline:
                    result.reason = "deadline"
                    result.retryable = True
                    return
                connected_socket[:] = [connection.sock]
                path = self.url.path or "/"
                if self.url.query:
                    path += "?" + self.url.query
                result.paid_calls += 1
                result.input_tokens += MAX_TOKENS
                result.estimated_tokens += MAX_TOKENS
                connection.request("POST", path, body=raw, headers={
                    "Authorization": "Bearer " + self.key, "Content-Type": "application/json", "Accept": "application/json",
                })
                response = connection.getresponse()
                status = response.status
                result.status = status
                delay = retry_after(response.getheader("Retry-After"))
                if status != 200:
                    result.reason = "provider refused (payment)" if status == 402 else f"HTTP {status}"
                    result.retryable = status in (408, 429) or 500 <= status <= 599
                    if not result.retryable:
                        return
                    if attempt == 2 or delay >= deadline - time.monotonic():
                        return
                else:
                    result.retryable = False
                    chunks, size = [], 0
                    while True:
                        if time.monotonic() >= deadline:
                            result.reason = "deadline"
                            result.retryable = True
                            return
                        if connection.sock is not None:
                            connection.sock.settimeout(max(0.001, deadline - time.monotonic()))
                        chunk = response.read1(min(8192, MAX_RESPONSE_BYTES + 1 - size))
                        if not chunk:
                            break
                        chunks.append(chunk)
                        size += len(chunk)
                        if size > MAX_RESPONSE_BYTES:
                            result.reason = "response size bound"
                            return
                    try:
                        answer = json.loads(b"".join(chunks))
                        usage = answer.get("usage", {}) if isinstance(answer, dict) else {}
                        tokens = usage.get("input_tokens")
                        if type(tokens) is int and 0 <= tokens <= MAX_TOKENS:
                            result.input_tokens += tokens - MAX_TOKENS
                            result.estimated_tokens -= MAX_TOKENS
                        else:
                            raise ValueError
                        if answer.get("model") != MODEL:
                            raise ValueError
                        result.model = MODEL
                        answers = answer["answers"]
                        if not isinstance(answers, dict) or set(answers) != set(questions):
                            raise ValueError
                        scores = {}
                        for position, item in answers.items():
                            probability = item.get("noul") if isinstance(item, dict) else None
                            if type(probability) not in (int, float) or not 0 <= probability <= 1:
                                raise ValueError
                            scores[position] = float(probability)
                        if time.monotonic() >= deadline:
                            result.reason = "deadline"
                            result.retryable = True
                            return
                        result.scores = scores
                        result.reason = ""
                        result.retryable = False
                        return
                    except (ValueError, KeyError, TypeError, AttributeError):
                        result.reason = "invalid answer"
                        return
            except TimeoutError:
                result.reason = "deadline"
                result.retryable = True
                return
            except (OSError, http.client.HTTPException, ValueError):
                result.reason = "deadline" if time.monotonic() >= deadline else "transport failure"
                result.retryable = True
                return
            finally:
                timer.cancel()
                connection.close()
            time.sleep(delay)
        return
