"""Pull source collector registration and invocation safeguards."""
from __future__ import annotations

import json
import math
from dataclasses import dataclass
from typing import Any, Protocol

from .credential import Credential, CredentialLogger
from .errors import ConfigurationError, PluginError
from .manifest import LoadedManifest
from .models import ConnectorCredentialRequest, ConnectorCredentialResponse, ConnectorFetchRequest, ConnectorFetchResponse, ErrorEnvelope
from .schema import protocol_errors, schema_errors

FETCH_PATH = "/v0/contributions/connector/fetch"
CREDENTIAL_PATH = "/v0/contributions/connector/check_credential"
MAX_CONNECTOR_REQUEST_BYTES = 16 << 20


@dataclass(kw_only=True)
class FetchRequest(ConnectorFetchRequest):
    """Generated fetch model with a redacting invocation logger."""

    def __post_init__(self) -> None:
        if not isinstance(self.credential, Credential):
            self.credential = Credential(self.credential)

    @property
    def logger(self) -> CredentialLogger:
        return CredentialLogger(self.credential, self.invocation_id)


@dataclass(kw_only=True)
class CredentialRequest(ConnectorCredentialRequest):
    """Generated credential-check model with a redacting invocation logger."""

    def __post_init__(self) -> None:
        if not isinstance(self.credential, Credential):
            self.credential = Credential(self.credential)

    @property
    def logger(self) -> CredentialLogger:
        return CredentialLogger(self.credential, self.invocation_id)


class Connector(Protocol):
    """One stateless pull kind; register its class or instance with Plugin.connector."""

    def fetch(self, request: FetchRequest) -> ConnectorFetchResponse | dict[str, Any]: ...
    def check_credential(self, request: CredentialRequest) -> ConnectorCredentialResponse | dict[str, Any]: ...


class ConnectorError(PluginError):
    """A failure classified for Connector Health; scrub before bounding its message."""

    error_class = "source"
    status = 422

    def __init__(self, code: str, message: str) -> None:
        super().__init__(code, message)
        self.message = message.strip() or code
        self.args = (self.message,)

    def envelope(self) -> ErrorEnvelope:
        return ErrorEnvelope(code=self.code, message=self.message[:1024], retryable=self.retryable,
                             error_class=self.error_class,
                             retry_after_seconds=getattr(self, "retry_after_seconds", 0) or None)


class AccessError(ConnectorError):
    """The source refuses the credential or access; deposit a new credential to recover."""

    error_class = "access"
    status = 403


class TransientError(ConnectorError):
    """An outage, timeout or rate limit; optionally defer retry by up to one day."""

    error_class = "transient"
    status = 503
    retryable = True

    def __init__(self, code: str, message: str, *, retry_after_seconds: float = 0) -> None:
        super().__init__(code, message)
        self.retry_after_seconds = min(max(math.ceil(retry_after_seconds), 0), 86400)


class SourceError(ConnectorError):
    """The source returned unusable data; terminal for this run."""


class NotDue(Exception):
    """Fetch should be skipped; answer an empty page with the checkpoint unchanged."""


def _failure(status: int, code: str, message: str, credential: Credential,
             *, error_class: str | None = None, retryable: bool = False) -> tuple[int, dict[str, Any]]:
    safe_code = code if credential.redact(code) == code else "connector_error"
    body = {"code": safe_code, "message": credential.redact(message)[:1024] or safe_code, "retryable": retryable}
    if error_class is not None:
        body["error_class"] = error_class
    return status, body


