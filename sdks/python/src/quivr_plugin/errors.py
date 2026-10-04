"""Plugin errors and their mapping to the Plugin Protocol error envelope."""
from __future__ import annotations

import re

from .models import ErrorEnvelope

_CODE = re.compile(r"^[a-z][a-z0-9_]{0,63}$")
_MAX_MESSAGE = 1024


class PluginError(Exception):
    """An error the plugin reports to the engine through the error envelope.

    Raise RetryableError or TerminalError rather than this base class.
    """

    retryable: bool = False
    status: int = 500

    def __init__(self, code: str, message: str, *, status: int | None = None) -> None:
        if not _CODE.match(code):
            raise ValueError(f"error code {code!r} must match ^[a-z][a-z0-9_]*$ and be at most 64 characters")
        message = message.strip() or code
        if len(message) > _MAX_MESSAGE:
            message = message[: _MAX_MESSAGE - 1] + "…"
        super().__init__(message)
        self.code = code
        self.message = message
        if status is not None:
            self.status = status

    def envelope(self) -> ErrorEnvelope:
        return ErrorEnvelope(code=self.code, message=self.message, retryable=self.retryable)


class RetryableError(PluginError):
    """A transient failure: the engine retries within the declared retry intent.

    Served as HTTP 503 with ``retryable: true``.
    """

    retryable = True
    status = 503


class TerminalError(PluginError):
    """A definitive failure for this input: the engine does not retry.

    Served as HTTP 422 with ``retryable: false``; the engine quarantines the
    Record Version.
    """

    retryable = False
    status = 422


class ConfigurationError(TerminalError):
    """The invocation configuration does not satisfy the manifest configuration schema."""

    def __init__(self, problems: list[str]) -> None:
        self.problems = problems
        super().__init__("invalid_configuration", "configuration is invalid: " + "; ".join(problems), status=400)
