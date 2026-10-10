"""Pull source collector registration and invocation safeguards."""
from __future__ import annotations

import json
import math
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any, Protocol

from .api_versions import FEATURE_SINCE
from .credential import Credential, CredentialLogger
from .errors import ConfigurationError, PluginError
from ._json import storage_encoded
from .manifest import LoadedManifest
from .models import (
    ConnectorCredentialRequest,
    ConnectorCredentialResponse,
    ConnectorDescribeAttachmentRequest,
    ConnectorDescribeAttachmentResponse,
    ConnectorFetchRequest,
    ConnectorFetchResponse,
    ConnectorReceiveRequest,
    ConnectorReceiveResponse,
    ConnectorUploadAttachmentRequest,
    ConnectorUploadAttachmentResponse,
    ErrorEnvelope,
)
from .schema import protocol_errors, schema_errors

FETCH_PATH = "/v0/contributions/connector/fetch"
CREDENTIAL_PATH = "/v0/contributions/connector/check_credential"
RECEIVE_PATH = "/v0/contributions/connector/receive"
DESCRIBE_ATTACHMENT_PATH = "/v0/contributions/connector/describe_attachment"
UPLOAD_ATTACHMENT_PATH = "/v0/contributions/connector/upload_attachment"
MAX_CONNECTOR_REQUEST_BYTES = 16 << 20
MAX_RECEIVE_RESPONSE_BODY_BYTES = 64 << 10
MAX_ATTACHMENT_RESPONSE_BYTES = 16 << 20
MAX_GENERIC_JSON_BYTES = 64 << 10
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


@dataclass(kw_only=True)
class ReceiveRequest(ConnectorReceiveRequest):
    """Generated receive model with a redacting invocation logger."""

    def __post_init__(self) -> None:
        if not isinstance(self.credential, Credential):
            self.credential = Credential(self.credential)

    @property
    def logger(self) -> CredentialLogger:
        return CredentialLogger(self.credential, self.invocation_id)


@dataclass(kw_only=True)
class DescribeAttachmentRequest(ConnectorDescribeAttachmentRequest):
    """Generated attachment-description model with a redacting logger."""

    def __post_init__(self) -> None:
        if not isinstance(self.credential, Credential):
            self.credential = Credential(self.credential)

    @property
    def logger(self) -> CredentialLogger:
        return CredentialLogger(self.credential, self.invocation_id)


@dataclass(kw_only=True)
class UploadAttachmentRequest(ConnectorUploadAttachmentRequest):
    """Generated attachment-upload model with credential and grant redaction."""

    def __post_init__(self) -> None:
        if not isinstance(self.credential, Credential):
            self.credential = Credential(self.credential)

    def __repr__(self) -> str:
        return f"{type(self).__name__}(invocation_id={self.invocation_id!r}, credential={self.credential!r}, grant=[redacted])"

    @property
    def logger(self) -> CredentialLogger:
        grant = self.grant.to_dict()
        values = [grant.get("url", ""), *(grant.get("headers") or {}).values()]
        raw = {"credential": self.credential.decode(), "grant": values}
        return CredentialLogger(Credential(raw), self.invocation_id)


class Connector(Protocol):
    """Pull operations for one stateless connector kind.

    ``receive`` and the attachment methods are optional capabilities.  A
    pull-only connector therefore only needs to implement this protocol;
    :class:`Receiver` and :class:`AttachmentSource` describe the additional
    methods when a manifest declares those capabilities.
    """

    def fetch(self, request: FetchRequest) -> ConnectorFetchResponse | dict[str, Any]: ...
    def check_credential(self, request: CredentialRequest) -> ConnectorCredentialResponse | dict[str, Any]: ...


class Receiver(Protocol):
    """Optional push delivery capability for a connector kind."""

    def receive(self, request: ReceiveRequest) -> ConnectorReceiveResponse | dict[str, Any]: ...


ConnectorRouteHandler = Callable[[ReceiveRequest], ConnectorReceiveResponse]


class AttachmentSource(Protocol):
    """Optional attachment description and upload capability."""

    def describe_attachment(self, request: DescribeAttachmentRequest) -> ConnectorDescribeAttachmentResponse | dict[str, Any]: ...
    def upload_attachment(self, request: UploadAttachmentRequest) -> ConnectorUploadAttachmentResponse | dict[str, Any]: ...


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


