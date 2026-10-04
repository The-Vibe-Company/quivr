"""Check the Plugin Protocol v0 schemas and normative fixtures with an independent validator.

Go tests run the full fixture semantics through the engine; this check proves the
schemas are standard JSON Schema 2020-12 that non-Go tooling can consume, and
that every fixture's schema-only outcome (schema_valid) holds.
"""
import json
from pathlib import Path

import yaml
from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

plugins = Path(__file__).resolve().parents[1]
contracts = plugins.parents[1]
base = "https://quivr.invalid/contracts/"

resources = []
for path in [contracts / "shared/v0/manifest.schema.json", *sorted(plugins.glob("*.schema.json"))]:
    doc = json.loads(path.read_text())
    Draft202012Validator.check_schema(doc)
    uri = base + path.relative_to(contracts).as_posix()
    resources.append((uri, Resource.from_contents(doc, default_specification=DRAFT202012)))
registry = Registry().with_resources(resources)

index = json.loads((plugins / "fixtures/index.json").read_text())
for case in index["cases"]:
    path = plugins / "fixtures" / case["file"]
    value = yaml.safe_load(path.read_text()) if path.suffix == ".yaml" else json.loads(path.read_text())
    validator = Draft202012Validator({"$ref": base + "plugins/v0/" + case["schema"]}, registry=registry)
    errors = list(validator.iter_errors(value))
    assert (not errors) == case["schema_valid"], (case["file"], [e.message for e in errors])
    assert case["schema_valid"] or not case["valid"], case["file"]

print(f"Plugin Protocol v0 schemas valid; {len(index['cases'])} fixtures matched their schema outcome")
