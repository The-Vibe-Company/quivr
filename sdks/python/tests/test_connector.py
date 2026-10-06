"""Owner tests for the Python connector adapter's invocation safeguards."""
import base64
import io
import json
import logging
from pathlib import Path
import tempfile
import unittest

import yaml

from quivr_plugin import Plugin
from quivr_plugin.connector import RECEIVE_PATH
from quivr_plugin.logs import configure_logging
from quivr_plugin.schema import protocol_errors

REPO = Path(__file__).resolve().parents[3]
FETCH = "/v0/contributions/connector/fetch"
CHECK = "/v0/contributions/connector/check_credential"


class Connectors(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        manifest = yaml.safe_load((REPO / "contracts/plugins/v0/fixtures/manifests/valid/connector.yaml").read_text())
        manifest["configuration"] = {"schema": {"type": "object", "additionalProperties": False,
                                                   "properties": {"count": {"type": "integer"}}}}
        manifest["contributions"]["connector"]["limits"] = {
            "max_items": 1, "max_response_bytes": 32768, "max_checkpoint_bytes": 1024,
        }
        self.path = Path(self.directory.name) / "quivr-plugin.yaml"
        self.path.write_text(yaml.safe_dump(manifest))
        self.plugin = Plugin(self.path)
        self.request = {
            "invocation_id": "fetch-1", "contribution": "connector", "organization_id": "org",
            "configuration": {}, "connector": {"instance_id": "source-1", "kind": "feed",
                                                     "config": {"feed_url": "https://example.org/feed"}},
            "credential": {"token": "secret-token"}, "checkpoint": None,
            "now": "2026-01-01T00:00:00Z", "page_in_run": 0, "reads_today": 0,
        }

    def register(self, fetch, check=None):
        class Source:
            def fetch(self, request):
                return fetch(request)

            def check_credential(self, request):
                return check(request) if check else {"status": "ok"}

        self.plugin.connector("feed")(Source)

    def invoke(self, request=None, route=FETCH):
        return self.plugin.handle("POST", route, json.dumps(request or self.request).encode())

    def assert_error(self, reply, status, code):
        self.assertEqual((reply.status, reply.body["code"]), (status, code), reply)
        self.assertEqual(protocol_errors("error.schema.json", reply.body), [])
        self.assertNotIn("secret-token", json.dumps(reply.body))

    def test_typed_requests_discovery_and_credential_check(self):
        from quivr_plugin import ConnectorFetchRequest, ConnectorCredentialRequest

        def fetch(request):
            self.assertIsInstance(request, ConnectorFetchRequest)
            self.assertEqual(request.credential["token"], "secret-token")
            self.assertNotIn("secret-token", repr(request))
            self.assertNotIn("secret-token", json.dumps(request.to_dict()))
            return {"items": [], "checkpoint": {"offset": 0}, "more": False}

        def check(request):
            self.assertIsInstance(request, ConnectorCredentialRequest)
            return {"status": "ok", "expires_at": "2027-01-01T00:00:00Z"}

        self.register(fetch, check)
        self.assertEqual(self.invoke().body, {"items": [], "checkpoint": {"offset": 0}, "more": False})
        credential_request = {key: value for key, value in self.request.items()
                              if key not in ("checkpoint", "page_in_run", "reads_today")}
        self.assertEqual(self.invoke(credential_request, CHECK).body["expires_at"], "2027-01-01T00:00:00Z")
        discovery = self.plugin.handle("GET", "/v0/discovery").body
        self.assertEqual((discovery["contributions"], discovery["plugin_api"]), (["connector"], "0.3.1"))
        self.assert_error(self.plugin.handle("GET", FETCH), 405, "method_not_allowed")

    def test_request_refusals_do_not_call_source(self):
        self.register(lambda request: self.fail("invalid request reached the source"))
        mutations = [
            (lambda request: request.update(page_in_run=-1), "invalid_request"),
            (lambda request: request["connector"].update(kind="unknown"), "unknown_kind"),
            (lambda request: request["connector"].update(config={}), "invalid_config"),
            (lambda request: request.update(configuration={"extra": "secret-token"}), "invalid_configuration"),
            (lambda request: request.update(credential=None), "invalid_credential"),
            (lambda request: request.update(credential={"token": "tiny"}), "invalid_credential"),
            (lambda request: request["connector"].update(kind="public_feed"), "invalid_credential"),
        ]
        for mutate, code in mutations:
            with self.subTest(code=code):
                request = json.loads(json.dumps(self.request))
                mutate(request)
                self.assert_error(self.invoke(request), 400, code)
        self.assert_error(self.plugin.handle("POST", FETCH, b"{"), 400, "invalid_request")

    def test_classified_failures_not_due_and_unclassified_io(self):
        from quivr_plugin import AccessError, TransientError, SourceError, NotDue

        failures = [(AccessError("denied", "secret-token refused"), 403, "denied", "access", False),
                    (TransientError("rate_limit", "secret-token limited", retry_after_seconds=1.2), 503, "rate_limit", "transient", True),
                    (SourceError("bad_source", "secret-token unreadable"), 422, "bad_source", "source", False),
                    (OSError("secret-token unavailable"), 503, "unexpected_error", "transient", True),
                    (RuntimeError("secret-token failed"), 500, "internal_error", "source", False)]
        for error, status, code, classification, retryable in failures:
            with self.subTest(error=type(error).__name__):
                def fetch(request):
                    raise error
                self.register(fetch)
                reply = self.invoke()
                self.assertEqual((reply.status, reply.body["code"], reply.body["error_class"], reply.body["retryable"]),
                                 (status, code, classification, retryable))
                self.assertNotIn("secret-token", json.dumps(reply.body))
                if classification == "transient" and isinstance(error, TransientError):
                    self.assertEqual(reply.body["retry_after_seconds"], 2)
        def not_due(request):
            raise NotDue()
        self.register(not_due)
        self.request["checkpoint"] = {"offset": 7}
        self.assertEqual(self.invoke().body, {"items": [], "checkpoint": {"offset": 7}, "more": False, "not_due": True})

    def test_credentials_are_redacted_from_logs_and_response_validation(self):
        output = io.StringIO()
        plain_output = io.StringIO()
        root = logging.getLogger()
        previous_level = root.level
        handler = configure_logging(stream=output)
        self.addCleanup(root.removeHandler, handler)
        self.addCleanup(root.setLevel, previous_level)
        plain_handler = logging.StreamHandler(plain_output)
        root.addHandler(plain_handler)
        self.addCleanup(root.removeHandler, plain_handler)
        secret = 'secret-token"\\value'
        self.request["credential"]["token"] = secret

        def fetch(request):
            request.logger.info("token %s", secret, extra={"nested": {"value": secret}})
            try:
                raise RuntimeError(secret)
            except RuntimeError:
                request.logger.exception("failed %s", secret)
            return {"items": [], "checkpoint": None, "more": secret}

        self.register(fetch)
        reply = self.invoke()
        self.assert_error(reply, 500, "invalid_response")
        self.assertNotIn(secret, output.getvalue())
        self.assertNotIn("secret-token", output.getvalue())
        self.assertNotIn("secret-token", json.dumps(reply.body))
        self.assertIn("[redacted]", output.getvalue())
        self.assertNotIn("secret-token", plain_output.getvalue())
        self.assertIn("[redacted]", plain_output.getvalue())
        records = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertTrue(all(record["invocation_id"] == "fetch-1" for record in records))
        self.assertTrue(any("exception" in record for record in records))

    def test_page_self_checks_and_credential_status_schema(self):
        pages = [None, {"items": [], "checkpoint": None, "more": "yes"},
                 {"items": [{"record_key": str(number), "content": {"kind": "text", "text": "body"}}
                            for number in range(2)], "checkpoint": 2, "more": False},
                 {"items": [], "checkpoint": "x" * 1024, "more": False},
                 {"items": [], "checkpoint": None, "more": False, "diagnostics": {"data": "x" * 16384}},
                 {"items": [{"record_key": "large", "content": {"kind": "text", "text": "x" * 32768}}],
                  "checkpoint": 1, "more": False},
                 {"items": [], "checkpoint": {"bad": object()}, "more": False}]
        for page in pages:
            with self.subTest(page_type=type(page).__name__):
                self.register(lambda request: page)
                self.assert_error(self.invoke(), 500, "invalid_response")
        self.register(lambda request: {}, lambda request: {"status": "wrong"})
        request = {key: value for key, value in self.request.items()
                   if key not in ("checkpoint", "page_in_run", "reads_today")}
        self.assert_error(self.invoke(request, CHECK), 500, "invalid_response")

    def test_configuration_diagnostics_redact_before_truncation(self):
        self.register(lambda request: self.fail("invalid configuration reached the source"))
        secret = "private-token-" * 160
        self.request["credential"]["token"] = secret
        self.request["configuration"] = {"count": secret}
        reply = self.invoke()
        self.assert_error(reply, 400, "invalid_configuration")
        self.assertNotIn("private-token", reply.body["message"])
        self.assertIn("[redacted]", reply.body["message"])

    def test_grouped_exceptions_redact_multiline_secrets_before_layout(self):
        output = io.StringIO()
        logger = logging.getLogger("quivr_plugin.connector")
        handler = logging.StreamHandler(output)
        logger.addHandler(handler)
        self.addCleanup(logger.removeHandler, handler)
        secret = "private-line-one\nprivate-line-two"
        self.request["credential"]["token"] = secret

        def fetch(request):
            cause = RuntimeError(secret)
            cause.add_note(secret)
            grouped = ExceptionGroup(secret, [cause, ExceptionGroup("nested", [ValueError(secret)])])
            raise grouped from OSError(secret)

        self.register(fetch)
        self.assert_error(self.invoke(), 500, "internal_error")
        self.assertNotIn("private-line", output.getvalue())
        self.assertIn("[redacted]", output.getvalue())

    def test_public_request_helpers_wrap_credentials_and_allow_public_sources(self):
        from quivr_plugin import CredentialRequest, FetchRequest

        for credential in (None, {"token": "secret-token"}):
            for request_class in (FetchRequest, CredentialRequest):
                with self.subTest(request_class=request_class.__name__, public=credential is None):
                    document = {**self.request, "credential": credential}
                    if request_class is CredentialRequest:
                        document = {key: value for key, value in document.items()
                                    if key not in ("checkpoint", "page_in_run", "reads_today")}
                    for request in (request_class.from_dict(document), request_class(**document)):
                        self.assertNotIn("secret-token", repr(request))
                        self.assertNotIn("secret-token", json.dumps(request.to_dict()))
                        request.logger.info("public helper invocation")

class ExtendedConnectors(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)

    def write_manifest(self, fixture):
        manifest = yaml.safe_load((REPO / "contracts/plugins/v0/fixtures/manifests/valid" / fixture).read_text())
        path = Path(self.directory.name) / fixture
        path.write_text(yaml.safe_dump(manifest))
        return Plugin(path)

    def write_route_manifest(self):
        manifest = json.loads((REPO / "contracts/plugins/v0/fixtures/manifests/valid/connector-api.json").read_text())
        events = manifest["contributions"]["connector"]["kinds"]["events"]
        alerts = json.loads(json.dumps(events))
        alerts["api"]["routes"][0]["path"] = "alerts/{category}"
        alerts["api"]["routes"][0]["request_schema"] = {
            "type": "object", "additionalProperties": False,
            "required": ["severity"], "properties": {"severity": {"type": "string"}},
        }
        manifest["contributions"]["connector"]["kinds"]["alerts"] = alerts
        path = Path(self.directory.name) / "connector-routes.yaml"
        path.write_text(yaml.safe_dump(manifest))
        return Plugin(path)

    def route_request(self, kind="events", route="push", method="POST", path="events/news", body=None):
        raw_body = json.dumps(body).encode()
        return {
            "invocation_id": "route-1", "contribution": "connector", "organization_id": "org",
            "configuration": {},
            "connector": {"instance_id": "route-1", "kind": kind, "corpus_id": "corpus",
                           "source_namespace": kind, "config": {}},
            "credential": None, "checkpoint": None, "now": "2026-01-01T00:00:00Z", "reads_today": 0,
            "request": {"method": method, "query": "", "headers": {},
                        "body_base64": base64.b64encode(raw_body).decode(), "path": path},
            "route": route, "body": body,
        }

    def receive_request(self):
        return {
            "invocation_id": "receive-1", "contribution": "connector", "organization_id": "org",
            "configuration": {},
            "connector": {"instance_id": "alerts-1", "kind": "alerts", "corpus_id": "corpus",
                           "source_namespace": "alerts", "config": {"account": "demo"}},
            "credential": {"token": "secret-token", "signing_secret": "secret-signing"},
            "checkpoint": None, "now": "2026-01-01T00:00:00Z", "reads_today": 0,
            "request": {"method": "POST", "query": "challenge=1", "headers": {"x-signature": ["sig"]},
                        "body_base64": "", "path": "/events"},
            "route": "events", "body": {"event": "ok"},
        }

    def test_receive_dispatches_custom_route_and_rejects_incoherent_verdict(self):
        plugin = self.write_manifest("connector-push.yaml")

        class Source:
            def fetch(self, request):
                return {"items": [], "checkpoint": request.checkpoint, "more": False}

            def check_credential(self, request):
                return {"status": "ok"}

            def receive(self, request):
                self.seen = request
                return {"verdict": "accepted", "response": {"status": 202}, "items": [
                    {"record_key": "event-1", "content": {"kind": "text", "text": "hello"}}
                ]}

        source = Source()
        plugin.connector("alerts")(source)
        reply = plugin.handle("POST", "/v0/contributions/connector/receive", json.dumps(self.receive_request()).encode())
        self.assertEqual(reply.status, 200, reply.body)
        self.assertEqual(reply.body["items"][0]["record_key"], "event-1")
        self.assertEqual(source.seen.route, "events")
        self.assertEqual(source.seen.body, {"event": "ok"})
        self.assertEqual(source.seen.credential["token"], "secret-token")

        for description, delivery in (
            ("refused with items", {"verdict": "refused", "response": {"status": 401}, "items": [
                {"record_key": "event-1", "content": {"kind": "text", "text": "hello"}}
            ]}),
            ("refused with success status", {"verdict": "refused", "response": {"status": 200}}),
        ):
            with self.subTest(description=description):
                class Invalid(Source):
                    def receive(self, request):
                        return delivery

                plugin.connector("alerts")(Invalid)
                invalid = plugin.handle("POST", RECEIVE_PATH, json.dumps(self.receive_request()).encode())
                self.assertEqual((invalid.status, invalid.body["code"]), (500, "invalid_response"), invalid.body)

    def test_connector_routes_register_dispatch_and_validate_before_handler(self):
        from quivr_plugin import ConnectorReceiveResponse, ReceiveAnswer, ReceiveRequest

        plugin = self.write_route_manifest()
        calls = {"events": 0, "alerts": 0}

        class Source:
            def fetch(self, request):
                return {"items": [], "checkpoint": request.checkpoint, "more": False}

            def check_credential(self, request):
                return {"status": "ok"}

        plugin.connector("events")(Source)
        plugin.connector("alerts")(Source)

        @plugin.connector_route("events", "push")
        def events(request: ReceiveRequest) -> ConnectorReceiveResponse:
            self.assertIsInstance(request, ReceiveRequest)
            calls["events"] += 1
            self.assertEqual((request.route, request.request.path, request.body), ("push", "events/news", {"text": "hello"}))
            return ConnectorReceiveResponse(verdict="accepted", response=ReceiveAnswer(status=202), items=[])

        @plugin.connector_route("alerts", "push")
        def alerts(request: ReceiveRequest) -> ConnectorReceiveResponse:
            self.assertIsInstance(request, ReceiveRequest)
            calls["alerts"] += 1
            self.assertEqual(request.body, {"severity": "high"})
            return ConnectorReceiveResponse(verdict="accepted", response=ReceiveAnswer(status=202), items=[])

        @plugin.connector_route("events", "challenge")
        def challenge(request: ReceiveRequest) -> ConnectorReceiveResponse:
            return ConnectorReceiveResponse(verdict="accepted", response=ReceiveAnswer(status=200), items=[])

        @plugin.connector_route("alerts", "challenge")
        def alert_challenge(request: ReceiveRequest) -> ConnectorReceiveResponse:
            return ConnectorReceiveResponse(verdict="accepted", response=ReceiveAnswer(status=200), items=[])

        plugin.check_registered()
        reply = plugin.handle("POST", RECEIVE_PATH, json.dumps(self.route_request(body={"text": "hello"})).encode())
        self.assertEqual((reply.status, reply.body["verdict"]), (200, "accepted"), reply.body)
        reply = plugin.handle("POST", RECEIVE_PATH, json.dumps(
            self.route_request(kind="alerts", path="alerts/critical", body={"severity": "high"})).encode())
        self.assertEqual((reply.status, reply.body["verdict"]), (200, "accepted"), reply.body)
        self.assertEqual(calls, {"events": 1, "alerts": 1})

        invalid = [
            ("missing route", self.route_request(route="missing", body={"text": "hello"}), "unknown_route"),
            ("wrong method", self.route_request(method="GET", body={"text": "hello"}), "invalid_request"),
            ("wrong path", self.route_request(path="alerts/news", body={"text": "hello"}), "invalid_request"),
            ("empty segment", self.route_request(path="events/", body={"text": "hello"}), "invalid_request"),
            ("dot segment", self.route_request(path="events/.", body={"text": "hello"}), "invalid_request"),
            ("body schema", self.route_request(body={"severity": "high"}), "invalid_request"),
        ]
        for description, request, code in invalid:
            with self.subTest(description=description):
                reply = plugin.handle("POST", RECEIVE_PATH, json.dumps(request).encode())
                self.assertEqual((reply.status, reply.body["code"]), (400, code), reply.body)
        self.assertEqual(calls, {"events": 1, "alerts": 1})

    def test_connector_route_registration_requires_declared_nonduplicate_handlers(self):
        plugin = self.write_route_manifest()
        with self.assertRaises(TypeError):
            plugin.connector_route("events", "push")(None)
        with self.assertRaises(ValueError):
            plugin.connector_route("events", "missing")
        with self.assertRaises(ValueError):
            plugin.connector_route("missing", "push")

        @plugin.connector_route("events", "push")
        def handler(request):
            return {"verdict": "accepted", "response": {"status": 200}}

        with self.assertRaises(ValueError):
            plugin.connector_route("events", "push")(handler)

    def test_route_only_connector_refuses_legacy_receive_and_requires_routes(self):
        from quivr_plugin import ConnectorReceiveResponse, ReceiveAnswer

        plugin = self.write_route_manifest()

        class Source:
            def fetch(self, request):
                return {"items": [], "checkpoint": request.checkpoint, "more": False}

            def check_credential(self, request):
                return {"status": "ok"}

        plugin.connector("events")(Source)
        plugin.connector("alerts")(Source)

        @plugin.connector_route("events", "push")
        def events(request):
            return ConnectorReceiveResponse(verdict="accepted", response=ReceiveAnswer(status=202), items=[])

        @plugin.connector_route("events", "challenge")
        def challenge(request):
            return ConnectorReceiveResponse(verdict="accepted", response=ReceiveAnswer(status=200), items=[])

        @plugin.connector_route("alerts", "push")
        def alerts(request):
            return ConnectorReceiveResponse(verdict="accepted", response=ReceiveAnswer(status=202), items=[])

        @plugin.connector_route("alerts", "challenge")
        def alert_challenge(request):
            return ConnectorReceiveResponse(verdict="accepted", response=ReceiveAnswer(status=200), items=[])

        plugin.check_registered()
        request = self.route_request(route=None, body=None)
        request.pop("route")
        request.pop("body")
        reply = plugin.handle("POST", RECEIVE_PATH, json.dumps(request).encode())
        self.assertEqual((reply.status, reply.body["code"]), (400, "push_unsupported"), reply.body)

    def attachment_request(self, *, upload=False):
        request = {
            "invocation_id": "attachment-1", "contribution": "connector", "organization_id": "org",
            "configuration": {},
            "connector": {"instance_id": "mail-1", "kind": "mailbox", "config": {"mailbox": "demo"}},
            "credential": {"token": "secret-token"}, "now": "2026-01-01T00:00:00Z",
            "item": {"record_key": "message-1", "revision": "r1"},
            "attachment": {"key": "file-1", "role": "attachment", "media_type": "text/plain", "ref": "opaque-ref"},
        }
        if upload:
            request["grant"] = {"url": "http://127.0.0.1:1/grant", "method": "PUT", "headers": {"x-grant": "grant-secret"},
                                  "size_bytes": 5, "sha256": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
                                  "media_type": "text/plain", "expires_at": "2027-01-01T00:00:00Z"}
        return request

    def test_attachment_handlers_dispatch_and_enforce_declared_size(self):
        plugin = self.write_manifest("connector-attachments.yaml")

        class Source:
            def fetch(self, request):
                return {"items": [], "checkpoint": request.checkpoint, "more": False}

            def check_credential(self, request):
                return {"status": "ok"}

            def describe_attachment(self, request):
                self.described = request
                size = 2 * 1024 * 1024 if getattr(self, "oversize", False) else 5
                return {"size_bytes": size, "sha256": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}

            def upload_attachment(self, request):
                self.uploaded = request
                return {"status": "uploaded"}

        source = Source()
        plugin.connector("mailbox")(source)
        described = plugin.handle("POST", "/v0/contributions/connector/describe_attachment",
                                  json.dumps(self.attachment_request()).encode())
        uploaded = plugin.handle("POST", "/v0/contributions/connector/upload_attachment",
                                 json.dumps(self.attachment_request(upload=True)).encode())
        self.assertEqual(described.body["size_bytes"], 5)
        self.assertEqual(uploaded.body, {"status": "uploaded"})
        self.assertNotIn("secret-token", repr(source.described))
        self.assertNotIn("grant-secret", repr(source.uploaded))

        too_large = self.attachment_request()
        too_large["attachment"]["size_bytes"] = 2 * 1024 * 1024
        too_large["attachment"]["sha256"] = "0" * 64
        source.oversize = True
        rejected = plugin.handle("POST", "/v0/contributions/connector/describe_attachment", json.dumps(too_large).encode())
        self.assertEqual((rejected.status, rejected.body["code"]), (500, "invalid_response"), rejected.body)

    def test_fetch_output_rejects_extension_and_manifest_storage_bounds(self):
        plugin = self.write_manifest("connector.yaml")
        request = {
            "invocation_id": "fetch-bounds", "contribution": "connector", "organization_id": "org",
            "configuration": {},
            "connector": {"instance_id": "source-1", "kind": "feed",
                           "config": {"feed_url": "https://example.org/feed"}},
            "credential": {"token": "secret-token"}, "checkpoint": None,
            "now": "2026-01-01T00:00:00Z", "page_in_run": 0, "reads_today": 0,
        }
        cases = [
            ("extension data key contains NUL", {
                "example-feeds": {"schema_version": "1", "data": {"bad\x00key": "value"}},
            }, None),
            ("extension data value contains NUL", {
                "example-feeds": {"schema_version": "1", "data": {"nested": {"bad": "va\x00lue"}}},
            }, None),
            ("extension data exceeds 64 KiB", {
                "example-feeds": {"schema_version": "1", "data": {"payload": "x" * (64 << 10)}},
            }, None),
            ("HTML-heavy extension data exceeds stored 64 KiB", {
                "example-feeds": {"schema_version": "1", "data": {"payload": "<&" * 6000}},
            }, None),
            ("Go fixed-float extension data exceeds stored 64 KiB", {
                "example-feeds": {"schema_version": "1", "data": {"values": [1e-6] * 10000}},
            }, None),
            ("Go float64 integer normalization exceeds stored 64 KiB", {
                "example-feeds": {"schema_version": "1", "data": {"values": [999999999999999999] * 3400}},
            }, None),
            ("non-text Manifest structure exceeds 64 KiB", None, "r" * (64 << 10)),
            ("HTML-heavy Manifest metadata exceeds stored 64 KiB", None, "<&" * 6000),
        ]
        for description, extensions, manifest_role in cases:
            with self.subTest(description=description):
                item = {"record_key": "item-1", "content": {"kind": "text", "text": "body"}}
                if extensions is not None:
                    item["extensions"] = extensions
                else:
                    item["content"] = {"kind": "manifest", "parts": [{
                        "key": "body", "role": manifest_role,
                        "content": {"kind": "text", "text": "body"},
                    }]}
                response = {"items": [item], "checkpoint": None, "more": False}

                class Source:
                    def fetch(self, _request):
                        return response

                    def check_credential(self, _request):
                        return {"status": "ok"}

                plugin.connector("feed")(Source)
                reply = plugin.handle("POST", FETCH, json.dumps(request).encode())
                self.assertEqual(reply.status, 500, reply.body)
                self.assertEqual(reply.body.get("code"), "invalid_response", reply.body)
