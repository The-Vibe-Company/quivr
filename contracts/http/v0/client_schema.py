"""Export client API input from the authoritative OpenAPI; write YAML to stdout.

OpenAPI Generator 7.25.0 incorrectly emits outgoing top-level webhooks as client
operations. Exclude that receiver surface, preserving all paths/schemas.
The WebhookEvent model remains reachable through Delivery.event. Shared Manifest
components are inlined by bundle.py so generators see one self-contained file.
With --opaque-plugin-responses, client generators keep declared plugin reply
bodies as bytes: their status/media/body cannot be decoded as an engine Error.
Transport generation retains the authoritative engine response schemas.
"""
import sys

import yaml

from bundle import load

spec = load()
spec.pop("webhooks", None)
if "--opaque-plugin-responses" in sys.argv[1:]:
    for path in spec["paths"].values():
        for operation in path.values():
            if isinstance(operation, dict) and operation.get("x-quivr-plugin-response"):
                operation["responses"]["default"]["content"] = {
                    "*/*": {"schema": {"type": "string", "format": "binary"}}
                }
yaml.safe_dump(spec, sys.stdout, sort_keys=False, allow_unicode=True)
