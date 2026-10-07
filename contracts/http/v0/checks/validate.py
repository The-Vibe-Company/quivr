"""Validate the authoritative schema, examples, and important invalid states."""
import json
import sys
from pathlib import Path

from jsonschema import Draft202012Validator
from openapi_spec_validator import validate

root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(root))
from bundle import load  # noqa: E402  shared Manifest components inlined

spec = load()
validate(spec)
cases = json.loads((root / "examples.json").read_text())
examples = {case["name"]: case["value"] for case in cases}

# The original contract fixture set (THE-543/THE-547/THE-640). Later slices may
# add examples, never drop one of these: THE-662 keeps them as a floor.
ORIGINAL_EXAMPLES = [
    "structured_inline", "structured_manifest", "blob_input", "pending_receipt",
    "resolved_receipt", "mixed_batch_input", "mixed_batch_result", "verified_upload",
    "version_relations", "administrative_operation", "saved_query_create", "saved_query",
    "subscription_create", "subscription", "positive_match", "reference_webhook",
    "delivery", "attempt_page", "monitoring_change", "text_search",
    "canonical_search_results", "lexical_search_without_embedding", "rebuild_queued",
    "rebuild_succeeded",
]
ORIGINAL_BOUNDARIES = 31
missing = [name for name in ORIGINAL_EXAMPLES if name not in examples]
assert not missing, f"original contract examples removed: {missing}"
assert len(examples) == len(cases), "duplicate example names"


def check(schema, value, valid=True):
    validator = Draft202012Validator({
        "$ref": "#/components/schemas/" + schema,
        "components": spec["components"],
    })
    errors = list(validator.iter_errors(value))
    assert (not errors) == valid, (schema, value, errors)


for case in cases:
    check(case["schema"], case["value"])

# Successful normalization may omit optional audit detail. The deterministic
# digest remains mandatory, and a retained invocation ID must be non-empty.
normalization = {
    "plugin_id": "example.markdown", "plugin_version": "1.0.0",
    "plugin_api": "0.1.0", "contribution": "normalizer",
    "idempotency_key": "nk_example", "input_sha256": "a" * 64,
}
check("NormalizationProvenance", normalization)
check("NormalizationProvenance", {**normalization, "invocation_id": "inv_1"})
check("NormalizationProvenance", {**normalization, "invocation_id": ""}, False)
check("NormalizationProvenance", {k: v for k, v in normalization.items() if k != "idempotency_key"}, False)
fallback = {**normalization, "fallback": {"code": "plugin_unavailable", "message": "The normalizer is unavailable."}}
check("NormalizationProvenance", {**fallback, "invocation_id": "inv_failed"})
check("NormalizationProvenance", fallback, False)


pending = examples["pending_receipt"]
resolved = examples["resolved_receipt"]
unavailable = examples["version_relations"]["relations"][1]
invalid = [
    ("Receipt", {**pending, "outcome": "created"}),
    ("Receipt", {**pending, "state": "resolved"}),
    ("Receipt", {**resolved, "outcome": "failed"}),
    ("Receipt", {k: v for k, v in pending.items() if k != "source"}),
    ("Receipt", {k: v for k, v in pending.items() if k != "processing"}),
    ("BatchRequest", {"items": []}),
    ("BatchRequest", {"items": [None] * 101}),
    ("BatchItem", {"index": 0}),
    ("BatchItem", {"index": 0, "receipt": pending,
                   "error": {"code": "x", "message": "x", "retryable": False}}),
    ("Upload", {"upload_id": "u", "state": "verified"}),
    ("Upload", {"upload_id": "u", "state": "verifying", "blob_id": "b"}),
    ("ResolvedRelation", {**unavailable, "target_record_id": "must-not-leak"}),
    ("ResolvedRelation", {**unavailable, "status": "available"}),
    ("IngestCommand", {**examples["structured_inline"], "unexpected": True}),
    ("IngestCommand", {**examples["structured_inline"],
                       "content": {"kind": "text", "blob_id": "b"}}),
    ("WebhookEvent", {**examples["reference_webhook"], "content": "must not be embedded"}),
    ("WebhookEvent", {**examples["reference_webhook"], "type": "delivery.updated"}),
    ("SubscriptionCreate", {**examples["subscription_create"], "signing_secret": "not accepted"}),
    ("SubscriptionCreate", {**examples["owned_subscription_create"], "owner": ""}),
    ("SubscriptionCreate", {**examples["owned_subscription_create"], "owner": "u" * 129}),
    ("Match", {k: v for k, v in examples["positive_match"].items() if k != "evidence"}),
    ("Delivery", {**examples["delivery"], "state": "failed"}),
]
search = examples["text_search"]
hit = examples["canonical_search_results"]["items"][0]
rebuild = examples["rebuild_succeeded"]
invalid.extend([
    ("SearchRequest", {**search, "corpus_ids": []}),
    ("SearchRequest", {**search, "corpus_ids": ["c", "c"]}),
    ("SearchRequest", {**search, "limit": 51}),
    ("SearchRequest", {**search, "engine_filter": {}}),
    ("SearchHit", {**hit, "score": 0.99}),
    ("SearchHit", {k: v for k, v in hit.items() if k != "vector_space_id"}),
    ("SearchHit", {k: v for k, v in hit.items() if k != "embedding_artifact_id"}),
    ("Operation", {k: v for k, v in rebuild.items() if k != "result"}),
    ("Operation", {k: v for k, v in rebuild.items() if k != "corpus_id"}),
])
for schema, value in invalid:
    check(schema, value, False)

# A malformed command is permitted through the envelope, then rejected per entry.
check("BatchRequest", {"items": [examples["structured_inline"], 42]})
check("IngestCommand", 42, False)
boundaries = len(invalid) + 2
assert boundaries >= ORIGINAL_BOUNDARIES, f"{boundaries} boundary checks, fewer than the original {ORIGINAL_BOUNDARIES}"
print(f"OpenAPI valid; {len(cases)} examples and {len(invalid) + 2} boundary checks passed")
