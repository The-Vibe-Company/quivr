"""Generate the SDK's typed models and schema copies from the Plugin Protocol contracts.

Sources: contracts/shared/v0/manifest.schema.json and contracts/plugins/v0/*.schema.json.
Outputs:
  src/quivr_plugin/models.py     dataclasses with from_dict/to_dict
  src/quivr_plugin/schemas/...   byte-identical copies used for runtime validation

Usage:
  python sdks/python/scripts/generate.py          # rewrite the outputs
  python sdks/python/scripts/generate.py --check  # fail when the outputs are stale

Standard library only, so regeneration is reproducible without a toolchain.
"""
from __future__ import annotations

import json
import keyword
import sys
from pathlib import Path

SDK = Path(__file__).resolve().parents[1]
REPO = SDK.parents[1]
CONTRACTS = REPO / "contracts"
PACKAGE = SDK / "src" / "quivr_plugin"
SHARED = "shared/v0/manifest.schema.json"

# Top-level class name of each Plugin Protocol schema file, in output order.
FILES = {
    "normalizer-request.schema.json": "NormalizerRequest",
    "normalizer-response.schema.json": "NormalizerResponse",
    "discovery.schema.json": "Discovery",
    "health.schema.json": "Health",
    "error.schema.json": "ErrorEnvelope",
    "plugin-manifest.schema.json": "PluginManifest",
    "plugin-fixture.schema.json": "InvocationFixture",
    "subscription-request.schema.json": "SubscriptionRequest",
    "subscription-response.schema.json": "SubscriptionResponse",
    "subscription-fixture.schema.json": "SubscriptionFixture",
    "connector-fetch-request.schema.json": "ConnectorFetchRequest",
    "connector-fetch-response.schema.json": "ConnectorFetchResponse",
    "connector-check-credential-request.schema.json": "ConnectorCredentialRequest",
    "connector-check-credential-response.schema.json": "ConnectorCredentialResponse",
    "connector-fixture.schema.json": "ConnectorFixture",
    "connector-describe-attachment-request.schema.json": "ConnectorDescribeAttachmentRequest",
    "connector-describe-attachment-response.schema.json": "ConnectorDescribeAttachmentResponse",
    "connector-upload-attachment-request.schema.json": "ConnectorUploadAttachmentRequest",
    "connector-upload-attachment-response.schema.json": "ConnectorUploadAttachmentResponse",
    "connector-receive-request.schema.json": "ConnectorReceiveRequest",
    "connector-receive-response.schema.json": "ConnectorReceiveResponse",
    "ingestion-segment-and-embed-request.schema.json": "SegmentAndEmbedRequest",
    "ingestion-segment-and-embed-response.schema.json": "SegmentAndEmbedResponse",
    "ingestion-embed-query-request.schema.json": "EmbedQueryRequest",
    "ingestion-embed-query-response.schema.json": "EmbedQueryResponse",
    "ingestion-fixture.schema.json": "IngestionFixture",
}

