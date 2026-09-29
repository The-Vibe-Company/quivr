"""Fixture helpers for plugin unit tests.

An invocation fixture (contracts/plugins/v0/plugin-fixture.schema.json) names a
local input file and optional configuration. ``build_request`` turns it into the
same normalizer request ``quivr plugin dev --fixture`` sends: an absolute
file:// reference, the computed size and SHA-256, and deterministic development
identifiers.

A subscription fixture (contracts/plugins/v0/subscription-fixture.schema.json)
holds record Parts and evaluations with optional expected decisions.
``build_subscription_requests`` turns it into the same batches the Contract
Runner and ``quivr plugin dev`` send.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

from .manifest import DEFAULT_MAX_BATCH_SIZE
from .models import (
    EvaluatedRecord,
    Evaluation,
    FileReference,
    InputBlob,
    InvocationFixture,
    NormalizerRequest,
    NormalizerResponse,
    RecordProvenance,
    RecordSource,
    SourceIdentity,
    SubscriptionFixture,
    SubscriptionRef,
    SubscriptionRequest,
    SubscriptionResponse,
)
from .schema import protocol_errors
from .server import Plugin, Reply

# Acceptance time of a subscription fixture that does not set one (as quivr plugin dev).
DEV_ACCEPTED_AT = "2026-01-01T00:00:00Z"


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


def load_subscription_fixture(path: str | Path) -> SubscriptionFixture:
    """Read and schema-check a subscription fixture file."""
    document = json.loads(Path(path).read_text(encoding="utf-8"))
    problems = protocol_errors("subscription-fixture.schema.json", document)
    if problems:
        raise ValueError(f"{path} is not a valid subscription fixture: " + "; ".join(problems))
    return SubscriptionFixture.from_dict(document)


def build_subscription_requests(fixture_path: str | Path, *, max_batch_size: int = DEFAULT_MAX_BATCH_SIZE) -> list[SubscriptionRequest]:
    """Build the development subscription requests for a fixture file, split into batches.

    Evaluations are numbered e1, e2, ... and evaluation n stands for Subscription
    dev-subscription-n; the Record Version ids and idempotency keys derive from the
    SHA-256 of the fixture bytes, exactly like quivr plugin dev and the Contract Runner.
    Record metadata the fixture omits defaults to source dev-namespace/dev-record-<digest>,
    origin client and DEV_ACCEPTED_AT.
    """
    raw = Path(fixture_path).read_bytes()
    fixture = load_subscription_fixture(fixture_path)
    digest = hashlib.sha256(raw).hexdigest()
    short = digest[:16]
    # Metadata the fixture omits takes the same development defaults as quivr plugin dev.
    record = EvaluatedRecord(corpus_id="dev-corpus", record_id=f"dev-record-{short}", record_version_id=f"dev-version-{short}",
                             enriched=bool(fixture.record.enriched), parts=fixture.record.parts,
                             source=fixture.record.source or RecordSource(namespace="dev-namespace", record_key=f"dev-record-{short}"),
                             accepted_at=fixture.record.accepted_at or DEV_ACCEPTED_AT,
                             provenance=fixture.record.provenance or RecordProvenance(origin="client"),
                             extensions=fixture.record.extensions)
    evaluations = [
        Evaluation(
            id=f"e{n}",
            expression=item.expression,
            configuration=item.configuration if item.configuration is not None else {},
            subscriptions=[SubscriptionRef(subscription_id=f"dev-subscription-{n}", subscription_version_id=f"dev-subscription-version-{n}",
                                           saved_query_id=f"dev-saved-query-{n}", saved_query_version_id=f"dev-saved-query-version-{n}")],
        )
        for n, item in enumerate(fixture.evaluations, start=1)
    ]
    batches = []
    for index, start in enumerate(range(0, len(evaluations), max_batch_size), start=1):
        batches.append(SubscriptionRequest(
            invocation_id=f"dev-invocation-{short}-{index}",
            idempotency_key=f"dev:{digest}:{index}",
            organization_id="dev-organization",
            record=record,
            evaluations=evaluations[start:start + max_batch_size],
            configuration=fixture.configuration if fixture.configuration is not None else {},
        ))
    return batches


def expected_decisions(fixture_path: str | Path) -> dict[str, str]:
    """Map evaluation ids (e1, e2, ...) to the decisions a subscription fixture expects."""
    fixture = load_subscription_fixture(fixture_path)
    return {f"e{n}": item.expect for n, item in enumerate(fixture.evaluations, start=1) if item.expect}


def invoke_subscription_fixture(plugin: Plugin, fixture_path: str | Path) -> SubscriptionResponse:
    """Run a subscription fixture through the plugin's subscription route in process, batch by batch.

    Returns the decisions of every batch; raises AssertionError on an error reply or when a
    decision differs from the fixture's expected decision.
    """
    decisions = []
    for request in build_subscription_requests(fixture_path, max_batch_size=plugin.manifest.max_batch_size):
        decisions.extend(expect_subscription_response(plugin.evaluate(request)).decisions)
    expected = expected_decisions(fixture_path)
    for decision in decisions:
        want = expected.get(decision.id)
        if want and want != decision.decision:
            raise AssertionError(f"evaluation {decision.id} answered {decision.decision}; the fixture expects {want}")
    return SubscriptionResponse(decisions=decisions)


def expect_subscription_response(reply: Reply) -> SubscriptionResponse:
    """Return the decoded response of a successful subscription reply, or raise AssertionError with the error envelope."""
    if reply.status != 200:
        raise AssertionError(f"subscription returned HTTP {reply.status}: {reply.body}")
    return SubscriptionResponse.from_dict(reply.body)
