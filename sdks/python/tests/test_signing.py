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

from quivr_plugin import Plugin
from quivr_plugin.signing import ENV_KEYS
from support import MANIFEST


CURRENT_SECRET = b"01234567890123456789012345678901"
OLD_SECRET = b"abcdefghijklmnopqrstuvwxyz123456"
PLUGIN_ID = "sdk-test"
DISCOVERY_TARGET = "/v0/discovery?source=engine"
NORMALIZER_TARGET = "/v0/contributions/normalizer?source=engine"


def _encode(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


def _token(secret: bytes, kid: str, *, audience: str, plugin_id: str, method: str,
           target: str, body: bytes, issued: int, expiry: int,
           header_edits=None, claim_edits=None) -> str:
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
    if header_edits:
        header.update(header_edits)
    if claim_edits:
        claims.update(claim_edits)
    header64 = _encode(json.dumps(header, separators=(",", ":")).encode())
    claims64 = _encode(json.dumps(claims, separators=(",", ":")).encode())
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

    def test_unsigned_discovery_is_refused_by_the_http_server(self):
        plugin = self._plugin()
        with _running_server(plugin) as server, _environment(None):
            status, document, _ = self._request(server, "GET", DISCOVERY_TARGET)
        self.assertEqual(status, 401)
        self.assertEqual(document["code"], "invalid_engine_token")

    def test_signed_discovery_and_post_reach_http_dispatch(self):
        plugin = self._plugin()
        now = int(time.time())
        ring = _ring("current", _key("current", CURRENT_SECRET))
        with _running_server(plugin) as server, _environment(ring):
            discovery = _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=now - 1, expiry=now + 59,
            )
            status, document, _ = self._request(server, "GET", DISCOVERY_TARGET, token=discovery)
            self.assertEqual(status, 200, document)
            self.assertEqual(document["plugin"]["id"], PLUGIN_ID)

            malformed = b"{"
            post = _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="POST", target=NORMALIZER_TARGET, body=malformed,
                issued=now - 1, expiry=now + 59,
            )
            status, document, _ = self._request(server, "POST", NORMALIZER_TARGET, malformed, post)
            self.assertEqual(status, 400, document)
            self.assertEqual(document["code"], "invalid_request")

    def test_invalid_tokens_are_rejected_before_dispatch(self):
        now = int(time.time())
        cases = [
            ("unsigned", lambda _: None, "GET", DISCOVERY_TARGET, b""),
            ("forged", lambda issued: _token(
                b"wrong-signing-secret-012345678901", "current", audience=PLUGIN_ID,
                plugin_id=PLUGIN_ID, method="GET", target=DISCOVERY_TARGET, body=b"",
                issued=issued - 1, expiry=issued + 59), "GET", DISCOVERY_TARGET, b""),
            ("expired", lambda issued: _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=issued - 120,
                expiry=issued - 60), "GET", DISCOVERY_TARGET, b""),
            ("wrong audience", lambda issued: _token(
                CURRENT_SECRET, "current", audience="other-plugin", plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=issued - 1,
                expiry=issued + 59), "GET", DISCOVERY_TARGET, b""),
            ("wrong plugin id", lambda issued: _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id="other-plugin",
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=issued - 1,
                expiry=issued + 59), "GET", DISCOVERY_TARGET, b""),
            ("tampered body", lambda issued: _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"original", issued=issued - 1,
                expiry=issued + 59), "GET", DISCOVERY_TARGET, b"tampered"),
            ("wrong route", lambda issued: _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=issued - 1,
                expiry=issued + 59), "GET", "/v0/discovery?source=other", b""),
            ("wrong method", lambda issued: _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="POST", target=DISCOVERY_TARGET, body=b"", issued=issued - 1,
                expiry=issued + 59), "GET", DISCOVERY_TARGET, b""),
            ("future issued at", lambda issued: _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=issued + 10,
                expiry=issued + 20), "GET", DISCOVERY_TARGET, b""),
            ("lifetime over sixty seconds", lambda issued: _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=issued - 1,
                expiry=issued + 70), "GET", DISCOVERY_TARGET, b""),
            ("algorithm confusion", lambda issued: _token(
                CURRENT_SECRET, "current", audience=PLUGIN_ID, plugin_id=PLUGIN_ID,
                method="GET", target=DISCOVERY_TARGET, body=b"", issued=issued - 1,
                expiry=issued + 59, header_edits={"alg": "none"}), "GET", DISCOVERY_TARGET, b""),
        ]
        plugin = self._plugin()
        ring = _ring("current", _key("current", CURRENT_SECRET))
        with _running_server(plugin) as server:
            for name, make_token, method, target, body in cases:
                with self.subTest(name=name), _environment(ring):
                    token = make_token(now)
                    status, document, raw = self._request(server, method, target, body, token)
                    self._assert_invalid(status, document, raw)

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