# Readable names for inline object schemas, keyed by "<file>#<JSON pointer>".
# Unlisted inline objects are named <Parent><Property>.
NAMES = {
    SHARED + "#/$defs/Extensions/additionalProperties": "ExtensionEntry",
    "plugins/v0/normalizer-request.schema.json#/properties/input": "InputBlob",
    "plugins/v0/normalizer-request.schema.json#/properties/input/properties/reference/oneOf/0": "SignedUrlReference",
    "plugins/v0/normalizer-request.schema.json#/properties/input/properties/reference/oneOf/1": "FileReference",
    "plugins/v0/normalizer-response.schema.json#/properties/warnings/items": "ResponseWarning",
    "plugins/v0/plugin-manifest.schema.json#/properties/compatibility": "ManifestCompatibility",
    "plugins/v0/plugin-manifest.schema.json#/properties/contributions": "ManifestContributions",
    "plugins/v0/plugin-manifest.schema.json#/properties/configuration": "ManifestConfiguration",
    "plugins/v0/discovery.schema.json#/properties/plugin": "PluginIdentity",
    "plugins/v0/plugin-manifest.schema.json#/$defs/Normalizer": "NormalizerContribution",
    "plugins/v0/plugin-manifest.schema.json#/properties/secrets/items": "Secret",
    "plugins/v0/plugin-manifest.schema.json#/properties/run": "RunCommand",
    "plugins/v0/plugin-manifest.schema.json#/$defs/Normalizer/properties/retry": "RetryIntent",
    "plugins/v0/plugin-manifest.schema.json#/$defs/Normalizer/properties/limits": "OutputLimits",
    "plugins/v0/plugin-fixture.schema.json#/properties/input": "FixtureInput",
    "plugins/v0/plugin-manifest.schema.json#/$defs/Subscription": "SubscriptionContribution",
    "plugins/v0/plugin-manifest.schema.json#/$defs/Subscription/properties/limits": "SubscriptionLimits",
    "plugins/v0/subscription-request.schema.json#/properties/record": "EvaluatedRecord",
    "plugins/v0/subscription-request.schema.json#/properties/record/properties/parts/items": "RecordPart",
    "plugins/v0/subscription-request.schema.json#/properties/record/properties/source": "RecordSource",
    "plugins/v0/subscription-request.schema.json#/properties/record/properties/provenance": "RecordProvenance",
    "plugins/v0/subscription-request.schema.json#/properties/record/properties/provenance/properties/connector": "ConnectorOrigin",
    "plugins/v0/subscription-request.schema.json#/properties/record/properties/provenance/properties/normalization": "NormalizationOrigin",
    "plugins/v0/subscription-request.schema.json#/properties/evaluations/items": "Evaluation",
    "plugins/v0/subscription-request.schema.json#/properties/evaluations/items/properties/subscriptions/items": "SubscriptionRef",
    "plugins/v0/subscription-response.schema.json#/properties/decisions/items": "Decision",
    "plugins/v0/subscription-response.schema.json#/properties/decisions/items/properties/evidence": "Evidence",
    "plugins/v0/subscription-fixture.schema.json#/properties/record": "FixtureRecord",
    "plugins/v0/subscription-fixture.schema.json#/properties/evaluations/items": "FixtureEvaluation",
    "plugins/v0/plugin-manifest.schema.json#/$defs/Connector": "ConnectorContribution",
    "plugins/v0/plugin-manifest.schema.json#/$defs/Connector/properties/limits": "ConnectorLimits",
    "plugins/v0/plugin-manifest.schema.json#/$defs/ConnectorKind": "ConnectorKind",
    "plugins/v0/connector-fetch-request.schema.json#/properties/connector": "ConnectorInstanceRef",
    "plugins/v0/connector-fetch-response.schema.json#/$defs/Item": "ConnectorItem",
    "plugins/v0/connector-fetch-response.schema.json#/$defs/Attachment": "ConnectorAttachment",
    "plugins/v0/connector-fixture.schema.json#/properties/connector": "FixtureConnector",
    "plugins/v0/connector-fixture.schema.json#/properties/expect": "ConnectorExpectation",
    "plugins/v0/connector-fixture.schema.json#/properties/expect/properties/pages/items": "ExpectedPage",
    "plugins/v0/connector-fixture.schema.json#/properties/expect/properties/error": "ExpectedError",
    "plugins/v0/connector-describe-attachment-request.schema.json#/properties/item": "AttachmentItem",
    "plugins/v0/connector-upload-attachment-request.schema.json#/properties/item": "UploadAttachmentItem",
    "plugins/v0/connector-upload-attachment-request.schema.json#/properties/grant": "UploadGrant",
    "plugins/v0/connector-fetch-response.schema.json#/$defs/PushStatus": "PushStatus",
    "plugins/v0/connector-receive-request.schema.json#/properties/connector": "ReceiveInstanceRef",
    "plugins/v0/connector-receive-request.schema.json#/properties/request": "RelayedRequest",
    "plugins/v0/connector-receive-response.schema.json#/properties/response": "ReceiveAnswer",
    "plugins/v0/connector-fixture.schema.json#/properties/receive/items": "FixtureReceiveCase",
    "plugins/v0/connector-fixture.schema.json#/properties/receive/items/properties/request": "FixtureReceiveRequest",
    "plugins/v0/connector-fixture.schema.json#/properties/receive/items/properties/expect": "ExpectedDelivery",
    "plugins/v0/connector-fixture.schema.json#/properties/receive/items/properties/expect/properties/error": "ExpectedDeliveryError",
}

