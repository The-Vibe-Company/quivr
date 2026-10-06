"""Authentication is enforced before the HTTP server dispatches plugin code."""
import base64
import contextlib
import hashlib
import hmac
import json
import os
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from quivr_plugin import Plugin, NormalizerResponse, ManifestContent, Part, TextContent
from quivr_plugin.signing import ENV_KEYS
from support import MANIFEST, make_request


CURRENT_SECRET = b"01234567890123456789012345678901"
OLD_SECRET = b"abcdefghijklmnopqrstuvwxyz123456"
PLUGIN_ID = "sdk-test"
DISCOVERY_TARGET = "/v0/discovery?source=engine"
NORMALIZER_TARGET = "/v0/contributions/normalizer?source=engine"


def _encode(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


def _token(secret: bytes, kid: str, *, audience: str, plugin_id: str, method: str,
           target: str, body: bytes, issued: int, expiry: int) -> str:
    header = {"alg": "HS256", "typ": "quivr-engine+jwt", "kid": kid}
    contribution = "discovery"
    path = target.split("?", 1)[0]
    if path.startswith("/v0/contributions/"):
        contribution = path.split("/")[3]
    claims = {
        "aud": audience,
        "plugin_id": plugin_id,
        "contribution": contribution,
        "method": method,
        "target": target,
        "iat": issued,
        "exp": expiry,
        "body_sha256": hashlib.sha256(body).hexdigest(),
    }
    header64 = _encode(json.dumps(header, separators=(",", ":")).encode("utf-8"))
    claims64 = _encode(json.dumps(claims, separators=(",", ":")).encode("utf-8"))
    signing_input = f"{header64}.{claims64}".encode("ascii")
    signature = hmac.new(secret, signing_input, hashlib.sha256).digest()
    return f"Bearer {header64}.{claims64}.{_encode(signature)}"


def _key(key_id: str, secret: bytes, not_before=None, not_after=None) -> dict:
    value = {"id": key_id, "secret": _encode(secret)}
    if not_before is not None:
        value["not_before"] = not_before
    if not_after is not None:
        value["not_after"] = not_after
    return value


def _ring(active: str, *keys: dict) -> str:
    return json.dumps({"active": active, "keys": list(keys)}, separators=(",", ":"))


@contextlib.contextmanager
def _environment(value):
    missing = object()
    previous = os.environ.get(ENV_KEYS, missing)
    try:
        if value is None:
            os.environ.pop(ENV_KEYS, None)
        else:
            os.environ[ENV_KEYS] = value
        yield
    finally:
        if previous is missing:
            os.environ.pop(ENV_KEYS, None)
        else:
            os.environ[ENV_KEYS] = previous


@contextlib.contextmanager
def _running_server(plugin):
    server = plugin.make_server("127.0.0.1", 0)
    thread = threading.Thread(
        target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True,
    )
    thread.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


class SignedCalls(unittest.TestCase):
    def _plugin(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = Path(directory.name) / "quivr-plugin.yaml"
        path.write_text(MANIFEST.read_text().replace(">=0.1.0 <0.2.0", ">=0.14.0 <0.15.0"))
        plugin = Plugin(path)
        plugin.normalizer(lambda invocation: {})
        return plugin

    def _request(self, server, method, target, body=b"", token=None):
        data = body if method != "GET" or body else None
        headers = {"Authorization": token} if token else {}
        request = urllib.request.Request(
            f"http://127.0.0.1:{server.server_port}{target}",
            data=data,
            headers=headers,
            method=method,
        )
        try:
            response = urllib.request.urlopen(request)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            return response.status, json.loads(raw), raw

    def _assert_invalid(self, status, document, raw):
        self.assertEqual(status, 401, raw)
        self.assertEqual(document["code"], "invalid_engine_token")

    def test_health_is_exempt_from_authentication(self):
        plugin = self._plugin()
        with _running_server(plugin) as server, _environment(None):
            for target in ["/v0/health", "/v0/health?probe=1"]:
                with self.subTest(target=target):
                    status, document, _ = self._request(server, "GET", target)
                    self.assertEqual(status, 200, document)

    def test_authentication_precedes_post_dispatch(self):
        plugin = self._plugin()
        now = int(time.time())
        calls = []

        @plugin.normalizer
        def record(invocation):
            calls.append(invocation)
            return NormalizerResponse(manifest=ManifestContent(parts=[
                Part(key="body", role="body", content=TextContent(text="authenticated"))]))

        body = json.dumps(make_request(MANIFEST).to_dict()).encode()
        ring = _ring("current", _key("current", CURRENT_SECRET))
        with _running_server(plugin) as server, _environment(ring):
            status, document, raw = self._request(server, "POST", NORMALIZER_TARGET, body)
            self._assert_invalid(status, document, raw)
            self.assertEqual(calls, [])
            token = _token(CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                           method="POST", target=NORMALIZER_TARGET, body=body,
                           issued=now-1, expiry=now+59)
            status, document, raw = self._request(server, "POST", NORMALIZER_TARGET, body, token)
            self.assertEqual(status, 200, raw)
            self.assertEqual(len(calls), 1)
            malformed = b"{"
            token = _token(CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                           method="POST", target=NORMALIZER_TARGET, body=malformed,
                           issued=now-1, expiry=now+59)
            status, document, raw = self._request(server, "POST", NORMALIZER_TARGET, malformed, token)
            self.assertEqual(status, 400, raw)
            self.assertEqual(document["code"], "invalid_request")
            self.assertEqual(len(calls), 1)

    def test_missing_or_malformed_key_configuration_fails_closed_without_echoing_secret(self):
        now = int(time.time())
        token = _token(
            CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
            method="GET", target=DISCOVERY_TARGET, body=b"", issued=now - 1, expiry=now + 59,
        )
        marker = "config-secret-marker"
        valid_key = _key("current", CURRENT_SECRET)
        cases = [
            ("missing", None),
            ("malformed JSON", '{"active":"current","keys":'),
            ("missing keys", '{"active":"current"}'),
            ("missing active", json.dumps({"keys": [valid_key]})),
            ("unknown active", _ring("retired", valid_key)),
            ("malformed secret", _ring("current", {"id": "current", "secret": marker})),
            ("short secret", _ring("current", _key("current", b"short"))),
        ]
        plugin = self._plugin()
        with _running_server(plugin) as server:
            for name, value in cases:
                with self.subTest(name=name), _environment(value):
                    status, document, raw = self._request(server, "GET", DISCOVERY_TARGET, token=token)
                    self._assert_invalid(status, document, raw)
                    self.assertNotIn(marker.encode(), raw)

    def test_key_rotation_accepts_overlap_and_rejects_retired_keys(self):
        now = int(time.time())
        old_start, old_end = now - 600, now + 600
        plugin = self._plugin()
        with _running_server(plugin) as server:
            overlap = _ring(
                "current",
                _key("old", OLD_SECRET, old_start, old_end),
                _key("current", CURRENT_SECRET),
            )
            with _environment(overlap):
                for key_id, secret in (("old", OLD_SECRET), ("current", CURRENT_SECRET)):
                    with self.subTest(key_id=key_id):
                        token = _token(
                            secret, key_id, audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                            method="GET", target=DISCOVERY_TARGET, body=b"",
                            issued=now - 1, expiry=now + 59,
                        )
                        status, document, raw = self._request(server, "GET", DISCOVERY_TARGET, token=token)
                        self.assertEqual(status, 200, raw)
                        self.assertEqual(document["plugin"]["id"], PLUGIN_ID)

            retired_at = now - 1
            retired = _ring(
                "current",
                _key("old", OLD_SECRET, not_after=retired_at),
                _key("current", CURRENT_SECRET),
            )
            token = _token(
                OLD_SECRET, "old", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=now - 1, expiry=now + 59,
            )
            with _environment(retired):
                status, document, raw = self._request(server, "GET", DISCOVERY_TARGET, token=token)
            self._assert_invalid(status, document, raw)


if __name__ == "__main__":
    unittest.main()
