"""Validate the authoritative schema, examples, and important invalid states."""
import json
from pathlib import Path

from jsonschema import Draft202012Validator
from openapi_spec_validator import validate
import yaml

root = Path(__file__).resolve().parents[1]
spec = yaml.safe_load((root / "openapi.yaml").read_text())
validate(spec)
cases = json.loads((root / "examples.json").read_text())
examples = {case["name"]: case["value"] for case in cases}


def check(schema, value, valid=True):
    validator = Draft202012Validator({
        "$ref": "#/components/schemas/" + schema,
        "components": spec["components"],
    })
    errors = list(validator.iter_errors(value))
    assert (not errors) == valid, (schema, value, errors)


for case in cases:
    check(case["schema"], case["value"])

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
    ("Match", {k: v for k, v in examples["positive_match"].items() if k != "evidence"}),
    ("Delivery", {**examples["delivery"], "state": "failed"}),
]
for schema, value in invalid:
    check(schema, value, False)

# A malformed command is permitted through the envelope, then rejected per entry.
check("BatchRequest", {"items": [examples["structured_inline"], 42]})
check("IngestCommand", 42, False)
print(f"OpenAPI valid; {len(cases)} examples and {len(invalid) + 2} boundary checks passed")