HEADER = '''"""Typed Plugin Protocol v0 models.

Code generated by sdks/python/scripts/generate.py from contracts/shared/v0 and
contracts/plugins/v0. DO NOT EDIT.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Literal

from ._codec import Model
'''


def camel(text: str) -> str:
    return "".join(part[:1].upper() + part[1:] for part in text.replace("-", "_").split("_") if part)


class Generator:
    def __init__(self) -> None:
        self.docs: dict[str, dict] = {}
        self.classes: list[str] = []
        self.emitted: dict[str, str] = {}  # "<file>#<pointer>" -> class name
        self.used: set[str] = set()

    def load(self, rel: str) -> dict:
        if rel not in self.docs:
            self.docs[rel] = json.loads((CONTRACTS / rel).read_text())
        return self.docs[rel]

    def resolve(self, rel: str, ref: str) -> tuple[str, str]:
        target, _, pointer = ref.partition("#")
        if target:
            rel = str(Path(rel).parent.joinpath(target)).replace("\\", "/")
            parts = []
            for piece in rel.split("/"):
                if piece == "..":
                    parts.pop()
                elif piece != ".":
                    parts.append(piece)
            rel = "/".join(parts)
        return rel, pointer

    def at(self, rel: str, pointer: str):
        node = self.load(rel)
        for token in [t for t in pointer.split("/") if t]:
            token = token.replace("~1", "/").replace("~0", "~")
            node = node[int(token)] if isinstance(node, list) else node[token]
        return node

    def type_of(self, rel: str, pointer: str, schema, name: str) -> str:
        if schema is True or not set(schema) - {"description", "title", "default", "examples"}:
            return "Any"  # any JSON value, such as an opaque connector checkpoint
        if "$ref" in schema:
            target_rel, target_pointer = self.resolve(rel, schema["$ref"])
            target = self.at(target_rel, target_pointer)
            return self.type_of(target_rel, target_pointer, target, target_pointer.rsplit("/", 1)[-1])
        if "const" in schema:
            return f"Literal[{json.dumps(schema['const'])}]"
        if "enum" in schema:
            return "Literal[" + ", ".join(json.dumps(v) for v in schema["enum"]) + "]"
        if "oneOf" in schema:
            return " | ".join(
                self.type_of(rel, f"{pointer}/oneOf/{i}", branch, f"{name}Option{i}")
                for i, branch in enumerate(schema["oneOf"])
            )
        kind = schema.get("type")
        if isinstance(kind, list):
            return "Any"
        if kind == "string":
            return "str"
        if kind == "integer":
            return "int"
        if kind == "number":
            return "float"
        if kind == "boolean":
            return "bool"
        if kind == "array":
            return f"list[{self.type_of(rel, pointer + '/items', schema.get('items', True), name + 'Item')}]"
        if kind == "object" or "properties" in schema:
            if "properties" in schema:
                return self.object_class(rel, pointer, schema, name)
            extra = schema.get("additionalProperties", True)
            if extra is True:
                return "dict[str, Any]"
            return f"dict[str, {self.type_of(rel, pointer + '/additionalProperties', extra, name + 'Value')}]"
        raise SystemExit(f"unsupported schema at {rel}#{pointer}: {schema}")

    def object_class(self, rel: str, pointer: str, schema: dict, name: str) -> str:
        key = f"{rel}#{pointer}"
        if key in self.emitted:
            return self.emitted[key]
        cls = NAMES.get(key, camel(name))
        if cls in self.used:
            raise SystemExit(f"class name {cls} is used twice; add an entry to NAMES for {key}")
        if schema.get("additionalProperties") is not False:
            raise SystemExit(f"{key}: object models must set additionalProperties: false")
        self.used.add(cls)
        self.emitted[key] = cls
        required = set(schema.get("required", []))
        lines = ["", "", "@dataclass(kw_only=True)", f"class {cls}(Model):"]
        description = schema.get("description") or schema.get("title")
        if description:
            lines.append(f"    {json.dumps(description)}")
            lines.append("")
        for prop, sub in schema["properties"].items():
            if sub is False:
                continue  # a name the schema forbids, such as a reserved Contribution
            if not prop.isidentifier() or keyword.iskeyword(prop):
                raise SystemExit(f"{key}: property {prop!r} is not a Python identifier")
            annotation = self.type_of(rel, f"{pointer}/properties/{prop}", sub, cls + camel(prop))
            literal = sub["const"] if "const" in sub else (sub["enum"][0] if len(sub.get("enum", [])) == 1 else None)
            if prop in required and literal is not None:
                lines.append(f"    {prop}: {annotation} = {json.dumps(literal)}")
            elif prop in required:
                lines.append(f"    {prop}: {annotation}")
            else:
                lines.append(f"    {prop}: {annotation} | None = None")
        if len(lines) == 4:
            lines.append("    pass")
        self.classes.append("\n".join(lines))
        return cls

    def run(self) -> str:
        shared = self.load(SHARED)
        for name, schema in shared["$defs"].items():
            self.type_of(SHARED, f"/$defs/{name}", schema, name)
        for file, cls in FILES.items():
            rel = f"plugins/v0/{file}"
            schema = self.load(rel)
            NAMES.setdefault(f"{rel}#", cls)
            self.object_class(rel, "", schema, cls)
        exports = sorted(self.used | {"Extensions"})
        body = "".join(c + "\n" for c in self.classes)
        tail = "\n\n# Keys are extension namespaces.\nExtensions = dict[str, ExtensionEntry]\n"
        tail += "\n__all__ = [\n" + "".join(f"    {json.dumps(n)},\n" for n in exports) + "]\n"
        return HEADER + body + tail


