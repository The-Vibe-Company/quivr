"""Owner tests for the Python connector adapter's invocation safeguards."""
import io
import json
import logging
from pathlib import Path
import tempfile
import unittest

import yaml

from quivr_plugin import Plugin
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

        failures = [(AccessError("denied", "secret-token refused"), 403, "access", False),
                    (TransientError("rate_limit", "secret-token limited", retry_after_seconds=1.2), 503, "transient", True),
                    (SourceError("bad_source", "secret-token unreadable"), 422, "source", False),
                    (OSError("secret-token unavailable"), 503, "transient", True)]
        for error, status, classification, retryable in failures:
            with self.subTest(error=type(error).__name__):
                def fetch(request):
                    raise error
                self.register(fetch)
                reply = self.invoke()
                self.assertEqual((reply.status, reply.body["error_class"], reply.body["retryable"]),
                                 (status, classification, retryable))
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

    def test_unsupported_capabilities_are_refused_at_manifest_load(self):
        from quivr_plugin import ManifestError

        for declaration in ({"attachments": {}}, {"attachments": {"max_bytes": 1}},
                            {"kinds": {"feed": {"config_schema": {"type": "object"},
                                               "default_interval_seconds": 60, "modes": ["pull", "push"]}}}):
            with self.subTest(declaration=declaration):
                manifest = yaml.safe_load(self.path.read_text())
                manifest["contributions"]["connector"].update(declaration)
                self.path.write_text(yaml.safe_dump(manifest))
                with self.assertRaisesRegex(ManifestError, "pull kinds without attachments"):
                    Plugin(self.path)
