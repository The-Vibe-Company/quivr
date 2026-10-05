"""Structured logging correlated with the current invocation.

While an invocation runs, every log record (from any logger) carries its
``invocation_id`` and ``idempotency_key``. ``configure_logging`` installs a
JSON-lines handler on stderr; plugins may keep their own handlers instead and
read the same attributes from the record.
"""
from __future__ import annotations

import contextlib
import contextvars
import datetime
import json
import logging
import sys
from .tracing import log_fields
from collections.abc import Iterator

_invocation: contextvars.ContextVar[tuple[str, str] | None] = contextvars.ContextVar("quivr_plugin_invocation", default=None)

logger = logging.getLogger("quivr_plugin")

_STANDARD = set(vars(logging.makeLogRecord({}))) | {"message", "asctime", "invocation_id", "idempotency_key"}


@contextlib.contextmanager
def invocation_context(invocation_id: str, idempotency_key: str) -> Iterator[None]:
    """Correlate log records emitted inside the block with one invocation."""
    token = _invocation.set((invocation_id, idempotency_key))
    try:
        yield
    finally:
        _invocation.reset(token)


def current_invocation_id() -> str | None:
    value = _invocation.get()
    return value[0] if value else None


class InvocationFilter(logging.Filter):
    """Adds invocation_id and idempotency_key attributes (None outside an invocation)."""

    def filter(self, record: logging.LogRecord) -> bool:
        value = _invocation.get()
        record.invocation_id = value[0] if value else getattr(record, "invocation_id", None)
        record.idempotency_key = value[1] if value else None
        for key, field in log_fields().items():
            setattr(record, key, field)
        return True


class JSONFormatter(logging.Formatter):
    """One JSON object per line: time, level, logger, message, invocation fields and extras."""

    def format(self, record: logging.LogRecord) -> str:
        entry = {
            "time": datetime.datetime.fromtimestamp(record.created, datetime.UTC).isoformat(timespec="milliseconds"),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
        }
        for key in ("invocation_id", "idempotency_key"):
            if getattr(record, key, None):
                entry[key] = getattr(record, key)
        for key, value in vars(record).items():
            if key not in _STANDARD and not key.startswith("_") and value is not None:
                entry[key] = value
        if record.exc_info:
            entry["exception"] = self.formatException(record.exc_info)
        elif record.exc_text:
            entry["exception"] = record.exc_text
        return json.dumps(entry, default=str, ensure_ascii=False)


def configure_logging(level: int | str = logging.INFO, stream=None) -> logging.Handler:
    """Send all logging to a JSON-lines handler (stderr by default) with invocation correlation."""
    handler = logging.StreamHandler(stream or sys.stderr)
    handler.setFormatter(JSONFormatter())
    handler.addFilter(InvocationFilter())
    root = logging.getLogger()
    for existing in list(root.handlers):
        if getattr(existing, "_quivr_plugin", False):
            root.removeHandler(existing)
    handler._quivr_plugin = True  # type: ignore[attr-defined]
    root.addHandler(handler)
    root.setLevel(level)
    return handler