def _contains_nul(value: Any) -> bool:
    """Return whether a JSON value contains NUL in a string or object key."""
    if isinstance(value, str):
        return "\x00" in value
    if isinstance(value, dict):
        return any(_contains_nul(key) or _contains_nul(item) for key, item in value.items())
    if isinstance(value, (list, tuple)):
        return any(_contains_nul(item) for item in value)
    return False


def _extensions_problems(manifest: LoadedManifest, extensions: Any, prefix: str) -> list[str]:
    if extensions is None:
        return []
    declared = manifest.model.extensions or {}
    problems: list[str] = []
    try:
        if len(storage_encoded(extensions)) > MAX_GENERIC_JSON_BYTES:
            problems.append(f"{prefix}: extensions exceed {MAX_GENERIC_JSON_BYTES} bytes of JSON")
    except (TypeError, ValueError):
        problems.append(f"{prefix}: extensions are not JSON-encodable")
    for namespace, entry in extensions.items():
        versions = declared.get(namespace)
        if versions is None:
            problems.append(f"{prefix}/{namespace}: extension namespace is not declared")
            continue
        version = entry.get("schema_version")
        schema = versions.get(version)
        if schema is None:
            problems.append(f"{prefix}/{namespace}: extension schema version {version!r} is not declared")
            continue
        data = entry.get("data")
        try:
            if len(storage_encoded(data)) > MAX_GENERIC_JSON_BYTES:
                problems.append(f"{prefix}/{namespace}/data: extension data exceeds {MAX_GENERIC_JSON_BYTES} bytes of JSON")
        except (TypeError, ValueError):
            problems.append(f"{prefix}/{namespace}/data: extension data is not JSON-encodable")
        if _contains_nul(data):
            problems.append(f"{prefix}/{namespace}/data: extension data contains NUL")
        for problem in schema_errors(schema, data):
            problems.append(f"{prefix}/{namespace}/data{problem}")
    return problems


