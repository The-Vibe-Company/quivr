"""Verify the narrowly specified HS256 engine request token, before dispatch."""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import time

ENV_KEYS = "QUIVR_PLUGIN_SIGNING_KEYS"
TOKEN_TYPE = "quivr-engine+jwt"


def _decode(value: str) -> bytes:
    if not isinstance(value, str) or not value or any(c not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" for c in value):
        raise ValueError("invalid base64url")
    # Reject non-canonical trailing bits as well as padding and whitespace.
    raw = base64.b64decode(value + "=" * (-len(value) % 4), altchars=b"-_", validate=True)
    if base64.urlsafe_b64encode(raw).decode().rstrip("=") != value:
        raise ValueError("invalid base64url")
    return raw


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON member")
        result[key] = value
    return result


def _json(raw):
    return json.loads(raw.decode("utf-8") if isinstance(raw, bytes) else raw, object_pairs_hook=_object)


def _key(raw, now):
    if not isinstance(raw, dict) or not isinstance(raw.get("id"), str) or not raw["id"]:
        raise ValueError("invalid key")
    secret = _decode(raw.get("secret"))
    start, end = raw.get("not_before", 0), raw.get("not_after", 0)
    if type(start) is not int or type(end) is not int or start < 0 or end < 0 or (end and end <= start) or len(secret) < 32:
        raise ValueError("invalid key")
    return secret, start <= now and (not end or now < end)


def verify(authorization: str, audience: str, method: str, target: str, body: bytes) -> bool:
    """Fail closed; diagnostics must never contain tokens or key material."""
    try:
        if not authorization.startswith("Bearer ") or len(authorization) > 8192:
            return False
        header64, claims64, signature64 = authorization[7:].split(".")
        header = _json(_decode(header64))
        if not isinstance(header, dict) or set(header) != {"alg", "typ", "kid"} or header["alg"] != "HS256" or header["typ"] != TOKEN_TYPE:
            return False
        now = int(time.time())
        ring = _json(os.environ.get(ENV_KEYS, "{}"))
        keys = ring["keys"]
        if not isinstance(keys, list) or not keys or len(keys) > 16:
            return False
        seen = set()
        selected = None
        for raw in keys:
            secret, active = _key(raw, now)
            if raw["id"] in seen:
                return False
            seen.add(raw["id"])
            if raw["id"] == header["kid"] and active:
                selected = secret
        if selected is None or ring.get("active") not in seen:
            return False
        expected = hmac.digest(selected, (header64 + "." + claims64).encode("ascii"), "sha256")
        if not hmac.compare_digest(expected, _decode(signature64)):
            return False
        claims = _json(_decode(claims64))
        if not isinstance(claims, dict) or set(claims) != {"aud", "plugin_id", "contribution", "method", "target", "iat", "exp", "body_sha256"}:
            return False
        issued, expiry = claims["iat"], claims["exp"]
        if type(issued) is not int or type(expiry) is not int or issued < 0 or issued > now or expiry <= now or not 0 < expiry - issued <= 60:
            return False
        contribution = target.split("?", 1)[0].split("/")[3] if target.startswith("/v0/contributions/") else "discovery"
        return (claims["aud"] == audience and claims["plugin_id"] == audience
                and claims["contribution"] == contribution and claims["method"] == method
                and claims["target"] == target and claims["body_sha256"] == hashlib.sha256(body).hexdigest())
    except (ValueError, TypeError, KeyError, IndexError, UnicodeError, OverflowError, RecursionError):
        return False