def outputs() -> dict[Path, bytes]:
    files = {PACKAGE / "models.py": Generator().run().encode()}
    for rel in [SHARED, *sorted(f"plugins/v0/{p.name}" for p in (CONTRACTS / "plugins/v0").glob("*.schema.json"))]:
        files[PACKAGE / "schemas" / rel] = (CONTRACTS / rel).read_bytes()
    return files


def main(argv: list[str]) -> int:
    check = argv[1:] == ["--check"]
    if argv[1:] and not check:
        print(__doc__, file=sys.stderr)
        return 2
    expected = outputs()
    schema_root = PACKAGE / "schemas"
    existing = {p for p in schema_root.rglob("*.json")} if schema_root.exists() else set()
    stale = sorted(str(p.relative_to(REPO)) for p, data in expected.items() if not p.exists() or p.read_bytes() != data)
    extra = sorted(str(p.relative_to(REPO)) for p in existing - set(expected))
    if check:
        if stale or extra:
            print("Python SDK generated files are stale; run python3 sdks/python/scripts/generate.py", file=sys.stderr)
            for path in stale + extra:
                print("  " + path, file=sys.stderr)
            return 1
        print(f"Python SDK generated files are up to date ({len(expected)} files)")
        return 0
    for path in extra:
        (REPO / path).unlink()
    for path, data in expected.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
    print(f"wrote {len(expected)} files")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
