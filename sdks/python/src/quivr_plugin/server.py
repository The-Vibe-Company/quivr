"""Plugin Protocol v0 server adapter.

A ``Plugin`` binds a manifest to a normalizer and/or a subscription handler
(see quivr_plugin.subscription) and serves the protocol routes over HTTP with
the standard library::

    plugin = Plugin(Path(__file__).parent.parent / "quivr-plugin.yaml")

    @plugin.normalizer
    def normalize(invocation: Invocation) -> NormalizerResponse:
        text = invocation.read_input().decode()
        ...

    if __name__ == "__main__":
        plugin.serve()
"""
from __future__ import annotations

import json
import logging
import os
import signal
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

from .blob import read_input
from .connector import CREDENTIAL_PATH, FETCH_PATH, MAX_CONNECTOR_REQUEST_BYTES, Connector, invoke_connector
from .errors import PluginError, TerminalError
from .logs import configure_logging, invocation_context
from .manifest import LoadedManifest, load_manifest
from .models import Discovery, Health, NormalizerRequest, NormalizerResponse, PluginIdentity, SubscriptionRequest
from .schema import protocol_errors
from .subscription import SubscriptionInvocation, as_response, response_problems

DISCOVERY_PATH = "/v0/discovery"
HEALTH_PATH = "/v0/health"
NORMALIZER_PATH = "/v0/contributions/normalizer"
SUBSCRIPTION_PATH = "/v0/contributions/subscription"
MAX_REQUEST_BYTES = 1 << 20
# A subscription request carries the text of a Record Version; the core keeps it within 16 MiB.
MAX_SUBSCRIPTION_REQUEST_BYTES = 16 << 20


def max_request_bytes(path: str) -> int:
    """Largest request body accepted on a route."""
    if path.split("?", 1)[0] in (FETCH_PATH, CREDENTIAL_PATH):
        return MAX_CONNECTOR_REQUEST_BYTES
    return MAX_SUBSCRIPTION_REQUEST_BYTES if path.split("?", 1)[0] == SUBSCRIPTION_PATH else MAX_REQUEST_BYTES

log = logging.getLogger("quivr_plugin.server")


@dataclass
class Invocation:
    """What a normalizer receives for one invocation."""

    request: NormalizerRequest
    manifest: LoadedManifest
    logger: logging.Logger = field(default_factory=lambda: logging.getLogger("quivr_plugin.invocation"))
    _data: bytes | None = field(default=None, repr=False)

    @property
    def configuration(self) -> dict[str, Any]:
        """Plugin configuration, already validated against the manifest configuration schema."""
        return self.request.configuration

    def read_input(self) -> bytes:
        """Input Blob bytes, fetched once and verified against the declared size and SHA-256."""
        if self._data is None:
            self._data = read_input(self.request.input)
        return self._data


Normalizer = Callable[[Invocation], "NormalizerResponse | dict[str, Any]"]
SubscriptionHandler = Callable[[SubscriptionInvocation], Any]
HealthCheck = Callable[[], None]


@dataclass(frozen=True)
class Reply:
    """A protocol response: HTTP status and JSON body."""

    status: int
    body: dict[str, Any]


def _error(status: int, code: str, message: str, retryable: bool = False) -> Reply:
    return Reply(status, {"code": code, "message": message[:1024] or code, "retryable": retryable})


def _from_exception(exc: PluginError) -> Reply:
    return Reply(exc.status, exc.envelope().to_dict())