def _manifest_problems(manifest: LoadedManifest, content: dict[str, Any], prefix: str,
                       attachments: list[dict[str, Any]]) -> list[str]:
    """Apply the engine's structural Manifest checks to a connector item.

    The protocol schema describes each Part and attachment, but it cannot
    express the combined key and parent hierarchy after attachment
    descriptors become Blob Parts.  Keeping this check here prevents a
    connector from returning a page that the engine would reject later.
    """
    problems: list[str] = []
    parts = list(content.get("parts") or [])
    if not parts and not attachments:
        problems.append(f"{prefix}/parts: the Manifest needs at least one Part or attachment")
    if not parts and attachments:
        attachment = attachments[0]
        if (len(attachments) != 1 or content.get("relations") or attachment.get("key") != "source"
                or attachment.get("role") != "source" or attachment.get("parent_key")
                or attachment.get("extensions")):
            problems.append(f"{prefix}: attachment-only input requires exactly one attachment with key and role source, no parent or Part extensions, and no relations")
    if not parts and attachments and tuple(map(int, manifest.plugin_api.split("."))) < tuple(map(int, FEATURE_SINCE["connector_attachment_only"].split("."))):
        problems.append(f"{prefix}/parts: attachment-only Manifests require Plugin API " + FEATURE_SINCE["connector_attachment_only"])
    if len(parts) + len(attachments) > 256:
        problems.append(f"{prefix}/parts: the Manifest has more than 256 Parts")

    keys: dict[str, str] = {}
    for index, part in enumerate(parts):
        part_prefix = f"{prefix}/parts/{index}"
        key = part.get("key")
        if key in keys:
            problems.append(f"{part_prefix}/key: Part key is duplicated")
        else:
            keys[key] = part_prefix
        part_content = part.get("content") or {}
        kind = part_content.get("kind")
        if kind == "blob":
            problems.append(f"{part_prefix}/content: Blob Parts are not allowed in connector manifests")
        elif kind == "text":
            text = part_content.get("text") or ""
            if not text or "\x00" in text:
                problems.append(f"{part_prefix}/content/text: text must be non-empty and contain no NUL")
        problems.extend(_extensions_problems(manifest, part.get("extensions"), part_prefix + "/extensions"))

    for index, attachment in enumerate(attachments):
        attachment_prefix = f"{prefix.rsplit('/content', 1)[0]}/attachments/{index}"
        key = attachment.get("key")
        if key in keys:
            problems.append(f"{attachment_prefix}/key: attachment key duplicates a Part key")
        else:
            keys[key] = attachment_prefix
        problems.extend(_extensions_problems(manifest, attachment.get("extensions"),
                                             attachment_prefix + "/extensions"))

    # Parent references may target either an inline Part or an attachment,
    # and must form one acyclic hierarchy.
    parent_of: dict[str, str] = {}
    for index, part in enumerate(parts):
        if part.get("parent_key"):
            parent_of[part["key"]] = part["parent_key"]
    for index, attachment in enumerate(attachments):
        if attachment.get("parent_key"):
            parent_of[attachment["key"]] = attachment["parent_key"]
    for key, parent in parent_of.items():
        if parent not in keys:
            problems.append(f"{keys.get(key, prefix)}/parent_key: unknown parent {parent!r}")
            continue
        seen: set[str] = set()
        current = key
        while current in parent_of:
            if current in seen:
                problems.append(f"{keys.get(key, prefix)}/parent_key: Part parent cycle")
                break
            seen.add(current)
            current = parent_of[current]
            if current == key:
                problems.append(f"{keys.get(key, prefix)}/parent_key: Part cannot be its own ancestor")
                break

    # content.CheckManifest bounds only the non-text shape.  Reconstruct the
    # same shape after attachment descriptors become Blob Parts, retaining
    # Part metadata, extension payloads and relations while omitting text.
    def structural_part(part: dict[str, Any]) -> dict[str, Any]:
        result = {key: part[key] for key in ("key", "parent_key", "role", "extensions")
                  if key in part and part[key] is not None}
        part_content = dict(part.get("content") or {})
        part_content.pop("text", None)
        result["content"] = part_content
        return result

    structural_parts = [structural_part(part) for part in parts]
    for attachment in attachments:
        part = {
            "key": attachment.get("key"),
            "role": attachment.get("role"),
            "content": {
                "kind": "blob",
                # Blob IDs are UUIDs in the engine; reserve the full shape
                # when checking the synthetic Part's persisted structure.
                "blob_id": "00000000-0000-0000-0000-000000000000",
                "media_type": attachment.get("media_type"),
            },
        }
        if attachment.get("parent_key") is not None:
            part["parent_key"] = attachment["parent_key"]
        if attachment.get("extensions") is not None:
            part["extensions"] = attachment["extensions"]
        structural_parts.append(part)
    structure: dict[str, Any] = {"parts": structural_parts}
    if content.get("relations"):
        structure["relations"] = content["relations"]
    try:
        if len(storage_encoded(structure)) > MAX_GENERIC_JSON_BYTES:
            problems.append(f"{prefix}: non-text Manifest structure exceeds {MAX_GENERIC_JSON_BYTES} bytes of JSON")
    except (TypeError, ValueError):
        problems.append(f"{prefix}: non-text Manifest structure is not JSON-encodable")
    return problems


