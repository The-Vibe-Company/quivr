"""Export client API input from the authoritative OpenAPI; write YAML to stdout.

OpenAPI Generator 7.25.0 incorrectly emits outgoing top-level webhooks as client
operations. Exclude only that receiver surface, preserving all paths/schemas.
The WebhookEvent model remains reachable through Delivery.event. Shared Manifest
components are inlined by bundle.py so generators see one self-contained file.
"""
import sys

import yaml

from bundle import load

spec = load()
spec.pop("webhooks", None)
yaml.safe_dump(spec, sys.stdout, sort_keys=False, allow_unicode=True)
