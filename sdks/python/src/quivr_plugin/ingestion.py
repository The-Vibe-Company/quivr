"""Ingestion Contribution registration, dispatch and output checks."""
from __future__ import annotations

import json
import logging
import math
import struct
from collections.abc import Callable
from typing import Any

from .api_versions import FEATURE_SINCE
from ._json import storage_encoded
from .errors import ConfigurationError, PluginError
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
MAX_SOURCE_RANGES = 256
MAX_SOURCE_RANGE_CODEPOINTS = 4096
MAX_PACKED_TEXT_CODEPOINTS = 16384
MAX_SOURCE_SEPARATOR_CODEPOINTS = 16
MULTI_PART_SEGMENTS_API = FEATURE_SINCE.get("multi_part_segments", "0.17.0")

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


def _version_tuple(value: str) -> tuple[int, int, int]:
    try:
        major, minor, patch = (int(item) for item in value.split("."))
    except (TypeError, ValueError):
        return (0, 0, 0)
    return major, minor, patch


def _supports_multi_part_segments(manifest: LoadedManifest) -> bool:
    return _version_tuple(manifest.plugin_api) >= _version_tuple(MULTI_PART_SEGMENTS_API)


def _source_ranges_problems(manifest: LoadedManifest, segment: dict[str, Any], prefix: str,
                            part_order: dict[str, int], lengths: dict[str, int]) -> list[str]:
    problems: list[str] = []
    ranges = segment.get("source_ranges")
    separator = segment.get("source_separator")
    if "source_ranges" in segment or "source_separator" in segment:
        if not _supports_multi_part_segments(manifest):
            problems.append(f"{prefix}: source ranges and separators require Plugin API {MULTI_PART_SEGMENTS_API}")
    if separator is not None:
        if "\x00" in separator:
            problems.append(f"{prefix}/source_separator: source separator contains NUL")
        elif len(separator) > MAX_SOURCE_SEPARATOR_CODEPOINTS:
            problems.append(f"{prefix}/source_separator: source separator exceeds {MAX_SOURCE_SEPARATOR_CODEPOINTS} code points")
    if not ranges:
        return problems
    if len(ranges) > MAX_SOURCE_RANGES:
        problems.append(f"{prefix}/source_ranges: source ranges exceed {MAX_SOURCE_RANGES} items")
    first = ranges[0]
    if (first["part_key"], first["start"], first["end"]) != (segment["part_key"], segment["start"], segment["end"]):
        problems.append(f"{prefix}/source_ranges/0: first source range must equal the legacy anchor")
    seen: dict[tuple[str, int, int], int] = {}
    last_part = -1
    last_end: dict[str, int] = {}
    packed_codepoints = 0
    for index, source_range in enumerate(ranges):
        range_prefix = f"{prefix}/source_ranges/{index}"
        key = source_range["part_key"]
        start = source_range["start"]
        end = source_range["end"]
        if key not in lengths:
            problems.append(f"{range_prefix}/part_key: the Part key is not in the request")
            continue
        if start >= end:
            problems.append(f"{range_prefix}: source ranges must be non-empty")
        elif start < 0 or end > lengths[key]:
            problems.append(f"{range_prefix}: offsets [{start}, {end}) are outside the Part text")
        elif end - start > MAX_SOURCE_RANGE_CODEPOINTS:
            problems.append(f"{range_prefix}: source range exceeds {MAX_SOURCE_RANGE_CODEPOINTS} code points")
        else:
            if packed_codepoints:
                packed_codepoints += len(separator or "")
            packed_codepoints += end - start
        identity = (key, start, end)
        if identity in seen:
            problems.append(f"{range_prefix}: source range is duplicated")
            continue
        else:
            seen[identity] = index
        order = part_order[key]
        if order < last_part or (order == last_part and start < last_end.get(key, 0)):
            problems.append(f"{range_prefix}: source ranges are out of reading order or overlap")
        if order > last_part:
            last_part = order
        last_end[key] = max(last_end.get(key, 0), end)
    if packed_codepoints > MAX_PACKED_TEXT_CODEPOINTS:
        problems.append(f"{prefix}/source_ranges: joined source ranges exceed {MAX_PACKED_TEXT_CODEPOINTS} code points")
    return problems


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
    part_order = {part.key: index for index, part in enumerate(request.parts)}
    has_title = any(part.role == "title" for part in request.parts)
    spaces = manifest.model.contributions.ingestion.spaces
    requested = set(request.spaces)
    seen: set[tuple[Any, ...]] = set()
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
        problems.extend(_source_ranges_problems(manifest, segment, prefix, part_order, lengths))
        source_ranges = segment.get("source_ranges") or []
        identity = tuple((item["part_key"], item["start"], item["end"]) for item in source_ranges) if source_ranges else (part_key, start, end)
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
    problems.extend(_page_problems(manifest, request, document))
    return problems


def _page_problems(manifest: LoadedManifest, request: SegmentAndEmbedRequest,
                   document: dict[str, Any]) -> list[str]:
    next_start = document.get("next_start")
    if request.page is None:
        return ["next_start requires a paged request"] if next_start is not None else []
    if _version_tuple(manifest.plugin_api) < (0, 18, 0):
        return ["ingestion pages require Plugin API 0.18"]
    if len(request.parts) != 1 or len(document["segments"]) > request.page.max_segments:
        return ["a page must respect its work bound and have one Part"]
    part = request.parts[0]
    end = request.page.start
    for segment in document["segments"]:
        ranges = segment.get("source_ranges") or [segment]
        for source in ranges:
            if source["part_key"] != part.key or source["start"] != end or source["end"] <= end:
                return ["paged source ranges must cover every code point once"]
            end = source["end"]
    if next_start is not None:
        if next_start != end or end <= request.page.start or end >= len(part.text):
            return ["next_start must advance to the first uncovered code point"]
    elif end != len(part.text):
        return ["a final page must cover the entire remaining source"]
    return []


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
    if segment and request.page is not None and (not contribution.paging or _version_tuple(manifest.plugin_api) < (0, 18, 0)):
        return _failure(400, "invalid_request", "ingestion pages require declared paging and Plugin API 0.18")
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
