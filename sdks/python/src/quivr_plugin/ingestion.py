"""Ingestion Contribution registration, dispatch and output checks."""
from __future__ import annotations

import json
import logging
import math
import struct
from collections.abc import Callable
from typing import Any

from .errors import ConfigurationError, PluginError
from ._json import storage_encoded
from .logs import invocation_context
from .manifest import LoadedManifest
from .models import (
    EmbedQueryRequest,
    EmbedQueryResponse,
    SegmentAndEmbedRequest,
    SegmentAndEmbedResponse,
)
from .schema import protocol_errors

SEGMENT_AND_EMBED_PATH = "/v0/contributions/ingestion/segment_and_embed"
EMBED_QUERY_PATH = "/v0/contributions/ingestion/embed_query"
MAX_INGESTION_REQUEST_BYTES = 16 << 20
MAX_EMBED_QUERY_RESPONSE_BYTES = 1 << 20
MAX_LEXICAL_TEXT_CODEPOINTS = 16384
MAX_PROVENANCE_BYTES = 4 << 10
MAX_FLOAT32 = 3.4028234663852886e38

SegmentAndEmbedHandler = Callable[[SegmentAndEmbedRequest], SegmentAndEmbedResponse | dict[str, Any]]
EmbedQueryHandler = Callable[[EmbedQueryRequest], EmbedQueryResponse | dict[str, Any]]

log = logging.getLogger("quivr_plugin.ingestion")


