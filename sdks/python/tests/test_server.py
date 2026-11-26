"""Protocol routes, error-class mapping, configuration validation and logging correlation."""
import hashlib
import copy
import io
import json
import logging
import os
import signal
import socket
import struct
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock

from quivr_plugin import ConfigurationError, RetryableError, TerminalError, configure_logging, match, validate_configuration
from quivr_plugin.schema import protocol_errors
from quivr_plugin.server import DEFAULT_IDLE_TIMEOUT, DEFAULT_REQUEST_QUEUE_SIZE, MANIFEST_DIGEST_HEADER, Plugin, Reply
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
    def test_invocation_continues_w3c_context(self):
        from opentelemetry import trace
        observed = []
        @self.plugin.normalizer
        def normalize(invocation):
            observed.append(trace.get_current_span().get_span_context().trace_id)
            raise TerminalError("observed", "observed")
        server = self.plugin.make_server("127.0.0.1", 0)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        request = urllib.request.Request(
            f"http://127.0.0.1:{server.server_port}/v0/contributions/normalizer",
            data=json.dumps(make_request(self.doc).to_dict()).encode(),
            headers={"traceparent": "00-11111111111111111111111111111111-2222222222222222-01"},
        )
        with self.assertRaises(urllib.error.HTTPError) as caught:
            urllib.request.urlopen(request, timeout=5)
        caught.exception.close()
        self.assertEqual(observed, [int("11111111111111111111111111111111", 16)])

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
                self.assertEqual(response.getheader(MANIFEST_DIGEST_HEADER), self.plugin.manifest.digest)
                body = json.loads(response.read())
                self.assertEqual(protocol_errors("error.schema.json", body), [])

    def test_server_backlog_reuse_and_idle_timeout(self):
        with self.assertRaises(ValueError):
            self.plugin.make_server("127.0.0.1", 0, idle_timeout=0)
        with self.assertRaises(ValueError):
            self.plugin.make_server("127.0.0.1", 0, idle_timeout=-5)

        default_server = self.plugin.make_server("127.0.0.1", 0)
        self.addCleanup(default_server.server_close)
        self.assertEqual(default_server.request_queue_size, DEFAULT_REQUEST_QUEUE_SIZE)
        self.assertEqual(default_server.request_queue_size, 128)
        self.assertTrue(default_server.allow_reuse_address)
        self.assertEqual(default_server.RequestHandlerClass.timeout, DEFAULT_IDLE_TIMEOUT)

        # Observe a real idle HTTP/1.1 keep-alive connection being closed when
        # the configured socket idle timeout expires, freeing its worker thread.
        server = self.plugin.make_server("127.0.0.1", 0, idle_timeout=0.05)
        accepted = []
        errors = []
        finished = threading.Event()
        accept = server.get_request
        finish = server.process_request_thread

        def capture_socket():
            connection, address = accept()
            accepted.append(connection)
            return connection, address

        def observed_finish(*args):
            try:
                finish(*args)
            finally:
                finished.set()

        server.get_request = capture_socket
        server.process_request_thread = observed_finish
        server.handle_error = lambda *_: errors.append(sys.exc_info()[1])
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        import http.client

        conn = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
        self.addCleanup(conn.close)
        conn.request("GET", "/v0/health")
        resp = conn.getresponse()
        self.assertEqual(resp.status, 200)
        self.assertEqual(json.loads(resp.read()), {"status": "ok"})
        self.assertEqual(len(accepted), 1)
        self.assertEqual(accepted[0].gettimeout(), 0.05)
        self.assertTrue(finished.wait(5), "idle keep-alive handler thread did not exit on timeout")
        self.assertEqual(errors, [], "idle timeout must not escape as a server error")
        self.assertEqual(conn.sock.recv(1), b"", "server must close the idle keep-alive socket")

    def test_manifest_digest_header_on_all_v0_responses(self):
        import http.client

        expected = self.plugin.manifest.digest
        server = self.plugin.make_server("127.0.0.1", 0)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        def send(method: str, path: str, body: bytes = b"") -> http.client.HTTPResponse:
            conn = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
            self.addCleanup(conn.close)
            conn.request(method, path, body=body)
            resp = conn.getresponse()
            resp.read()
            return resp

        self.assertEqual(send("GET", "/v0/discovery").getheader(MANIFEST_DIGEST_HEADER), expected)
        self.assertEqual(send("GET", "/v0/health").getheader(MANIFEST_DIGEST_HEADER), expected)
        self.assertEqual(send("GET", "/v0/unknown").getheader(MANIFEST_DIGEST_HEADER), expected)
        self.assertEqual(send("GET", "/v0/contributions/normalizer").getheader(MANIFEST_DIGEST_HEADER), expected)
        self.assertIsNone(send("GET", "/not-v0").getheader(MANIFEST_DIGEST_HEADER))

        @self.plugin.health_check
        def unhealthy():
            raise RetryableError("warming_up", "loading models")

        resp = send("GET", "/v0/health")
        self.assertEqual(resp.status, 503)
        self.assertEqual(resp.getheader(MANIFEST_DIGEST_HEADER), expected)

        ok_body = json.dumps(make_request(self.doc).to_dict()).encode()
        resp = send("POST", "/v0/contributions/normalizer", ok_body)
        self.assertEqual(resp.status, 200)
        self.assertEqual(resp.getheader(MANIFEST_DIGEST_HEADER), expected)

        # Signed API 0.14.0 401 responses also carry X-Quivr-Manifest-Digest.
        manifest_014 = Path(self.tmp.name) / "quivr-plugin.yaml"
        manifest_014.write_text(MANIFEST.read_text().replace(">=0.1.0 <0.2.0", ">=0.14.0 <0.15.0"))
        signed_plugin = Plugin(manifest_014)
        signed_plugin.normalizer(lambda inv: {"manifest": {"kind": "manifest", "parts": []}})
        signed_server = signed_plugin.make_server("127.0.0.1", 0)
        threading.Thread(target=signed_server.serve_forever, daemon=True).start()
        self.addCleanup(signed_server.server_close)
        self.addCleanup(signed_server.shutdown)
        conn = http.client.HTTPConnection("127.0.0.1", signed_server.server_port, timeout=5)
        self.addCleanup(conn.close)
        conn.request("GET", "/v0/discovery")
        resp = conn.getresponse()
        resp.read()
        self.assertEqual(resp.status, 401)
        self.assertEqual(resp.getheader(MANIFEST_DIGEST_HEADER), signed_plugin.manifest.digest)

    def test_single_json_serialization_for_normalizer_and_subscription(self):
        import quivr_plugin.server as server_mod

        real_dumps = server_mod.json.dumps
        # Normalizer HTTP path
        server = self.plugin.make_server("127.0.0.1", 0)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        body = real_dumps(make_request(self.doc).to_dict()).encode()
        with mock.patch.object(server_mod.json, "dumps", wraps=real_dumps) as spy:
            req = urllib.request.Request(f"http://127.0.0.1:{server.server_port}/v0/contributions/normalizer", data=body)
            with urllib.request.urlopen(req, timeout=5) as resp:
                self.assertEqual(resp.status, 200)
                parsed = json.load(resp)
            self.assertEqual(parsed["manifest"]["kind"], "manifest")
            response_dumps = [call for call in spy.call_args_list if call.args and call.args[0] == parsed]
            self.assertEqual(len(response_dumps), 1, "normalizer HTTP response must serialize JSON only once")

        # Subscription HTTP path
        sub_plugin = Plugin(DATA / "subscription-plugin.yaml")

        @sub_plugin.subscription
        def evaluate(invocation):
            return [match(ev, "matched", part_keys=["title"]) for ev in invocation.evaluations]

        sub_server = sub_plugin.make_server("127.0.0.1", 0)
        threading.Thread(target=sub_server.serve_forever, daemon=True).start()
        self.addCleanup(sub_server.server_close)
        self.addCleanup(sub_server.shutdown)
        sub_body = real_dumps({
            "invocation_id": "inv-1",
            "idempotency_key": "key-1",
            "contribution": "subscription",
            "organization_id": "org",
            "record": {"corpus_id": "c", "record_id": "r", "record_version_id": "v", "enriched": False,
                       "parts": [{"key": "title", "role": "title", "text": "headline"}]},
            "evaluations": [{"id": "e1", "expression": {"kind": "substring", "text": "headline"}, "configuration": {},
                             "subscriptions": [{"subscription_id": "s", "subscription_version_id": "sv",
                                                "saved_query_id": "q", "saved_query_version_id": "qv"}]}],
            "configuration": {},
        }).encode()
        with mock.patch.object(server_mod.json, "dumps", wraps=real_dumps) as spy:
            req = urllib.request.Request(f"http://127.0.0.1:{sub_server.server_port}/v0/contributions/subscription", data=sub_body)
            with urllib.request.urlopen(req, timeout=5) as resp:
                self.assertEqual(resp.status, 200)
                parsed = json.load(resp)
            self.assertEqual(len(parsed["decisions"]), 1)
            response_dumps = [call for call in spy.call_args_list if call.args and call.args[0] == parsed]
            self.assertEqual(len(response_dumps), 1, "subscription HTTP response must serialize JSON only once")

        # Reply equality remains based on status and body only.
        self.assertEqual(Reply(200, {"ok": True}, _payload=b'{"ok":true}'), Reply(200, {"ok": True}))

    def test_serve_handles_sigterm_and_sigint_gracefully(self):
        for sig in (signal.SIGTERM, signal.SIGINT):
            with self.subTest(sig=sig.name):
                plugin = make_plugin()
                entered = threading.Event()
                release = threading.Event()
                started = threading.Event()
                holder: dict[str, object] = {}
                client_result: dict[str, object] = {}

                @plugin.health_check
                def held_health():
                    entered.set()
                    if not release.wait(5):
                        raise RuntimeError("timed out waiting for shutdown signal")

                real_make_server = plugin.make_server

                def capture_server(*args, **kwargs):
                    srv = real_make_server(*args, **kwargs)
                    holder["server"] = srv
                    started.set()
                    return srv

                def run_client():
                    if not started.wait(5):
                        return
                    port = holder["server"].server_port
                    try:
                        with urllib.request.urlopen(f"http://127.0.0.1:{port}/v0/health", timeout=5) as resp:
                            client_result["status"] = resp.status
                            client_result["body"] = json.load(resp)
                            client_result["digest"] = resp.headers.get(MANIFEST_DIGEST_HEADER)
                    except Exception as exc:
                        client_result["error"] = exc

                def send_signal():
                    if not entered.wait(5):
                        return
                    os.kill(os.getpid(), sig)
                    holder["server"]._draining.wait(5)
                    release.set()

                prev = signal.getsignal(sig)
                client_thread = threading.Thread(target=run_client, daemon=True)
                signal_thread = threading.Thread(target=send_signal, daemon=True)
                client_thread.start()
                signal_thread.start()
                try:
                    with mock.patch.object(plugin, "make_server", side_effect=capture_server):
                        plugin.serve("127.0.0.1", 0, log_level=logging.ERROR)
                finally:
                    release.set()
                    client_thread.join(timeout=5)
                    signal_thread.join(timeout=5)
                    root = logging.getLogger()
                    for handler in list(root.handlers):
                        if getattr(handler, "_quivr_plugin", False):
                            root.removeHandler(handler)
                self.assertEqual(signal.getsignal(sig), prev, "serve() must restore previous signal handlers")
                self.assertNotIn("error", client_result)
                self.assertEqual(client_result.get("status"), 200)
                self.assertEqual(client_result.get("body"), {"status": "ok"})
                self.assertEqual(client_result.get("digest"), plugin.manifest.digest)

    def test_draining_rejects_new_keepalive_requests_before_dispatch(self):
        import http.client

        dispatched = []

        @self.plugin.normalizer
        def guard_normalizer(invocation):
            dispatched.append(invocation.request.invocation_id)
            raise AssertionError("normalizer must not run after draining starts")

        server = self.plugin.make_server("127.0.0.1", 0)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        conn = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
        self.addCleanup(conn.close)
        conn.request("GET", "/v0/health")
        first = conn.getresponse()
        self.assertEqual(first.status, 200)
        self.assertEqual(json.loads(first.read()), {"status": "ok"})

        # Simulate SIGTERM/SIGINT draining while the keep-alive socket stays open.
        server._draining.set()
        body = json.dumps(make_request(self.doc).to_dict()).encode()
        conn.request("POST", "/v0/contributions/normalizer", body=body)
        second = conn.getresponse()
        payload = json.loads(second.read())
        self.assertEqual(second.status, 503)
        self.assertEqual(second.getheader("Connection"), "close")
        self.assertEqual(second.getheader(MANIFEST_DIGEST_HEADER), self.plugin.manifest.digest)
        self.assertEqual(payload["code"], "shutting_down")
        self.assertIs(payload["retryable"], True)
        self.assertEqual(protocol_errors("error.schema.json", payload), [])
        self.assertEqual(dispatched, [], "draining must reject new requests before plugin dispatch")

        # Signed API 0.14.0 rejects with 503 shutting_down before authentication (not 401).
        manifest_014 = Path(self.tmp.name) / "quivr-plugin-drain.yaml"
        manifest_014.write_text(MANIFEST.read_text().replace(">=0.1.0 <0.2.0", ">=0.14.0 <0.15.0"))
        signed_plugin = Plugin(manifest_014)
        signed_plugin.normalizer(lambda inv: {"manifest": {"kind": "manifest", "parts": []}})
        signed_server = signed_plugin.make_server("127.0.0.1", 0)
        signed_server._draining.set()
        threading.Thread(target=signed_server.serve_forever, daemon=True).start()
        self.addCleanup(signed_server.server_close)
        self.addCleanup(signed_server.shutdown)
        signed_conn = http.client.HTTPConnection("127.0.0.1", signed_server.server_port, timeout=5)
        self.addCleanup(signed_conn.close)
        signed_conn.request("GET", "/v0/discovery")
        resp = signed_conn.getresponse()
        signed_payload = json.loads(resp.read())
        self.assertEqual(resp.status, 503)
        self.assertEqual(signed_payload["code"], "shutting_down")
        self.assertIs(signed_payload["retryable"], True)
        self.assertEqual(resp.getheader("Connection"), "close")
        self.assertEqual(resp.getheader(MANIFEST_DIGEST_HEADER), signed_plugin.manifest.digest)


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
