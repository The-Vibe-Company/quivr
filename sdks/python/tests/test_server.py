"""Protocol routes, error-class mapping, configuration validation and logging correlation."""
import hashlib
import copy
import io
import json
import logging
import socket
import struct
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from quivr_plugin import ConfigurationError, RetryableError, TerminalError, configure_logging, validate_configuration
from quivr_plugin.schema import protocol_errors
from quivr_plugin.testing import build_request, expect_response, invoke_fixture

from support import DATA, MANIFEST, make_plugin, make_request

REPO = Path(__file__).resolve().parents[3]


class Base(unittest.TestCase):
    def setUp(self):
        self.plugin = make_plugin()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.doc = Path(self.tmp.name) / "doc.md"
        self.doc.write_text("# Title\n\nHello world.\n")

    def invoke(self, **kwargs):
        return self.plugin.invoke(make_request(self.doc, **kwargs))

    def assertEnvelope(self, reply, status, code, retryable):
        self.assertEqual(reply.status, status, reply.body)
        self.assertEqual(reply.body["code"], code)
        self.assertIs(reply.body["retryable"], retryable)
        self.assertEqual(protocol_errors("error.schema.json", reply.body), [])


class Routes(Base):
    def test_discovery_matches_the_manifest_bytes(self):
        reply = self.plugin.handle("GET", "/v0/discovery")
        self.assertEqual(reply.status, 200)
        self.assertEqual(protocol_errors("discovery.schema.json", reply.body), [])
        self.assertEqual(reply.body["manifest_digest"], "sha256:" + hashlib.sha256(MANIFEST.read_bytes()).hexdigest())
        self.assertEqual(reply.body["plugin"], {"id": "sdk-test", "version": "0.3.0"})
        self.assertEqual(reply.body["plugin_api"], "0.1.0")
        self.assertEqual(reply.body["contributions"], ["normalizer"])

    def test_health(self):
        self.assertEqual(self.plugin.handle("GET", "/v0/health").body, {"status": "ok"})

        @self.plugin.health_check
        def not_ready():
            raise RetryableError("warming_up", "loading models")

        self.assertEnvelope(self.plugin.handle("GET", "/v0/health"), 503, "warming_up", True)

        @self.plugin.health_check
        def broken():
            raise RuntimeError("boom")

        with self.assertLogs("quivr_plugin.server", logging.ERROR):
            self.assertEnvelope(self.plugin.handle("GET", "/v0/health"), 503, "unhealthy", True)

    def test_unknown_route_and_wrong_method(self):
        self.assertEnvelope(self.plugin.handle("GET", "/v0/nope"), 404, "not_found", False)
        self.assertEnvelope(self.plugin.handle("GET", "/v0/contributions/normalizer"), 405, "method_not_allowed", False)

    def test_successful_invocation_is_schema_valid(self):
        reply = self.invoke()
        response = expect_response(reply)
        self.assertEqual(response.manifest.parts[0].content.text, "# Title\n\nHello world.\n")
        self.assertEqual(protocol_errors("normalizer-response.schema.json", reply.body), [])

    def test_dict_responses_are_accepted(self):
        self.assertEqual(self.invoke(configuration={"mode": "dict"}).status, 200)


class ErrorMapping(Base):
    def test_retryable_error(self):
        self.assertEnvelope(self.invoke(configuration={"mode": "retry"}), 503, "backend_busy", True)

    def test_terminal_error(self):
        self.assertEnvelope(self.invoke(configuration={"mode": "terminal"}), 422, "unreadable_document", False)

    def test_unexpected_exception_is_terminal_internal_error(self):
        with self.assertLogs("quivr_plugin.server", logging.ERROR):
            self.assertEnvelope(self.invoke(configuration={"mode": "crash"}), 500, "internal_error", False)

    def test_invalid_response_is_caught_before_sending(self):
        with self.assertLogs("quivr_plugin.server", logging.ERROR):
            reply = self.invoke(configuration={"mode": "invalid"})
        self.assertEnvelope(reply, 500, "invalid_response", False)

    def test_response_over_the_declared_limit(self):
        self.assertEnvelope(self.invoke(configuration={"mode": "large"}), 500, "response_too_large", False)

    def test_invalid_request_body(self):
        self.assertEnvelope(self.plugin.handle("POST", "/v0/contributions/normalizer", b"{"), 400, "invalid_request", False)
        bad = json.loads((REPO / "contracts/plugins/v0/fixtures/requests/inline-content.json").read_text())
        reply = self.plugin.invoke(bad)
        self.assertEnvelope(reply, 400, "invalid_request", False)
        self.assertIn("content", reply.body["message"])

    def test_undeclared_media_type(self):
        self.assertEnvelope(self.invoke(media_type="application/pdf"), 400, "unsupported_media_type", False)

    def test_error_codes_are_checked(self):
        for code in ("", "Bad", "has space", "x" * 65):
            with self.subTest(code):
                with self.assertRaises(ValueError):
                    TerminalError(code, "message")
        self.assertEqual(len(RetryableError("long", "m" * 5000).message), 1024)


