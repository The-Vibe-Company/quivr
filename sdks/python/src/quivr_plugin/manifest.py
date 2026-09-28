"""Loading quivr-plugin.yaml and validating plugin configuration against it."""
from __future__ import annotations

import datetime
import hashlib
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml

from .errors import ConfigurationError
from .models import PluginManifest
from .schema import protocol_errors, schema_errors

MANIFEST_FILE = "quivr-plugin.yaml"
PLUGIN_API_VERSION = "0.1.0"
DEFAULT_MAX_RESPONSE_BYTES = 4 << 20


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
    def media_types(self) -> list[str]:
        return list(self.model.contributions.normalizer.media_types)

    @property
    def max_response_bytes(self) -> int:
        limits = self.model.contributions.normalizer.limits
        return (limits and limits.max_response_bytes) or DEFAULT_MAX_RESPONSE_BYTES

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
    return LoadedManifest(path=path.resolve(), raw=raw, model=PluginManifest.from_dict(document))


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
