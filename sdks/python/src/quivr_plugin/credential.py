"""Invocation-local credentials and redacting loggers for source collectors."""
from __future__ import annotations

import copy
import json
import logging
from collections.abc import Iterator, Mapping
from typing import Any

REDACTED = "[redacted]"


class Credential(Mapping[str, Any]):
    """A credential readable by key, but redacted in repr and model serialization.

    Use ``decode()`` only when a source client needs the whole credential object.
    Its returned dict contains secrets and must not be logged or persisted.
    """

    def __init__(self, value: Any) -> None:
        self._value = value
        secrets = set()

        def collect(item):
            if isinstance(item, str) and len(item) >= 4:
                secrets.add(item)
                secrets.add(json.dumps(item, ensure_ascii=False)[1:-1])
                secrets.add(json.dumps(item, ensure_ascii=True)[1:-1])
                secrets.add(repr(item)[1:-1])
            elif isinstance(item, dict):
                for nested in item.values():
                    collect(nested)
            elif isinstance(item, list):
                for nested in item:
                    collect(nested)

        collect(value)
        self._secrets = sorted(secrets, key=len, reverse=True)

    def __getitem__(self, key: str) -> Any:
        return self._value[key]

    def __iter__(self) -> Iterator[str]:
        return iter(self._value or {})

    def __len__(self) -> int:
        return len(self._value or {})

    def __repr__(self) -> str:
        return REDACTED

    def decode(self) -> dict[str, Any] | None:
        """Return a copy of the decrypted credential (None for a public source)."""
        return copy.deepcopy(self._value)

    def redact(self, text: str) -> str:
        """Scrub string values of at least four characters, as in the Go kit."""
        for secret in self._secrets:
            text = text.replace(secret, REDACTED)
        return text

    def redact_value(self, value: Any) -> Any:
        if isinstance(value, Credential):
            return REDACTED
        if isinstance(value, str):
            return self.redact(value)
        if isinstance(value, Mapping):
            return {self.redact(str(key)): self.redact_value(item) for key, item in value.items()}
        if isinstance(value, (list, tuple)):
            return [self.redact_value(item) for item in value]
        if value is None or isinstance(value, (bool, int, float)):
            return value
        return self.redact(str(value))


class CredentialLogger(logging.Logger):
    """Scrub messages, extras and tracebacks before any destination handler sees them."""

    def __init__(self, credential: Credential, invocation_id: str) -> None:
        self._destination = logging.getLogger("quivr_plugin.connector")
        super().__init__(self._destination.name, self._destination.getEffectiveLevel())
        self._credential = credential
        self._invocation_id = credential.redact(invocation_id)

    def handle(self, record: logging.LogRecord) -> None:
        safe = copy.copy(record)
        safe.msg = self._credential.redact(record.getMessage())
        safe.args = ()
        if record.exc_info:
            try:
                exception = self._safe_exception(record.exc_info[1], {})
                detail = logging.Formatter().formatException((type(exception), exception, exception.__traceback__))
                safe.exc_text = self._credential.redact(detail)
            except Exception:
                safe.exc_text = "exception details unavailable"
            safe.exc_info = None
        for key, value in vars(safe).copy().items():
            if key not in ("msg", "args", "exc_info"):
                setattr(safe, key, self._credential.redact_value(value))
        safe.invocation_id = self._invocation_id
        self._destination.handle(safe)

    def _safe_exception(self, error: BaseException, seen: dict[int, BaseException]) -> BaseException:
        if id(error) in seen:
            return seen[id(error)]
        if isinstance(error, BaseExceptionGroup):
            safe = BaseExceptionGroup(self._credential.redact(error.message),
                                      [self._safe_exception(child, seen) for child in error.exceptions])
        else:
            safe = Exception(f"{type(error).__name__}: {self._credential.redact(str(error))}")
        seen[id(error)] = safe
        safe.__traceback__ = error.__traceback__
        safe.__suppress_context__ = error.__suppress_context__
        if error.__cause__ is not None:
            safe.__cause__ = self._safe_exception(error.__cause__, seen)
        if error.__context__ is not None:
            safe.__context__ = self._safe_exception(error.__context__, seen)
        for note in getattr(error, "__notes__", []):
            safe.add_note(self._credential.redact(str(note)))
        return safe