class Configuration(Base):
    def test_invalid_configuration_is_rejected_before_the_normalizer_runs(self):
        reply = self.invoke(configuration={"mode": "ok", "repeat": 0, "extra": True})
        self.assertEnvelope(reply, 400, "invalid_configuration", False)
        self.assertIn("/repeat", reply.body["message"])
        self.assertIn("extra", reply.body["message"])

    def test_validate_configuration_lists_every_problem(self):
        schema = self.plugin.manifest.configuration_schema
        with self.assertRaises(ConfigurationError) as caught:
            validate_configuration(schema, {"mode": "unknown", "repeat": "2"})
        self.assertEqual([p.split(":")[0] for p in caught.exception.problems], ["/mode", "/repeat"])
        validate_configuration(schema, {"mode": "ok", "repeat": 3})
        validate_configuration(None, {"anything": 1})
        with self.assertRaises(ConfigurationError):
            validate_configuration(None, [])

        # Reusing a schema must still judge every value, and changing a
        # caller-owned schema must not reuse the previous validation rules.
        changed = copy.deepcopy(schema)
        validate_configuration(changed, {"mode": "ok", "repeat": 3})
        changed["properties"]["repeat"]["maximum"] = 2
        for _ in range(2):
            with self.assertRaises(ConfigurationError) as caught:
                validate_configuration(changed, {"mode": "ok", "repeat": 3})
            self.assertEqual([p.split(":")[0] for p in caught.exception.problems], ["/repeat"])
            validate_configuration(changed, {"mode": "ok", "repeat": 2})


class LoggingCorrelation(Base):
    def test_records_carry_the_invocation_id(self):
        stream = io.StringIO()
        handler = configure_logging(logging.INFO, stream)
        self.addCleanup(logging.getLogger().removeHandler, handler)
        self.invoke()
        lines = [json.loads(line) for line in stream.getvalue().splitlines()]
        mine = [line for line in lines if line["message"] == "normalizing"]
        self.assertEqual(len(mine), 1)
        self.assertEqual(mine[0]["invocation_id"], "inv-1")
        self.assertEqual(mine[0]["idempotency_key"], "key-1")
        self.assertEqual(mine[0]["chars"], len(self.doc.read_text()))
        finished = [line for line in lines if line["message"] == "normalizer invocation finished"]
        self.assertEqual(finished[0]["invocation_id"], "inv-1")
        self.assertEqual(finished[0]["status"], 200)