def _encoded(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()


def _contains_nul(value: Any) -> bool:
    if isinstance(value, str):
        return "\x00" in value
    if isinstance(value, dict):
        return any(_contains_nul(key) or _contains_nul(item) for key, item in value.items())
    if isinstance(value, list):
        return any(_contains_nul(item) for item in value)
    return False


def _finite_float32(value: Any) -> bool:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return False
    try:
        numeric = float(value)
    except (OverflowError, ValueError):
        return False
    if not math.isfinite(numeric) or abs(numeric) > MAX_FLOAT32:
        return False
    try:
        struct.pack("!f", numeric)
    except (OverflowError, struct.error):
        return False
    return True


def _response_document(response: Any, model: type) -> dict[str, Any]:
    if isinstance(response, model):
        return response.to_dict()
    if not isinstance(response, dict):
        raise TypeError(f"expected {model.__name__} or a response object")
    return model.from_dict(response).to_dict()


def _output_problems(manifest: LoadedManifest, request: SegmentAndEmbedRequest,
                     document: dict[str, Any]) -> list[str]:
    problems = protocol_errors("ingestion-segment-and-embed-response.schema.json", document)
    if problems:
        return problems
    try:
        if len(_encoded(document)) > manifest.ingestion_max_response_bytes:
            problems.append("the response exceeds max_response_bytes")
    except (TypeError, ValueError):
        problems.append("the response is not JSON-encodable")
        return problems
    segments = document.get("segments", [])
    if len(segments) > manifest.ingestion_max_segments:
        problems.append("the response exceeds max_segments")

    lengths = {part.key: len(part.text) for part in request.parts}
    has_title = any(part.role == "title" for part in request.parts)
    spaces = manifest.model.contributions.ingestion.spaces
    requested = set(request.spaces)
    seen: set[tuple[str, int, int]] = set()
    for index, segment in enumerate(segments):
        prefix = f"/segments/{index}"
        part_key = segment.get("part_key")
        start = segment.get("start")
        end = segment.get("end")
        if part_key not in lengths:
            problems.append(f"{prefix}/part_key: the Part key is not in the request")
        elif start > end or end > lengths[part_key]:
            problems.append(f"{prefix}: offsets [{start}, {end}) are outside the Part text")
        elif start == end and not has_title:
            problems.append(f"{prefix}: an empty segment requires a title Part")
        identity = (part_key, start, end)
        if identity in seen:
            problems.append(f"{prefix}: the segment is duplicated")
        seen.add(identity)

        vectors = segment.get("vectors", {})
        for space in requested - set(vectors):
            problems.append(f"{prefix}/vectors: missing vector for {space!r}")
        for space in set(vectors) - requested:
            problems.append(f"{prefix}/vectors/{space}: vector space was not requested")
        for space, vector in vectors.items():
            if space not in requested:
                continue
            declaration = spaces.get(space)
            if declaration is None:
                problems.append(f"{prefix}/vectors/{space}: vector space is not declared")
                continue
            if len(vector) != declaration.dimensions:
                problems.append(f"{prefix}/vectors/{space}: expected {declaration.dimensions} dimensions")
            elif any(not _finite_float32(value) for value in vector):
                problems.append(f"{prefix}/vectors/{space}: vector values must be finite 32-bit floats")
            elif declaration.metric == "cosine" and all(value == 0 for value in vector):
                problems.append(f"{prefix}/vectors/{space}: cosine vectors cannot be all zero")

        lexical = segment.get("lexical_text")
        if lexical is not None:
            if "\x00" in lexical:
                problems.append(f"{prefix}/lexical_text: lexical text contains NUL")
            elif len(lexical) > MAX_LEXICAL_TEXT_CODEPOINTS:
                problems.append(f"{prefix}/lexical_text: lexical text exceeds {MAX_LEXICAL_TEXT_CODEPOINTS} code points")
        provenance = segment.get("provenance")
        if provenance is not None:
            try:
                provenance_size = len(storage_encoded(provenance))
            except (TypeError, ValueError):
                problems.append(f"{prefix}/provenance: provenance is not JSON-encodable")
            else:
                if provenance_size > MAX_PROVENANCE_BYTES:
                    problems.append(f"{prefix}/provenance: provenance exceeds {MAX_PROVENANCE_BYTES} bytes")
                if _contains_nul(provenance):
                    problems.append(f"{prefix}/provenance: provenance contains NUL")
    return problems


def _query_output_problems(manifest: LoadedManifest, request: EmbedQueryRequest,
                           document: dict[str, Any]) -> list[str]:
    problems = protocol_errors("ingestion-embed-query-response.schema.json", document)
    if problems:
        return problems
    try:
        if len(_encoded(document)) > MAX_EMBED_QUERY_RESPONSE_BYTES:
            problems.append("the response exceeds the embed_query response limit")
    except (TypeError, ValueError):
        problems.append("the response is not JSON-encodable")
        return problems
    ingestion = manifest.model.contributions.ingestion
    declaration = ingestion.spaces.get(request.space)
    vector = document.get("vector", [])
    if declaration is not None:
        if len(vector) != declaration.dimensions:
            problems.append(f"/vector: expected {declaration.dimensions} dimensions")
        elif any(not _finite_float32(value) for value in vector):
            problems.append("/vector: vector values must be finite 32-bit floats")
        elif declaration.metric == "cosine" and all(value == 0 for value in vector):
            problems.append("/vector: cosine vectors cannot be all zero")
    return problems


def _failure(status: int, code: str, message: str) -> tuple[int, dict[str, Any]]:
    return status, {"code": code, "message": message[:1024] or code, "retryable": False}


def _run_handler(handler: Callable[[Any], Any], request: Any, operation: str) -> Any:
    try:
        return handler(request)
    except PluginError:
        raise
    except Exception:
        log.exception("%s handler raised an unexpected exception", operation)
        raise


def invoke_ingestion(manifest: LoadedManifest, handlers: dict[str, Callable[..., Any]],
                     path: str, body: bytes) -> tuple[int, dict[str, Any]]:
    """Validate, dispatch and self-check one ingestion invocation."""
    contribution = manifest.model.contributions.ingestion
    if contribution is None:
        return _failure(501, "not_implemented", "the plugin declares no ingestion Contribution")
    if len(body) > MAX_INGESTION_REQUEST_BYTES:
        return _failure(413, "request_too_large", "ingestion request exceeds 16 MiB")
    try:
        document = json.loads(body)
    except (UnicodeDecodeError, ValueError):
        return _failure(400, "invalid_request", "request body is not JSON")

    segment = path == SEGMENT_AND_EMBED_PATH
    schema = "ingestion-segment-and-embed-request.schema.json" if segment else "ingestion-embed-query-request.schema.json"
    problems = protocol_errors(schema, document)
    if problems:
        return _failure(400, "invalid_request", "; ".join(problems))
    try:
        request = (SegmentAndEmbedRequest if segment else EmbedQueryRequest).from_dict(document)
    except (TypeError, ValueError) as error:
        return _failure(400, "invalid_request", str(error))
    try:
        manifest.validate_configuration(request.configuration)
    except ConfigurationError as error:
        return _failure(400, error.code, "; ".join(error.problems))

    requested_spaces = request.spaces if segment else [request.space]
    for space in requested_spaces:
        if space not in contribution.spaces:
            return _failure(400, "unknown_space", f"the ingestion space {space!r} is not declared")
    if not segment and request.query.modality not in (contribution.spaces[request.space].query_modalities or []):
        return _failure(400, "unsupported_modality", f"space {request.space!r} does not accept {request.query.modality!r} queries")
    operation = "segment_and_embed" if segment else "embed_query"
    handler = handlers.get(operation)
    if handler is None:
        return _failure(501, "not_implemented", f"the plugin registered no {operation} handler")

    request_id = request.invocation_id
    idempotency_key = request.idempotency_key if segment else ""
    with invocation_context(request_id, idempotency_key):
        try:
            result = _run_handler(handler, request, operation)
        except PluginError as error:
            return error.status, error.envelope().to_dict()
        except Exception:
            return _failure(500, "internal_error", f"the {operation} handler raised an unexpected exception; see the plugin logs")
    model = SegmentAndEmbedResponse if segment else EmbedQueryResponse
    try:
        response = _response_document(result, model)
        problems = _output_problems(manifest, request, response) if segment else _query_output_problems(manifest, request, response)
    except (TypeError, ValueError, KeyError) as error:
        problems = [f"the {operation} response is invalid: {error}"]
    if problems:
        log.error("%s response violates the protocol: %s", operation, "; ".join(problems))
        return _failure(500, "invalid_response", "; ".join(problems))
    return 200, response
