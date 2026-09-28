"""Fixture helpers for plugin unit tests.

An invocation fixture (contracts/plugins/v0/plugin-fixture.schema.json) names a
local input file and optional configuration. ``build_request`` turns it into the
same normalizer request ``quivr plugin dev --fixture`` sends: an absolute
file:// reference, the computed size and SHA-256, and deterministic development
identifiers.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

from .models import FileReference, InputBlob, InvocationFixture, NormalizerRequest, NormalizerResponse, SourceIdentity
from .schema import protocol_errors
from .server import Plugin, Reply


def load_fixture(path: str | Path) -> InvocationFixture:
    """Read and schema-check an invocation fixture file."""
    document = json.loads(Path(path).read_text(encoding="utf-8"))
    problems = protocol_errors("plugin-fixture.schema.json", document)
    if problems:
        raise ValueError(f"{path} is not a valid invocation fixture: " + "; ".join(problems))
    return InvocationFixture.from_dict(document)


def build_request(fixture_path: str | Path, *, configuration: dict[str, Any] | None = None) -> NormalizerRequest:
    """Build the development normalizer request for a fixture file.

    ``configuration`` overrides the fixture's configuration.
    """
    fixture_path = Path(fixture_path).absolute()
    fixture = load_fixture(fixture_path)
    # Relative to the fixture file (an absolute path is used as is), symlinks resolved.
    input_path = (fixture_path.parent / fixture.input.path).resolve()
    data = input_path.read_bytes()
    digest = hashlib.sha256(data).hexdigest()
    short = digest[:16]
    source = fixture.source or SourceIdentity(corpus_id="dev-corpus", namespace="dev", record_key=fixture.input.path)
    return NormalizerRequest(
        invocation_id=f"dev-invocation-{short}",
        idempotency_key=f"dev:{digest}",
        organization_id="dev-organization",
        corpus_id=source.corpus_id,
        record_id=f"dev-record-{short}",
        record_version_id=f"dev-version-{short}",
        source=source,
        input=InputBlob(
            blob_id=f"dev-blob-{short}",
            media_type=fixture.input.media_type,
            size_bytes=len(data),
            sha256=digest,
            reference=FileReference(url=input_path.as_uri()),
        ),
        extensions=fixture.extensions,
        provenance=fixture.provenance,
        configuration=configuration if configuration is not None else (fixture.configuration or {}),
    )


def invoke_fixture(plugin: Plugin, fixture_path: str | Path, **overrides: Any) -> Reply:
    """Run a fixture through the plugin's normalizer route in process."""
    return plugin.invoke(build_request(fixture_path, **overrides))


def expect_response(reply: Reply) -> NormalizerResponse:
    """Return the decoded response of a successful reply, or raise AssertionError with the error envelope."""
    if reply.status != 200:
        raise AssertionError(f"normalizer returned HTTP {reply.status}: {reply.body}")
    return NormalizerResponse.from_dict(reply.body)