def _item_problems(manifest: LoadedManifest, item: dict[str, Any], prefix: str,
                   *, attachments_allowed: bool) -> list[str]:
    problems: list[str] = []
    has_content = item.get("content") is not None
    withdrawn = item.get("withdraw") is True
    if has_content == withdrawn:
        problems.append(f"{prefix}: exactly one of content and withdraw: true is required")
    if withdrawn:
        if item.get("attachments"):
            problems.append(f"{prefix}/attachments: a withdrawal carries no attachments")
        if item.get("extensions"):
            problems.append(f"{prefix}/extensions: a withdrawal carries no extensions")
        return problems
    content = item.get("content") or {}
    if item.get("attachments"):
        if not attachments_allowed:
            problems.append(f"{prefix}/attachments: attachments are not declared by the manifest")
        if content.get("kind") != "manifest":
            problems.append(f"{prefix}/attachments: attachments require manifest content")
    if content.get("kind") == "manifest":
        problems.extend(_manifest_problems(manifest, content, prefix + "/content", item.get("attachments") or []))
    elif content.get("kind") == "text" and "\x00" in (content.get("text") or ""):
        problems.append(f"{prefix}/content/text: text contains NUL")
    problems.extend(_extensions_problems(manifest, item.get("extensions"), prefix + "/extensions"))
    seen_attachments: set[str] = set()
    for index, attachment in enumerate(item.get("attachments") or []):
        key = attachment.get("key")
        if key in seen_attachments:
            problems.append(f"{prefix}/attachments/{index}/key: attachment key is duplicated")
        seen_attachments.add(key)
        if not attachment.get("ref"):
            problems.append(f"{prefix}/attachments/{index}/ref: attachment ref is required")
        if attachment.get("sha256") is not None and attachment.get("size_bytes") is None:
            problems.append(f"{prefix}/attachments/{index}: sha256 requires size_bytes")
        if (attachment.get("size_bytes") is not None
                and attachment.get("size_bytes") > manifest.attachment_max_bytes
                and attachment.get("sha256") is not None):
            problems.append(f"{prefix}/attachments/{index}/size_bytes: attachment exceeds attachments.max_bytes")
        problems.extend(_extensions_problems(manifest, attachment.get("extensions"),
                                             f"{prefix}/attachments/{index}/extensions"))
    return problems


def _same_json(left: Any, right: Any) -> bool:
    try:
        return _encoded(left) == _encoded(right)
    except (TypeError, ValueError):
        return False


def _response_problems(manifest: LoadedManifest, document: dict[str, Any], operation: str,
                       *, kind: Any = None, request: Any = None) -> list[str]:
    schemas = {
        "fetch": "connector-fetch-response.schema.json",
        "check_credential": "connector-check-credential-response.schema.json",
        "receive": "connector-receive-response.schema.json",
        "describe_attachment": "connector-describe-attachment-response.schema.json",
        "upload_attachment": "connector-upload-attachment-response.schema.json",
    }
    problems = protocol_errors(schemas[operation], document)
    if problems:
        return problems
    try:
        if len(_encoded(document)) > (
                MAX_ATTACHMENT_RESPONSE_BYTES if operation in ("describe_attachment", "upload_attachment")
                else manifest.connector_max_response_bytes):
            problems.append("the response exceeds max_response_bytes")
    except (TypeError, ValueError):
        return ["the response is not a JSON-encodable protocol response"]
    contribution = manifest.model.contributions.connector
    if operation == "check_credential":
        return problems
    if operation == "describe_attachment":
        if document.get("skip") is None and document.get("size_bytes", 0) > manifest.attachment_max_bytes:
            problems.append("the attachment exceeds attachments.max_bytes; answer with skip")
        problems.extend(_extensions_problems(manifest, document.get("item_extensions"), "/item_extensions"))
        return problems
    if operation == "upload_attachment":
        return problems
    if operation == "fetch":
        if document.get("submission_concurrency") is not None and tuple(map(int, manifest.plugin_api.split("."))) < tuple(map(int, FEATURE_SINCE["connector_submission_concurrency"].split("."))):
            problems.append("submission_concurrency requires Plugin API " + FEATURE_SINCE["connector_submission_concurrency"])
        allow_repeated = document.get("allow_repeated_record_keys")
        ordered_records_since = FEATURE_SINCE["connector_ordered_records"]
        if allow_repeated is not None and tuple(map(int, manifest.plugin_api.split("."))) < tuple(map(int, ordered_records_since.split("."))):
            problems.append("allow_repeated_record_keys requires Plugin API " + ordered_records_since)
        items = document.get("items", [])
        if len(items) > manifest.connector_max_items:
            problems.append("the page exceeds max_items")
        try:
            if len(_encoded(document.get("checkpoint"))) > manifest.connector_max_checkpoint_bytes:
                problems.append("the checkpoint exceeds max_checkpoint_bytes")
            if len(_encoded(document.get("diagnostics"))) > (16 << 10):
                problems.append("the diagnostics exceed 16 KiB")
        except (TypeError, ValueError):
            problems.append("the checkpoint or diagnostics is not JSON-encodable")
        if document.get("not_due") and request is not None:
            if items or document.get("more") or not _same_json(document.get("checkpoint"), request.checkpoint):
                problems.append("not_due requires no items, more false and the checkpoint unchanged")
        if document.get("push") is not None:
            pushes = kind is not None and "push" in (kind.modes or [])
            if not pushes:
                problems.append("a push status requires a push connector kind")
            else:
                push = document["push"]
                state = push.get("state")
                if state == "failed" and (not push.get("error_class") or not push.get("code")):
                    problems.append("a failed push status requires error_class and code")
                if state != "failed" and push.get("error_class"):
                    problems.append("error_class is only valid for a failed push status")
                if state == "active" and push.get("code"):
                    problems.append("an active push status carries no code")
                if state != "active" and push.get("poll_interval_seconds") is not None:
                    problems.append("poll_interval_seconds requires an active push status")
        seen_records: set[str] = set()
        attachments_allowed = contribution.attachments is not None
        for index, item in enumerate(items):
            record_key = item.get("record_key")
            if record_key in seen_records and not allow_repeated:
                problems.append(f"/items/{index}/record_key: record key is duplicated")
            seen_records.add(record_key)
            problems.extend(_item_problems(manifest, item, f"/items/{index}",
                                           attachments_allowed=attachments_allowed))
        return problems
    # receive
    items = document.get("items") or []
    response = document.get("response") or {}
    status = response.get("status", 0)
    verdict = document.get("verdict")
    if verdict == "accepted" and not 200 <= status <= 299:
        problems.append("an accepted delivery must answer with a 2xx status")
    if verdict == "refused" and not 400 <= status <= 499:
        problems.append("a refused delivery must answer with a 4xx status")
    if verdict == "refused" and items:
        problems.append("a refused delivery carries no items")
    if len(items) > manifest.connector_max_items:
        problems.append("the delivery exceeds max_items")
    if len((response.get("body") or "").encode()) > MAX_RECEIVE_RESPONSE_BODY_BYTES:
        problems.append("the delivery response body exceeds 64 KiB")
    seen_records: set[str] = set()
    for index, item in enumerate(items):
        record_key = item.get("record_key")
        if record_key in seen_records:
            problems.append(f"/items/{index}/record_key: record key is duplicated")
        seen_records.add(record_key)
        problems.extend(_item_problems(manifest, item, f"/items/{index}", attachments_allowed=False))
    return problems


