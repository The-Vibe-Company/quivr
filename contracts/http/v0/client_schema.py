"""Export client API input from the authoritative OpenAPI; write YAML to stdout.

OpenAPI Generator 7.25.0 incorrectly emits outgoing top-level webhooks as client
operations. Exclude only that receiver surface, preserving all paths/schemas.
The WebhookEvent model remains reachable through Delivery.event.
"""
from pathlib import Path
import sys

import yaml

spec = yaml.safe_load((Path(__file__).resolve().parent / "openapi.yaml").read_text())
spec.pop("webhooks", None)
yaml.safe_dump(spec, sys.stdout, sort_keys=False, allow_unicode=True)