class HTTPTransport(Base):
    def test_disconnected_caller_does_not_raise_a_server_error(self):
        entered, release, finished = (threading.Event() for _ in range(3))

        @self.plugin.health_check
        def held_health_check():
            entered.set()
            if not release.wait(5):
                raise RuntimeError("test did not release the health check")

        server = self.plugin.make_server("127.0.0.1", 0)
        errors = []
        finish = server.process_request_thread

        def observed_finish(*args):
            try:
                finish(*args)
            finally:
                finished.set()

        server.process_request_thread = observed_finish
        server.handle_error = lambda *_: errors.append(sys.exc_info()[1])
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        self.addCleanup(release.set)
        connection = socket.create_connection(("127.0.0.1", server.server_port), timeout=5)
        self.addCleanup(connection.close)
        connection.sendall(b"GET /v0/health HTTP/1.1\r\nHost: localhost\r\n\r\n")
        self.assertTrue(entered.wait(5), "request did not reach the health handler")
        # Reset the TCP connection before allowing the response to be written.
        connection.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
        connection.close()
        release.set()
        self.assertTrue(finished.wait(5), "disconnected request did not finish")
        self.assertEqual(errors, [], "a caller hang-up must not escape as a server error")
        with urllib.request.urlopen(f"http://127.0.0.1:{server.server_port}/v0/health", timeout=5) as response:
            self.assertEqual(json.load(response), {"status": "ok"})

    def test_persistent_responses_disable_nagle(self):
        # Small protocol replies must not wait for a delayed ACK between
        # their headers and body. Observe the actual accepted TCP socket;
        # latency thresholds belong in the separate burst measurement.
        server = self.plugin.make_server("127.0.0.1", 0)
        accepted = []
        accept = server.get_request

        def capture_socket():
            connection, address = accept()
            accepted.append(connection)
            return connection, address

        server.get_request = capture_socket
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        import http.client

        connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
        self.addCleanup(connection.close)
        for _ in range(2):
            connection.request("GET", "/v0/health")
            response = connection.getresponse()
            self.assertEqual(response.status, 200)
            self.assertEqual(json.loads(response.read()), {"status": "ok"})
        self.assertEqual(len(accepted), 1, "both responses should reuse the connection")
        self.assertEqual(accepted[0].getsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY), 1)

    def test_routes_over_http(self):
        server = self.plugin.make_server("127.0.0.1", 0)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        base = f"http://127.0.0.1:{server.server_port}"
        with urllib.request.urlopen(base + "/v0/discovery") as response:
            self.assertEqual(json.load(response)["plugin"]["id"], "sdk-test")
        body = json.dumps(make_request(self.doc).to_dict()).encode()
        request = urllib.request.Request(base + "/v0/contributions/normalizer", data=body, headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(request) as response:
            self.assertEqual(response.status, 200)
            self.assertEqual(json.load(response)["manifest"]["kind"], "manifest")
        body = json.dumps(make_request(self.doc, configuration={"mode": "retry"}).to_dict()).encode()
        request = urllib.request.Request(base + "/v0/contributions/normalizer", data=body)
        with self.assertRaises(urllib.error.HTTPError) as caught:
            urllib.request.urlopen(request)
        self.assertEqual(caught.exception.code, 503)
        with caught.exception as error:
            self.assertEqual(json.load(error), {"code": "backend_busy", "message": "try again later", "retryable": True})


    def test_malformed_requests_get_an_envelope(self):
        import http.client

        server = self.plugin.make_server("127.0.0.1", 0)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        for method, headers, status in [
            ("POST", {"Content-Length": "abc"}, 400),
            ("POST", {"Content-Length": "-1"}, 400),
            ("PATCH", {}, 405),
        ]:
            with self.subTest(method=method, headers=headers):
                conn = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
                self.addCleanup(conn.close)
                conn.putrequest(method, "/v0/contributions/normalizer", skip_accept_encoding=True)
                for key, value in headers.items():
                    conn.putheader(key, value)
                conn.endheaders()
                response = conn.getresponse()
                self.assertEqual(response.status, status)
                body = json.loads(response.read())
                self.assertEqual(protocol_errors("error.schema.json", body), [])


class Fixtures(Base):
    def test_build_request_from_the_normative_fixture(self):
        fixture = REPO / "contracts/plugins/v0/fixtures/invocations/markdown.json"
        request = build_request(fixture)
        document = request.to_dict()
        self.assertEqual(protocol_errors("normalizer-request.schema.json", document), [])
        data = (fixture.parent / "notes.md").read_bytes()
        self.assertEqual(request.input.sha256, hashlib.sha256(data).hexdigest())
        self.assertEqual(request.input.size_bytes, len(data))
        self.assertEqual(request.input.reference.url, (fixture.parent / "notes.md").resolve().as_uri())
        self.assertEqual(request.configuration, {"max_sections": 10})
        self.assertEqual(request.source.record_key, "notes/field-notes.md")
        self.assertEqual(build_request(fixture).to_dict(), document, "fixture requests are deterministic")

    def test_invoke_fixture(self):
        fixture = Path(self.tmp.name) / "f.json"
        fixture.write_text(json.dumps({"input": {"path": "doc.md", "media_type": "text/markdown"}}))
        response = expect_response(invoke_fixture(self.plugin, fixture))
        self.assertEqual(len(response.manifest.parts), 1)
        self.assertEqual(invoke_fixture(self.plugin, fixture, configuration={"mode": "terminal"}).status, 422)


if __name__ == "__main__":
    unittest.main()
