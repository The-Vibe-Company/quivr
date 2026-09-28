"""Load openapi.yaml with the shared Manifest schema inlined; not a source of truth.

openapi.yaml aliases the Manifest, Part, Extensions, Relation, SourceIdentity and
Provenance components to contracts/shared/v0/manifest.schema.json, the single
source shared with the Plugin Protocol. Generators and validators that cannot
follow cross-file references use this mechanical projection instead: each alias
is replaced by the shared definition under the same component name, so generated
code is identical to what an inline definition would produce.
"""
import json
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
SHARED_REF = "../../shared/v0/manifest.schema.json"
SHARED = (HERE / SHARED_REF).resolve()


def _local(value):
    if isinstance(value, dict):
        return {k: (v.replace("#/$defs/", "#/components/schemas/") if k == "$ref" else _local(v))
                for k, v in value.items()}
    if isinstance(value, list):
        return [_local(v) for v in value]
    return value


def load():
    spec = yaml.safe_load((HERE / "openapi.yaml").read_text())
    shared = json.loads(SHARED.read_text())["$defs"]
    schemas = spec["components"]["schemas"]
    for name, schema in schemas.items():
        ref = schema.get("$ref", "") if isinstance(schema, dict) else ""
        if ref.startswith(SHARED_REF + "#/$defs/"):
            target = ref.split("#/$defs/", 1)[1]
            if len(schema) != 1 or target != name:
                raise ValueError(f"component {name} must be exactly an alias of $defs/{name}")
            schemas[name] = _local(shared[target])
    remaining = json.dumps(spec)
    if SHARED_REF in remaining:
        raise ValueError("openapi.yaml references the shared schema outside a component alias")
    return spec