def _redactor(request: Any) -> Credential:
    credential = request.credential
    if isinstance(request, UploadAttachmentRequest):
        grant = request.grant.to_dict()
        return Credential({"credential": credential.decode(),
                           "grant": [grant.get("url", ""), *(grant.get("headers") or {}).values()]})
    return credential


def _match_route_path(pattern: str, path: str) -> bool:
    """Match one relative path against a declared literal/template path."""
    patterns, actual = pattern.split("/"), path.split("/")
    if len(patterns) != len(actual):
        return False
    for declared, segment in zip(patterns, actual):
        if segment in ("", ".", ".."):
            return False
        if not declared.startswith("{") and declared != segment:
            return False
    return True


def invoke_connector(manifest: LoadedManifest, implementations: dict[str, Connector],
                     path: str, body: bytes,
                     route_handlers: dict[str, dict[str, ConnectorRouteHandler]] | None = None) -> tuple[int, dict[str, Any]]:
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
    operations = {
        FETCH_PATH: ("fetch", "connector-fetch-request.schema.json", FetchRequest, ConnectorFetchResponse),
        CREDENTIAL_PATH: ("check_credential", "connector-check-credential-request.schema.json", CredentialRequest, ConnectorCredentialResponse),
        RECEIVE_PATH: ("receive", "connector-receive-request.schema.json", ReceiveRequest, ConnectorReceiveResponse),
        DESCRIBE_ATTACHMENT_PATH: ("describe_attachment", "connector-describe-attachment-request.schema.json", DescribeAttachmentRequest, ConnectorDescribeAttachmentResponse),
        UPLOAD_ATTACHMENT_PATH: ("upload_attachment", "connector-upload-attachment-request.schema.json", UploadAttachmentRequest, ConnectorUploadAttachmentResponse),
    }
    operation, schema, request_type, response_type = operations[path]
    problems = protocol_errors(schema, document)
    if problems:
        return _failure(400, "invalid_request", "; ".join(problems), credential)
    kind_name = document["connector"]["kind"]
    kind = contribution.kinds.get(kind_name)
    if kind is None:
        return _failure(400, "unknown_kind", "the connector kind is not declared", credential)
    if operation == "receive" and "push" not in (kind.modes or []):
        return _failure(400, "push_unsupported", f"connector kind {kind_name!r} does not declare push", credential)
    route_handler: ConnectorRouteHandler | None = None
    if operation == "receive" and document.get("route") is not None and kind.api is not None:
        route_name = document["route"]
        route = next((declared for declared in kind.api.routes if declared.name == route_name), None)
        if route is None:
            return _failure(400, "unknown_route", f"the connector route {route_name!r} is not declared", credential)
        request_document = document.get("request") or {}
        request_method = request_document.get("method")
        request_path = request_document.get("path")
        if request_method != route.method:
            return _failure(400, "invalid_request",
                            f"route {route_name!r} accepts {route.method}, not {request_method}", credential)
        if not isinstance(request_path, str) or not _match_route_path(route.path, request_path):
            return _failure(400, "invalid_request",
                            f"request path {request_path!r} does not match route {route.path!r}", credential)
        if route.request_schema is not None:
            try:
                route_problems = schema_errors(route.request_schema, document.get("body"))
            except (TypeError, ValueError) as error:
                route_problems = [f"route request_schema is invalid: {error}"]
            if route_problems:
                return _failure(400, "invalid_request", "; ".join(route_problems), credential)
        route_handler = (route_handlers or {}).get(kind_name, {}).get(route_name)
    if operation in ("describe_attachment", "upload_attachment") and contribution.attachments is None:
        return _failure(400, "attachments_unsupported", "the connector does not declare attachments", credential)
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
    implementation = implementations.get(kind_name)
    if implementation is None:
        return _failure(501, "not_implemented", "the connector kind has no registered implementation", credential)
    handler = route_handler if route_handler is not None else getattr(implementation, operation, None)
    if not callable(handler):
        if operation == "receive" and document.get("route") is None:
            return _failure(400, "push_unsupported", f"connector kind {kind_name!r} has no receive handler", credential)
        return _failure(501, "not_implemented", f"the connector kind has no registered {operation} handler", credential)
    try:
        request = request_type.from_dict(document)
    except (TypeError, ValueError) as error:
        return _failure(400, "invalid_request", str(error), credential)
    credential = request.credential
    redactor = _redactor(request)
    try:
        response = handler(request)
    except NotDue:
        if operation == "fetch":
            return 200, {"items": [], "checkpoint": request.checkpoint, "more": False, "not_due": True}
        return _failure(500, "internal_error", "NotDue is only valid for fetch", redactor, error_class="source")
    except ConnectorError as error:
        status, envelope = _failure(error.status, error.code, error.message, redactor,
                                     error_class=error.error_class, retryable=error.retryable)
        if isinstance(error, TransientError) and error.retry_after_seconds:
            envelope["retry_after_seconds"] = error.retry_after_seconds
        return status, envelope
    except OSError:
        request.logger.exception("connector failed with an unclassified I/O error")
        return _failure(503, "unexpected_error", "the plugin failed with an unclassified error; see the plugin log",
                        redactor, error_class="transient", retryable=True)
    except Exception:
        request.logger.exception("connector failed unexpectedly")
        return _failure(500, "internal_error", "the plugin failed unexpectedly; see the plugin log",
                        redactor, error_class="source")
    try:
        result = response.to_dict() if isinstance(response, response_type) else response_type.from_dict(response).to_dict()
        problems = _response_problems(manifest, result, operation, kind=kind, request=request)
        if redactor.redact(json.dumps(result, ensure_ascii=False, separators=(",", ":"))) != json.dumps(result, ensure_ascii=False, separators=(",", ":")):
            problems.append("the response contains credential or grant data")
    except (AttributeError, TypeError, ValueError):
        problems = ["the connector response is not a JSON-encodable protocol response"]
    if problems:
        request.logger.error("connector returned an invalid response: %s", "; ".join(problems))
        return _failure(500, "invalid_response", "; ".join(problems), redactor, error_class="source")
    return 200, result
