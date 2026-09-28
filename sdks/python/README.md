# Quivr Plugin SDK for Python

`quivr-plugin-sdk` (import `quivr_plugin`) implements the Plugin Protocol v0
([contract](../../contracts/plugins/v0/README.md)) so that a Python normalizer
is a single function. It has no Temporal, Weaviate or database clients, and its
only runtime dependencies are PyYAML and jsonschema (both MIT). Python 3.12 or
later.

The SDK is installed from this repository; it is not published to PyPI in v0.

```bash
pip install -e sdks/python                       # from a checkout
pip install "quivr-plugin-sdk @ git+https://github.com/The-Vibe-Company/quivr-v2#subdirectory=sdks/python"
```

Start a new plugin with `quivr plugin init <name>`. It writes a working
`text/markdown` normalizer, a fixture and tests that use only this SDK.

## A normalizer

```python
from pathlib import Path
from quivr_plugin import Invocation, ManifestContent, NormalizerResponse, Part, Plugin, TerminalError, TextContent

plugin = Plugin(Path(__file__).parent.parent / "quivr-plugin.yaml")

@plugin.normalizer
def normalize(invocation: Invocation) -> NormalizerResponse:
    text = invocation.read_input().decode("utf-8")   # verified against size and sha256
    if not text.strip():
        raise TerminalError("empty_document", "the document has no text")
    return NormalizerResponse(manifest=ManifestContent(parts=[
        Part(key="body", role="body", content=TextContent(text=text)),
    ]))

if __name__ == "__main__":
    plugin.serve()   # QUIVR_PLUGIN_HOST / QUIVR_PLUGIN_PORT, else 127.0.0.1:8080
```

## What the SDK does

| Concern | Behavior |
| --- | --- |
| Models | Dataclasses generated from the contract schemas (`quivr_plugin.models`), with `from_dict` and `to_dict` |
| Routes | `GET /v0/discovery` (plugin identity, Plugin API `0.1.0`, `sha256:` digest of the exact `quivr-plugin.yaml` bytes), `GET /v0/health`, `POST /v0/contributions/normalizer` |
| Request checks | Request schema → 400 `invalid_request`; media type not declared → 400 `unsupported_media_type`; configuration against the manifest configuration schema → 400 `invalid_configuration` |
| Errors | `RetryableError` → 503, `retryable: true`. `TerminalError` → 422, `retryable: false`. Unexpected exception → 500 `internal_error`, `retryable: false`. Always the protocol error envelope |
| Response checks | Before sending: response schema (500 `invalid_response`) and the declared `max_response_bytes` (500 `response_too_large`). The engine still applies its own Manifest validation |
| Input Blob | `Invocation.read_input()` reads `file://` or signed http(s) references, at most `size_bytes + 1` bytes. Transport errors and 401/403/408/425/429/5xx raise `RetryableError("input_unavailable")`; a size or SHA-256 mismatch raises `TerminalError` |
| Logging | `serve()` logs JSON lines to stderr. Every record emitted during an invocation carries `invocation_id` and `idempotency_key`; use `invocation.logger` or any logger |
| Health | `@plugin.health_check` may raise a `PluginError` to answer 503 while not ready |

## Testing a plugin

```python
from quivr_plugin.testing import expect_response, invoke_fixture

response = expect_response(invoke_fixture(plugin, "fixtures/sample.json"))
```

`invoke_fixture` builds the same request `quivr plugin dev --fixture` sends,
using an [invocation fixture](../../contracts/plugins/v0/plugin-fixture.schema.json),
and runs the normalizer route in process. `Plugin.invoke(request)` and
`Plugin.handle(method, path, body)` expose the same dispatch without HTTP.

## Maintaining the SDK

```bash
python3 sdks/python/scripts/generate.py          # regenerate models.py and schema copies after a contract change
python3 sdks/python/scripts/generate.py --check  # fails when they are stale (part of make test)
bash scripts/plugin_sdk.sh                       # SDK tests and a scaffolded plugin end to end (part of make test)
```

The generator is standard-library only, so the output is reproducible. It
reads `contracts/shared/v0/manifest.schema.json` and
`contracts/plugins/v0/*.schema.json`. Never edit `models.py` or
`src/quivr_plugin/schemas/` by hand.
