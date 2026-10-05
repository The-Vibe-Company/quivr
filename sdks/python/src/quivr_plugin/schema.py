"""JSON Schema validation against the bundled Plugin Protocol v0 schemas."""
from __future__ import annotations

import json
from functools import cache, lru_cache
from importlib import resources
from typing import Any

from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

_BASE = "https://quivr.invalid/contracts/"


@cache
def _registry() -> Registry:
    root = resources.files("quivr_plugin") / "schemas"
    items = []
    for folder in ("shared/v0", "plugins/v0"):
        directory = root.joinpath(*folder.split("/"))
        for entry in sorted(directory.iterdir(), key=lambda e: e.name):
            if entry.name.endswith(".json"):
                contents = json.loads(entry.read_text(encoding="utf-8"))
                items.append((f"{_BASE}{folder}/{entry.name}", Resource.from_contents(contents, default_specification=DRAFT202012)))
    return Registry().with_resources(items)


@cache
def _validator(schema_file: str) -> Draft202012Validator:
    return Draft202012Validator({"$ref": f"{_BASE}plugins/v0/{schema_file}"}, registry=_registry())


def _format(errors) -> list[str]:
    out = []
    for error in sorted(errors, key=lambda e: (list(map(str, e.absolute_path)), e.message)):
        path = "/" + "/".join(str(p) for p in error.absolute_path)
        out.append(f"{path}: {error.message}")
    return out


def protocol_errors(schema_file: str, value: Any) -> list[str]:
    """Return "<JSON pointer>: <message>" problems of value against a protocol schema file,
    such as "normalizer-request.schema.json"."""
    return _format(_validator(schema_file).iter_errors(value))


@lru_cache(maxsize=128)
def _checked_schema(document: str) -> Any:
    # Copy the schema from its content key: callers may mutate their input,
    # while each invocation constructs its own validator from checked rules.
    schema = json.loads(document)
    Draft202012Validator.check_schema(schema)
    return schema


def schema_errors(schema: Any, value: Any) -> list[str]:
    """Validate value against a plugin-declared JSON Schema 2020-12 document."""
    document = json.dumps(schema, sort_keys=True, separators=(",", ":"))
    return _format(Draft202012Validator(_checked_schema(document)).iter_errors(value))
