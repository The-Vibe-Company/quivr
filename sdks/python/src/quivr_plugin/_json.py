"""Internal JSON encodings used by protocol and persistence bounds."""
from __future__ import annotations

import json
import math
from json.encoder import encode_basestring, _make_iterencode
from typing import Any


def _go_float(value: float) -> str:
    """Format a Python float as Go's encoding/json formats float64 values."""
    if not math.isfinite(value):
        raise ValueError("Out of range float values are not JSON compliant: " + repr(value))
    if value == 0:
        return "-0" if math.copysign(1.0, value) < 0 else "0"

    text = repr(value).lower()
    use_exponent = abs(value) < 1e-6 or abs(value) >= 1e21
    if "e" not in text:
        return text[:-2] if text.endswith(".0") else text

    mantissa, exponent_text = text.split("e", 1)
    exponent = int(exponent_text)
    if use_exponent:
        if mantissa.endswith(".0"):
            mantissa = mantissa[:-2]
        return f"{mantissa}e{exponent:+d}"

    sign = ""
    if mantissa.startswith("-"):
        sign, mantissa = "-", mantissa[1:]
    whole, _, fraction = mantissa.partition(".")
    digits = whole + fraction
    decimal_index = len(whole) + exponent
    if decimal_index <= 0:
        fixed = "0." + ("0" * -decimal_index) + digits
    elif decimal_index >= len(digits):
        fixed = digits + ("0" * (decimal_index - len(digits)))
    else:
        fixed = digits[:decimal_index] + "." + digits[decimal_index:]
    return sign + fixed


class _GoStorageEncoder(json.JSONEncoder):
    """Compact encoder with Go's float formatting and JSON string escaping."""

    def __init__(self) -> None:
        super().__init__(ensure_ascii=False, separators=(",", ":"), allow_nan=False)

    def iterencode(self, value: Any, _one_shot: bool = False):
        markers = {} if self.check_circular else None

        def floatstr(number: float) -> str:
            return _go_float(number)

        iterencode = _make_iterencode(
            markers, self.default, encode_basestring, self.indent, floatstr,
            self.key_separator, self.item_separator, self.sort_keys, self.skipkeys,
            _one_shot,
        )
        return iterencode(value, 0)


def storage_encoded(value: Any) -> bytes:
    """Encode compact JSON with the escaping used by Go's persisted JSON."""
    # Connector and ingestion responses arrive at the engine as JSON. Its
    # generic decoder stores every JSON number as float64 before marshalling
    # the persisted value, so normalize integer tokens before measuring.
    wire = json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
    normalized = json.loads(wire, parse_int=float, parse_float=float)
    encoded = "".join(_GoStorageEncoder().iterencode(normalized))
    encoded = (encoded.replace("<", r"\u003c")
                      .replace(">", r"\u003e")
                      .replace("&", r"\u0026")
                      .replace("\u2028", r"\u2028")
                      .replace("\u2029", r"\u2029"))
    return encoded.encode()
