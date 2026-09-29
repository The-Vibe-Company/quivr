"""Loading quivr-plugin.yaml and validating plugin configuration against it."""
from __future__ import annotations

import datetime
import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml

from .errors import ConfigurationError
from .models import PluginManifest
from .schema import protocol_errors, schema_errors

MANIFEST_FILE = "quivr-plugin.yaml"
# The highest Plugin API version this SDK implements.
PLUGIN_API_VERSION = "0.2.0"
# Every Plugin API version this SDK can serve, oldest first. A minor version
# only adds to the previous one; discovery reports the highest version the
# manifest's plugin_api range admits.
SUPPORTED_PLUGIN_API_VERSIONS = ("0.1.0", "0.2.0")
DEFAULT_MAX_RESPONSE_BYTES = 4 << 20
DEFAULT_MAX_BATCH_SIZE = 32

_COMPARATOR = re.compile(r"^(>=|<=|>|<|=)?(\d+)\.(\d+)\.(\d+)$")


def negotiate_plugin_api(declared_range: str) -> str | None:
    """Return the highest supported Plugin API version a manifest range admits, or None.

    The range grammar is the one of contracts/plugins/v0/README.md: whitespace-separated
    comparators (>=, >, <=, <, =, none meaning =) over MAJOR.MINOR.PATCH, all of which must hold.
    """
    comparators = []
    for token in declared_range.split():
        match = _COMPARATOR.match(token)
        if not match:
            return None
        comparators.append((match.group(1) or "=", tuple(int(g) for g in match.group(2, 3, 4))))
    tests = {">=": lambda a, b: a >= b, ">": lambda a, b: a > b, "<=": lambda a, b: a <= b,
             "<": lambda a, b: a < b, "=": lambda a, b: a == b}
    for version in reversed(SUPPORTED_PLUGIN_API_VERSIONS):
        candidate = tuple(int(n) for n in version.split("."))
        if comparators and all(tests[op](candidate, bound) for op, bound in comparators):
            return version
    return None


class ManifestError(ValueError):
    """quivr-plugin.yaml is unreadable or does not match the manifest schema."""


@dataclass(frozen=True)
class LoadedManifest:
    """A parsed plugin manifest together with the digest of its exact bytes."""

    path: Path
    raw: bytes
    model: PluginManifest

    @property
    def digest(self) -> str:
        """``sha256:<hex>`` of the raw manifest bytes, as served by discovery."""
        return "sha256:" + hashlib.sha256(self.raw).hexdigest()

    @property
    def contributions(self) -> list[str]:
        """Declared Contributions in protocol order, as discovery lists them."""
        declared = self.model.contributions
        return [name for name in ("normalizer", "subscription") if getattr(declared, name) is not None]

    @property
    def plugin_api(self) -> str:
        """The Plugin API version discovery reports: the highest supported one the range admits."""
        return negotiate_plugin_api(self.model.compatibility.plugin_api) or PLUGIN_API_VERSION

    @property
    def media_types(self) -> list[str]:
        normalizer = self.model.contributions.normalizer
        return list(normalizer.media_types) if normalizer else []

    @property
    def max_response_bytes(self) -> int:
        """Declared max_response_bytes of the normalizer."""
        normalizer = self.model.contributions.normalizer
        limits = normalizer and normalizer.limits
        return (limits and limits.max_response_bytes) or DEFAULT_MAX_RESPONSE_BYTES

    @property
    def subscription_max_response_bytes(self) -> int:
        subscription = self.model.contributions.subscription
        limits = subscription and subscription.limits
        return (limits and limits.max_response_bytes) or DEFAULT_MAX_RESPONSE_BYTES

    @property
    def max_batch_size(self) -> int:
        subscription = self.model.contributions.subscription
        return (subscription and subscription.max_batch_size) or DEFAULT_MAX_BATCH_SIZE

    def validate_expression(self, expression: Any) -> list[str]:
        """Problems of a Saved Query expression against the declared expression_schema."""
        subscription = self.model.contributions.subscription
        return _object_problems(subscription.expression_schema if subscription else None, expression)

    def validate_subscription_configuration(self, configuration: Any) -> list[str]:
        """Problems of a Subscription evaluator configuration against the declared configuration_schema."""
        subscription = self.model.contributions.subscription
        return _object_problems(subscription.configuration_schema if subscription else None, configuration)

    @property
    def configuration_schema(self) -> Any:
        return self.model.configuration.schema if self.model.configuration else None

    def validate_configuration(self, configuration: Any) -> None:
        """Raise ConfigurationError when configuration violates the manifest configuration schema."""
        validate_configuration(self.configuration_schema, configuration)


def _jsonable(value: Any) -> Any:
    # YAML-only scalars (timestamps) become strings, as quivr plugin inspect does.
    if isinstance(value, dict):
        return {str(k): _jsonable(v) for k, v in value.items()}
    if isinstance(value, list):
        return [_jsonable(v) for v in value]
    if isinstance(value, (datetime.date, datetime.datetime)):
        return value.isoformat()
    return value


def load_manifest(path: str | Path) -> LoadedManifest:
    """Read and schema-check a manifest file, or quivr-plugin.yaml inside a directory.

    Semantic checks (ranges, compatibility, namespace prefixes) belong to
    ``quivr plugin inspect``; this only guarantees the shape the SDK relies on.
    """
    path = Path(path)
    if path.is_dir():
        path = path / MANIFEST_FILE
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise ManifestError(f"cannot read {path}: {exc}") from exc
    try:
        document = _jsonable(yaml.safe_load(raw))
    except yaml.YAMLError as exc:
        raise ManifestError(f"{path} is not valid YAML: {exc}") from exc
    problems = protocol_errors("plugin-manifest.schema.json", document)
    if problems:
        raise ManifestError(f"{path} does not match the plugin manifest schema: " + "; ".join(problems))
    if "connector" in (document.get("contributions") or {}):
        raise ManifestError(f"{path} declares a connector Contribution, which this SDK does not serve yet; write connectors with the Go SDK in sdks/go")
    return LoadedManifest(path=path.resolve(), raw=raw, model=PluginManifest.from_dict(document))


def _object_problems(schema: Any, value: Any) -> list[str]:
    if not isinstance(value, dict):
        return [f"/: expected an object, got {json.dumps(value)[:64]}"]
    return [] if schema is None else schema_errors(schema, value)


def validate_configuration(schema: Any, configuration: Any) -> None:
    """Validate configuration against a manifest configuration schema (None accepts any object).

    Raises ConfigurationError listing every problem as "<JSON pointer>: <message>".
    """
    if not isinstance(configuration, dict):
        raise ConfigurationError([f"/: expected an object, got {json.dumps(configuration)[:64]}"])
    if schema is None:
        return
    problems = schema_errors(schema, configuration)
    if problems:
        raise ConfigurationError(problems)