class Plugin:
    """A plugin process: one manifest, its Contributions' handlers, the Plugin Protocol v0 routes."""

    def __init__(self, manifest: str | Path | None = None) -> None:
        """Load the manifest file (or directory). Without an argument, QUIVR_PLUGIN_MANIFEST is used."""
        if manifest is None:
            manifest = os.environ.get("QUIVR_PLUGIN_MANIFEST")
            if not manifest:
                raise ValueError("pass the quivr-plugin.yaml path or set QUIVR_PLUGIN_MANIFEST")
        self.manifest = load_manifest(manifest)
        self._normalizer: Normalizer | None = None
        self._subscription: SubscriptionHandler | None = None
        self._connectors: dict[str, Connector] = {}
        self._health: HealthCheck | None = None

    def normalizer(self, fn: Normalizer) -> Normalizer:
        """Decorator registering the normalizer Contribution."""
        self._normalizer = fn
        return fn

    def subscription(self, fn: SubscriptionHandler) -> SubscriptionHandler:
        """Decorator registering the subscription Contribution (an alert rule).

        The function receives a SubscriptionInvocation and returns a SubscriptionResponse,
        a list of Decision models (see quivr_plugin.match, no_match, not_ready) or their dicts.
        """
        self._subscription = fn
        return fn

    def health_check(self, fn: HealthCheck) -> HealthCheck:
        """Decorator registering a readiness check; raise a PluginError to report not ready (503)."""
        self._health = fn
        return fn

    def connector(self, kind: str):
        """Register a class or instance implementing fetch and check_credential for a pull kind."""
        contribution = self.manifest.model.contributions.connector
        if contribution is None or kind not in contribution.kinds:
            raise ValueError(f"connector kind {kind!r} is not declared in the manifest")

        def register(implementation):
            instance = implementation() if isinstance(implementation, type) else implementation
            if not all(callable(getattr(instance, operation, None)) for operation in ("fetch", "check_credential")):
                raise TypeError("a connector must implement fetch and check_credential")
            self._connectors[kind] = instance
            return implementation

        return register

    def discovery(self) -> Discovery:
        m = self.manifest.model
        return Discovery(
            plugin_api=self.manifest.plugin_api,
            plugin=PluginIdentity(id=m.id, version=m.version),
            manifest_digest=self.manifest.digest,
            contributions=self.manifest.contributions,
        )

    # Protocol dispatch, independent of the HTTP transport.

    def handle(self, method: str, path: str, body: bytes = b"") -> Reply:
        """Answer one protocol request; used by the HTTP server and by quivr_plugin.testing."""
        routes = {DISCOVERY_PATH: "GET", HEALTH_PATH: "GET", NORMALIZER_PATH: "POST", SUBSCRIPTION_PATH: "POST",
                  FETCH_PATH: "POST", CREDENTIAL_PATH: "POST"}
        path = path.split("?", 1)[0]
        if path not in routes:
            return _error(404, "not_found", f"no Plugin Protocol v0 route {path}")
        if method != routes[path]:
            return _error(405, "method_not_allowed", f"{path} accepts {routes[path]} only")
        if path == DISCOVERY_PATH:
            return Reply(200, self.discovery().to_dict())
        if path in (FETCH_PATH, CREDENTIAL_PATH):
            status, document = invoke_connector(self.manifest, self._connectors, path, body)
            return Reply(status, document)
        if path == HEALTH_PATH:
            try:
                if self._health is not None:
                    self._health()
            except PluginError as exc:
                return Reply(503, exc.envelope().to_dict())
            except Exception:  # a failing check means not ready, never a dropped connection
                log.exception("health check raised an unexpected exception")
                return _error(503, "unhealthy", "the health check raised an unexpected exception; see the plugin logs", True)
            return Reply(200, Health().to_dict())
        if path == SUBSCRIPTION_PATH:
            return self._evaluate(body)
        return self._invoke(body)

    def invoke(self, request: NormalizerRequest | dict[str, Any]) -> Reply:
        """Run the normalizer route in process for a request model or JSON object."""
        data = request.to_dict() if isinstance(request, NormalizerRequest) else request
        return self.handle("POST", NORMALIZER_PATH, json.dumps(data).encode())

    def evaluate(self, request: SubscriptionRequest | dict[str, Any]) -> Reply:
        """Run the subscription route in process for a request model or JSON object."""
        data = request.to_dict() if isinstance(request, SubscriptionRequest) else request
        return self.handle("POST", SUBSCRIPTION_PATH, json.dumps(data).encode())

    def _evaluate(self, body: bytes) -> Reply:
        if self.manifest.model.contributions.subscription is None or self._subscription is None:
            return _error(501, "not_implemented", "the plugin declares or registers no subscription Contribution")
        if len(body) > MAX_SUBSCRIPTION_REQUEST_BYTES:
            return _error(413, "request_too_large", f"request body exceeds {MAX_SUBSCRIPTION_REQUEST_BYTES} bytes")
        try:
            document = json.loads(body)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            return _error(400, "invalid_request", f"request body is not JSON: {exc}")
        problems = protocol_errors("subscription-request.schema.json", document)
        if problems:
            return _error(400, "invalid_request", "; ".join(problems))
        request = SubscriptionRequest.from_dict(document)
        with invocation_context(request.invocation_id, request.idempotency_key):
            started = time.monotonic()
            reply = self._run_subscription(request)
            log.info(
                "subscription invocation finished",
                extra={
                    "status": reply.status,
                    "code": reply.body.get("code") if reply.status != 200 else None,
                    "evaluations": len(request.evaluations),
                    "duration_ms": round((time.monotonic() - started) * 1000, 1),
                },
            )
            return reply

    def _run_subscription(self, request: SubscriptionRequest) -> Reply:
        manifest = self.manifest
        for i, evaluation in enumerate(request.evaluations):
            problems = manifest.validate_expression(evaluation.expression)
            if problems:
                return _error(400, "invalid_expression", f"evaluation {evaluation.id}: " + "; ".join(f"/evaluations/{i}/expression{p}" for p in problems))
            problems = manifest.validate_subscription_configuration(evaluation.configuration)
            if problems:
                return _error(400, "invalid_subscription_configuration", f"evaluation {evaluation.id}: " + "; ".join(f"/evaluations/{i}/configuration{p}" for p in problems))
        try:
            manifest.validate_configuration(request.configuration)
            result = self._subscription(SubscriptionInvocation(request=request, manifest=manifest))
        except PluginError as exc:
            log.warning("subscription failed: %s", exc.message, extra={"code": exc.code, "retryable": exc.retryable})
            return _from_exception(exc)
        except Exception:  # an unexpected bug: report it, never crash the server
            log.exception("subscription handler raised an unexpected exception")
            return _error(500, "internal_error", "the subscription handler raised an unexpected exception; see the plugin logs")
        try:
            document = as_response(result).to_dict()
        except (TypeError, ValueError, KeyError) as exc:
            log.error("subscription handler returned an invalid response: %s", exc)
            return _error(500, "invalid_response", f"the subscription handler returned an invalid response: {exc}")
        problems = protocol_errors("subscription-response.schema.json", document) or response_problems(request, document)
        if problems:
            log.error("subscription response violates the protocol", extra={"problems": problems})
            return _error(500, "invalid_response", "the subscription response violates the protocol: " + "; ".join(problems))
        size = len(json.dumps(document, ensure_ascii=False, separators=(",", ":")).encode())
        if size > manifest.subscription_max_response_bytes:
            return _from_exception(TerminalError(
                "response_too_large",
                f"the subscription response is {size} bytes; the manifest declares at most {manifest.subscription_max_response_bytes}",
                status=500))
        return Reply(200, document)

    def _invoke(self, body: bytes) -> Reply:
        if self.manifest.model.contributions.normalizer is None:
            return _error(501, "not_implemented", "the plugin declares no normalizer Contribution")
        if self._normalizer is None:
            return _error(501, "not_implemented", "the plugin registered no normalizer")
        if len(body) > MAX_REQUEST_BYTES:
            return _error(413, "request_too_large", f"request body exceeds {MAX_REQUEST_BYTES} bytes")
        try:
            document = json.loads(body)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            return _error(400, "invalid_request", f"request body is not JSON: {exc}")
        problems = protocol_errors("normalizer-request.schema.json", document)
        if problems:
            return _error(400, "invalid_request", "; ".join(problems))
        request = NormalizerRequest.from_dict(document)
        with invocation_context(request.invocation_id, request.idempotency_key):
            started = time.monotonic()
            reply = self._run(request)
            log.info(
                "normalizer invocation finished",
                extra={
                    "status": reply.status,
                    "code": reply.body.get("code") if reply.status != 200 else None,
                    "duration_ms": round((time.monotonic() - started) * 1000, 1),
                },
            )
            return reply

    def _run(self, request: NormalizerRequest) -> Reply:
        manifest = self.manifest
        if request.input.media_type not in manifest.media_types:
            return _error(400, "unsupported_media_type",
                          f"media type {request.input.media_type} is not declared by this plugin ({', '.join(manifest.media_types)})")
        try:
            manifest.validate_configuration(request.configuration)
            result = self._normalizer(Invocation(request=request, manifest=manifest))
        except PluginError as exc:
            log.warning("normalizer failed: %s", exc.message, extra={"code": exc.code, "retryable": exc.retryable})
            return _from_exception(exc)
        except Exception:  # an unexpected bug: report it, never crash the server
            log.exception("normalizer raised an unexpected exception")
            return _error(500, "internal_error", "the normalizer raised an unexpected exception; see the plugin logs")
        try:
            response = result if isinstance(result, NormalizerResponse) else NormalizerResponse.from_dict(result)
            document = response.to_dict()
        except (TypeError, ValueError) as exc:
            log.error("normalizer returned an invalid response: %s", exc)
            return _error(500, "invalid_response", f"the normalizer returned an invalid response: {exc}")
        problems = protocol_errors("normalizer-response.schema.json", document)
        if problems:
            log.error("normalizer response violates the protocol schema", extra={"problems": problems})
            return _error(500, "invalid_response", "the normalizer response violates the protocol schema: " + "; ".join(problems))
        size = len(json.dumps(document, ensure_ascii=False, separators=(",", ":")).encode())
        if size > manifest.max_response_bytes:
            return _from_exception(TerminalError(
                "response_too_large",
                f"the normalizer response is {size} bytes; the manifest declares at most {manifest.max_response_bytes}",
                status=500))
        return Reply(200, document)

    # HTTP transport.

    def make_server(self, host: str = "127.0.0.1", port: int = 0) -> ThreadingHTTPServer:
        """Build (but do not start) a threaded HTTP server bound to host:port."""
        plugin = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            server_version = "quivr-plugin-sdk/0.2"

            def _serve(self, method: str) -> None:
                raw_length = self.headers.get("Content-Length") or "0"
                try:
                    length = int(raw_length)
                except ValueError:
                    length = -1
                if length < 0:
                    reply = _error(400, "invalid_request", f"invalid Content-Length {raw_length!r}")
                    self.close_connection = True
                elif length > max_request_bytes(self.path):
                    reply = _error(413, "request_too_large", f"request body exceeds {max_request_bytes(self.path)} bytes")
                    self.close_connection = True
                else:
                    body = self.rfile.read(length) if length else b""
                    try:
                        reply = plugin.handle(method, self.path, body)
                    except Exception:  # last resort: every answer carries the envelope
                        log.exception("unexpected error while handling %s %s", method, self.path)
                        reply = _error(500, "internal_error", "the plugin failed to handle the request; see the plugin logs")
                payload = json.dumps(reply.body, ensure_ascii=False, separators=(",", ":")).encode()
                self.send_response(reply.status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                if method != "HEAD":
                    self.wfile.write(payload)

            def do_GET(self) -> None:  # noqa: N802
                self._serve("GET")

            def do_POST(self) -> None:  # noqa: N802
                self._serve("POST")

            def do_PUT(self) -> None:  # noqa: N802
                self._serve("PUT")

            def do_PATCH(self) -> None:  # noqa: N802
                self._serve("PATCH")

            def do_DELETE(self) -> None:  # noqa: N802
                self._serve("DELETE")

            def do_HEAD(self) -> None:  # noqa: N802
                self._serve("HEAD")

            def do_OPTIONS(self) -> None:  # noqa: N802
                self._serve("OPTIONS")

            def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
                log.debug("http %s", format % args)

        server = ThreadingHTTPServer((host, port), Handler)
        server.daemon_threads = True
        return server

    def serve(self, host: str | None = None, port: int | None = None, *, log_level: int | str = logging.INFO) -> None:
        """Serve until interrupted. Host and port default to QUIVR_PLUGIN_HOST / QUIVR_PLUGIN_PORT,
        then 127.0.0.1:8080 (``quivr plugin dev`` sets both)."""
        configure_logging(log_level)
        host = host or os.environ.get("QUIVR_PLUGIN_HOST") or "127.0.0.1"
        port = port if port is not None else int(os.environ.get("QUIVR_PLUGIN_PORT") or 8080)
        server = self.make_server(host, port)
        m = self.manifest.model
        log.info("plugin listening", extra={"plugin": m.id, "version": m.version, "address": f"http://{host}:{server.server_port}",
                                            "manifest_digest": self.manifest.digest})

        def stop(signum, frame) -> None:
            threading.Thread(target=server.shutdown, daemon=True).start()

        if threading.current_thread() is threading.main_thread():
            signal.signal(signal.SIGTERM, stop)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass
        finally:
            server.server_close()