def _encoded(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()


def _response_problems(manifest: LoadedManifest, document: dict[str, Any], fetch: bool) -> list[str]:
    schema = "connector-fetch-response.schema.json" if fetch else "connector-check-credential-response.schema.json"
    problems = protocol_errors(schema, document)
    contribution = manifest.model.contributions.connector
    limits = contribution.limits
    if len(_encoded(document)) > min((limits and limits.max_response_bytes) or (4 << 20), 16 << 20):
        problems.append("the response exceeds max_response_bytes")
    if not fetch:
        return problems
    if len(document.get("items", [])) > ((limits and limits.max_items) or 100):
        problems.append("the page exceeds max_items")
    if len(_encoded(document.get("checkpoint"))) > min((limits and limits.max_checkpoint_bytes) or (64 << 10), 1 << 20):
        problems.append("the checkpoint exceeds max_checkpoint_bytes")
    if len(_encoded(document.get("diagnostics"))) > (16 << 10):
        problems.append("the diagnostics exceed 16 KiB")
    if any(item.get("attachments") for item in document.get("items", [])):
        problems.append("this Python connector adapter does not serve attachments")
    if document.get("push") is not None:
        problems.append("this Python connector adapter does not serve push kinds")
    return problems


def invoke_connector(manifest: LoadedManifest, implementations: dict[str, Connector],
                     path: str, body: bytes) -> tuple[int, dict[str, Any]]:
    """Validate, dispatch and self-check one connector call without retaining credentials."""
    credential = Credential(None)
    contribution = manifest.model.contributions.connector
    if contribution is None:
        return _failure(501, "not_implemented", "the plugin declares no connector Contribution", credential)
    if len(body) > MAX_CONNECTOR_REQUEST_BYTES:
        return _failure(413, "request_too_large", "request body exceeds 16 MiB", credential)
    try:
        document = json.loads(body)
    except (UnicodeDecodeError, ValueError):
        return _failure(400, "invalid_request", "request body is not JSON", credential)
    credential = Credential(document.get("credential") if isinstance(document, dict) else None)
    fetch = path == FETCH_PATH
    schema = "connector-fetch-request.schema.json" if fetch else "connector-check-credential-request.schema.json"
    problems = protocol_errors(schema, document)
    if problems:
        return _failure(400, "invalid_request", "; ".join(problems), credential)
    kind = contribution.kinds.get(document["connector"]["kind"])
    if kind is None:
        return _failure(400, "unknown_kind", "the connector kind is not declared", credential)
    try:
        manifest.validate_configuration(document["configuration"])
    except ConfigurationError as error:
        return _failure(400, error.code, "configuration is invalid: " + "; ".join(error.problems), credential)
    problems = schema_errors(kind.config_schema, document["connector"]["config"])
    if problems:
        return _failure(400, "invalid_config", "; ".join(problems), credential)
    raw_credential = document["credential"]
    if ((kind.credential_schema is None and raw_credential is not None)
            or (kind.credential_schema is not None and raw_credential is None and kind.credential_required is not False)
            or (kind.credential_schema is not None and raw_credential is not None
                and schema_errors(kind.credential_schema, raw_credential))):
        return _failure(400, "invalid_credential", "the credential does not match the kind's credential_schema", credential)
    implementation = implementations.get(document["connector"]["kind"])
    if implementation is None:
        return _failure(501, "not_implemented", "the connector kind has no registered implementation", credential)
    request = (FetchRequest if fetch else CredentialRequest).from_dict(document)
    credential = request.credential
    try:
        response = implementation.fetch(request) if fetch else implementation.check_credential(request)
    except NotDue:
        if fetch:
            return 200, {"items": [], "checkpoint": request.checkpoint, "more": False, "not_due": True}
        return _failure(500, "internal_error", "NotDue is only valid for fetch", credential, error_class="source")
    except ConnectorError as error:
        status, envelope = _failure(error.status, error.code, error.message, credential,
                                    error_class=error.error_class, retryable=error.retryable)
        if isinstance(error, TransientError) and error.retry_after_seconds:
            envelope["retry_after_seconds"] = error.retry_after_seconds
        return status, envelope
    except OSError:
        request.logger.exception("connector failed with an unclassified I/O error")
        return _failure(503, "unexpected_error", "the plugin failed with an unclassified error; see the plugin log",
                        credential, error_class="transient", retryable=True)
    except Exception:
        request.logger.exception("connector failed unexpectedly")
        return _failure(500, "internal_error", "the plugin failed unexpectedly; see the plugin log",
                        credential, error_class="source")
    try:
        result = response if isinstance(response, dict) else response.to_dict()
        problems = _response_problems(manifest, result, fetch)
    except (AttributeError, TypeError, ValueError):
        problems = ["the connector response is not a JSON-encodable protocol response"]
    if problems:
        request.logger.error("connector returned an invalid response: %s", "; ".join(problems))
        return _failure(500, "invalid_response", "; ".join(problems), credential, error_class="source")
    return 200, result
