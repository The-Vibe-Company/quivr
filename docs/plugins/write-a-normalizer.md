# Write a normalizer

A **normalizer** turns a Blob of a given media type into a Manifest: ordered
Parts of text that Quivr indexes and searches. It runs in its own process and
speaks the Plugin Protocol v0 over HTTP. Quivr calls it after it accepts a Blob
and before it publishes the Record Version.

This guide takes you from an empty directory to a searchable Record whose
Version names your plugin. You write Python with the Quivr Plugin SDK and use
the `quivr` command line; you do not need to read Quivr's Go code.

| Step | Command | Needs a running Quivr |
| --- | --- | --- |
| [1. Get the tools](#1-get-the-tools) | `go build ./cmd/quivr`, a virtualenv | no |
| [2. Scaffold](#2-scaffold) | `quivr plugin init` | no |
| [3. Develop](#3-develop) | unit tests, `quivr plugin dev --fixture` | no |
| [4. Make it yours](#4-make-it-yours) | edit the manifest and the function | no |
| [5. Certify](#5-certify) | `quivr plugin test` | no |
| [6. Pin](#6-pin-it-in-quivr_config) | `plugin` in `QUIVR_CONFIG` | yes |
| [7. Ingest](#7-ingest-a-blob) | Upload Session, `POST /v0/records` | yes |
| [8. Observe](#8-observe-provenance-and-diagnostics) | search, Version, Receipt, logs | yes |

For a complete example, read the reference plugin [`plugins/pdf-text`](../../plugins/pdf-text/README.md).
It extracts PDF text into one Part per page.

## 1. Get the tools

You need Python 3.12 or later and Go (the version in `go.mod`), plus Git.
Steps 6 to 8 also need a running Quivr. The local stack, `make dev`, needs
Linux x86_64 and Docker (see the [local harness](../quivr-v2-local-harness.md)).
Any installation where you can edit the startup configuration works too.

```bash
git clone https://github.com/The-Vibe-Company/quivr-v2
cd quivr-v2
go build -o bin/quivr ./cmd/quivr
export PATH="$PWD/bin:$PATH"
QUIVR_REPO=$PWD
```

The Python Plugin SDK is installed from this repository; it is not on PyPI in v0.

## 2. Scaffold

```bash
cd ..
quivr plugin init field-notes
cd field-notes
python3 -m venv .venv && . .venv/bin/activate
pip install -e "$QUIVR_REPO/sdks/python" -e .
```

`quivr plugin init` writes a working normalizer for `text/markdown`:

| Path | Purpose |
| --- | --- |
| `quivr-plugin.yaml` | Identity, compatibility ranges, media types, limits, configuration schema, run command |
| `field_notes/normalizer.py` | The normalizer function |
| `field_notes/__main__.py` | Serves the plugin over HTTP (`python3 -m field_notes`) |
| `fixtures/` | Invocation fixtures and their input files |
| `tests/` | Unit tests that use `quivr_plugin.testing` |

## 3. Develop

```bash
python3 -m unittest discover -s tests                # unit tests, in process
quivr plugin inspect .                               # validate quivr-plugin.yaml
quivr plugin dev --fixture fixtures/sample.json      # run it and replay one fixture
```

`quivr plugin dev --fixture` starts `run.command` from the manifest and waits
for `GET /v0/health`. It then checks that `GET /v0/discovery` serves the digest
of this exact `quivr-plugin.yaml`, sends the fixture through a `file://`
reference, and prints the response once Quivr's Manifest validation accepts it:

```text
quivr plugin dev: response valid: 4 Parts, 0 Relations, 0 warnings; the engine's Manifest validation accepts it
```

Add `--watch` to replay after every source change.

## 4. Make it yours

### The manifest

```yaml
id: field-notes                   # lowercase; the name the Version's provenance shows
version: 0.1.0                    # SemVer; bump it when the output changes
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.1.0 <0.2.0"
contributions:
  normalizer:
    media_types: [text/markdown]  # exact types; Quivr routes only the ones it is configured to route
    timeout_ms: 10000             # Quivr caps it at 2 minutes
    retry:
      max_attempts: 3             # RetryableError or timeout attempts before quarantine (capped at 5)
    limits:                       # optional
      max_parts: 256
      max_response_bytes: 4194304
configuration:
  schema: {type: object, ...}     # JSON Schema of the configuration the operator pins
extensions:                       # namespaces you own, with a JSON Schema per version
  field-notes.outline:
    "1": {type: object, ...}
run:
  command: [python3, -m, field_notes]
```

Every edit of `quivr-plugin.yaml` changes its digest. Restart the plugin, and
restart Quivr's API and worker, so that both sides use the same manifest.
[Plugin Protocol v0](../../contracts/plugins/v0/README.md) documents every field.

### The function

```python
@plugin.normalizer
def normalize(invocation: Invocation) -> NormalizerResponse:
    data = invocation.read_input()                  # bytes, verified against size and SHA-256
    config = invocation.configuration               # already validated against your schema
    ...
    return NormalizerResponse(
        manifest=ManifestContent(parts=[Part(key="body", role="body", content=TextContent(text=text))]),
        warnings=[ResponseWarning(code="something_skipped", message="…")] or None,
    )
```

What Quivr accepts:

- **Parts.** Each Part has a unique `key`. Text Parts with the roles `title`
  (at most one) and `body` are indexed; other roles are stored but not searched.
  A search hit names its Part through `part_key`, so choose keys a reader can
  understand, such as `page-2` or `section-3`. Text must be valid UTF-8 without
  NUL characters.
- **Blob Parts.** A Blob Part may reference only the input Blob:
  `BlobContent(blob_id=invocation.request.input.blob_id, media_type=…)`.
- **Extensions.** Structured data goes in extension namespaces your manifest
  declares under `extensions:`. Each namespace is your plugin id, or starts
  with it followed by a dot, and has a JSON Schema per schema version.
  - Return them with `extensions={"field-notes.outline": ExtensionEntry(schema_version="1", data={...})}`,
    as the template does.
  - Quivr validates them against your schema and publishes them on the
    Version. Retrieval mappings may point at `/extensions/<namespace>/data/...`.
  - Clients cannot write your namespaces.
  - An undeclared namespace or invalid data quarantines the Version with
    `normalizer_invalid_output`.
- **Warnings.** At most 32 warnings; a `code` in `snake_case` and a message of
  up to 1024 characters. Log them too (`invocation.logger.warning`) so that
  operators see them.
- **Errors.** Raise `RetryableError(code, message)` for a transient failure that
  Quivr should retry, and `TerminalError(code, message)` for input that can never
  be normalized, such as a damaged file. Do not return an empty Manifest.
- **Determinism.** The same input must give the same output. Quivr may call the
  plugin again for the same Version, and `quivr plugin test` replays every fixture.

What Quivr indexes per Record Version (a Version beyond these limits is
published but not searchable, with `segmentation_limit`):

| Limit | Value |
| --- | --- |
| Indexed text Parts (`title` + `body`) | 64 |
| Indexed text | 256 KiB (UTF-8) |
| Indexed segments (windows of about 384 tokens) | 256 |
| Parts in a Manifest | 256 |
| Stored Manifest | 2 MiB |
| Response | declared `max_response_bytes`, default 4 MiB, capped at 16 MiB |
| One invocation | declared `timeout_ms`, capped at 2 minutes; a timeout is retried up to `retry.max_attempts`, then quarantined |
| Input Blob (Upload Session) | 1 GiB |

Stay within them in the plugin itself, for example by merging trailing Parts, and
report what you merged or dropped as a warning. pdf-text shows how.

Add a fixture for each case you care about: a `fixtures/<name>.json` describes
the input file, media type and configuration
([schema](../../contracts/plugins/v0/plugin-fixture.schema.json)). Test error
cases with unit tests: every fixture in `fixtures/` must succeed, while unit
tests can assert a `422` reply and its code, as the template does.

## 5. Certify

```bash
quivr plugin test --report contract-report.json
```

The Contract Runner starts your plugin and checks it over the public protocol
only. It runs health and discovery, invokes every fixture, replays each
idempotency key, enforces `timeout_ms`, and sends invalid requests. It judges
the output with Quivr's own validation. It exits `0` and prints
`CERTIFIED: the engine can safely invoke this normalizer` only when Quivr can
safely call your plugin. Run it in your CI and keep the JSON report.

## 6. Pin it in `QUIVR_CONFIG`

Quivr invokes one pinned plugin in v0. Add a `plugin` object to the startup
configuration of both `quivr api` and `quivr worker`:

```json
"plugin": {
  "manifest": "/opt/plugins/field-notes/quivr-plugin.yaml",
  "endpoint": "http://127.0.0.1:9900",
  "configuration": {"max_sections": 32},
  "routes": [{"media_type": "text/markdown", "mode": "required"}]
}
```

- `manifest` is the path to the same `quivr-plugin.yaml` the plugin serves.
- `endpoint` is where the plugin listens. Run it there with
  `QUIVR_PLUGIN_HOST=127.0.0.1 QUIVR_PLUGIN_PORT=9900 python3 -m field_notes`, or with
  `quivr plugin dev --port 9900 .` while you develop (it restarts on changes).
- `configuration` is validated against your configuration schema.
- `routes` lists the media types Quivr sends to the plugin. Each must be one your
  normalizer declares. `required` is the only mode in v0.

`quivr api` and `quivr worker` refuse to start when the pin is invalid, and list
every issue: an incompatible range, a configuration that fails the schema, or
an undeclared route. They never contact the plugin at startup, so a plugin that
is down does not stop Quivr.

**With the local stack.** `make dev` pins pdf-text by default. To pin your own
plugin instead, run it on a port and point `QUIVR_NORMALIZER` at its directory:

```bash
quivr plugin dev --port 9900 ../field-notes          # terminal 1, from the plugin's virtualenv
QUIVR_NORMALIZER=$PWD/../field-notes make dev        # terminal 2, in the quivr-v2 checkout
```

The stack routes every media type your manifest declares. `QUIVR_NORMALIZER_PORT`
changes the port (default 9900), and `QUIVR_NORMALIZER_CONFIG='{"max_sections": 8}'`
sets the configuration. `make dev` prints the admin API key's location. Run
`make dev` again after editing the manifest.

## 7. Ingest a Blob

A Blob is uploaded through an Upload Session, then ingested by reference:

```bash
API=http://127.0.0.1:<api_port>; KEY=<API key>; FILE=notes.md; TYPE=text/markdown
SIZE=$(wc -c < "$FILE" | tr -d ' '); SHA=$(shasum -a 256 "$FILE" | cut -d' ' -f1)
CORPUS=$(curl -s -X POST "$API/v0/corpora" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"name":"Field notes","idempotency_key":"field-notes-corpus"}' | jq -r .corpus_id)

curl -s -X POST "$API/v0/uploads" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"size_bytes\":$SIZE,\"sha256\":\"$SHA\",\"media_type\":\"$TYPE\"}" > upload.json
BLOB=$(jq -r '.blob_id // empty' upload.json)   # set when these bytes are already a verified Blob
if [ -z "$BLOB" ]; then
  HEADERS=(); while IFS= read -r h; do HEADERS+=(-H "$h"); done \
    < <(jq -r '.upload_headers // {} | to_entries[] | "\(.key): \(.value)"' upload.json)
  curl -s -X PUT "${HEADERS[@]}" --data-binary @"$FILE" "$(jq -r .upload_url upload.json)"
  BLOB=$(curl -s -X POST "$API/v0/uploads/$(jq -r .upload_id upload.json)/confirm" -H "Authorization: Bearer $KEY" | jq -r .blob_id)
fi

RECEIPT=$(curl -s -X POST "$API/v0/records" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"idempotency_key\":\"notes-v1\",\"source\":{\"corpus_id\":\"$CORPUS\",\"namespace\":\"notes\",\"record_key\":\"notes\"},
       \"content\":{\"kind\":\"blob\",\"blob_id\":\"$BLOB\",\"media_type\":\"$TYPE\"}}" | jq -r .receipt_id)
```

A media type that is neither `text/*` nor routed is refused with
`422 unverified_blob`.

## 8. Observe provenance and diagnostics

**Receipt.** `GET /v0/ingestion-receipts/$RECEIPT` shows progress. Once the
plugin has answered and the Version is published, the Receipt is `resolved`,
and `availability.searchable` becomes `true` when the text is indexed. While
Quivr waits for the plugin, the Receipt stays `pending`, and `processing.state`
and `diagnostics` explain why: `plugin_unavailable` means Quivr cannot reach the
plugin, or the discovery digest differs from the pinned manifest. Quivr keeps
retrying that case without limit and never quarantines for it. Start the plugin
at `endpoint`, and restart it after a manifest change.

**Quarantine.** When your plugin cannot produce acceptable output, the Version is
quarantined:
- the Receipt resolves with `availability.state: "quarantined"`;
- the Version is published with only its submitted input Blob Part and none of
  your output, so it is not searchable;
- a `record.quarantined` change event is emitted.

| Diagnostic `code` | Meaning | What to do |
| --- | --- | --- |
| `normalizer_failed` | Your plugin raised `TerminalError` | Expected for bad input; the message carries your code and message |
| `normalizer_invalid_output` | Quivr refused the output: schema, Manifest rules, `max_parts`, foreign Blob Part, undeclared or invalid extensions, size | Reproduce with `quivr plugin dev --fixture` or `quivr plugin test` on the same input |
| `normalizer_timeout` | Invocations exceeded `timeout_ms` `retry.max_attempts` times (capped at 5) | Make slow inputs faster, or bound them and warn |
| `normalizer_retries_exhausted` | `RetryableError` was raised `retry.max_attempts` times (capped at 5) | Keep `RetryableError` for transient causes |

`GET /v0/records/{record_id}/versions/{version_id}` lists the same reason in
`diagnostics`, naming your plugin and the invocation:

```json
{"code": "normalizer_failed", "message": "…", "retryable": false,
 "plugin": "field-notes", "contribution": "normalizer", "invocation_id": "inv_…"}
```

Reprocessing quarantined Versions is not available yet. With the local stack,
`make reset` starts over.

**Search.** Your text Parts are searchable like any other:

```bash
curl -s -X POST "$API/v0/search" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"query\":\"water levels\",\"corpus_ids\":[\"$CORPUS\"],\"mode\":\"lexical\"}" | jq '.items[] | {record_id, version_id, part_key}'
```

**Version provenance.** `GET /v0/records/{record_id}/versions/{version_id}`
returns the Manifest your plugin produced, your extensions and the `provenance`:

```json
"extensions": {
  "field-notes.outline": {"schema_version": "1", "data": {"heading_count": 3, "heading_levels": ["h1", "h2"]}}
},
"provenance": {
  "producer": "…the client or Connector Instance that submitted the Blob…",
  "source_blob_ids": ["blob_…"],
  "normalization": {
    "plugin_id": "field-notes",
    "plugin_version": "0.1.0",
    "plugin_api": "0.1.0",
    "contribution": "normalizer",
    "invocation_id": "inv_…",
    "idempotency_key": "…",
    "input_sha256": "…"
  }
}
```

The Version keeps this output: rebuilding the search projections never calls
your plugin again, and a newer plugin version applies only to new Versions.

**Logs.** The SDK writes one JSON line per event to the plugin's stderr. Every
line written during an invocation carries the `invocation_id` from the Version's
provenance and the `idempotency_key`. Errors are logged as
`normalizer failed` with your `code`. With the local stack, the pdf-text and
template logs are in `.scratch/<project>/normalizer-plugin.log`, and the worker's
logs are in `.scratch/<project>/worker.log`.

## Next

- [Python Plugin SDK](../../sdks/python/README.md): models, errors, test helpers.
- [Plugin Protocol v0](../../contracts/plugins/v0/README.md): the manifest, the
  routes and the schemas, for a plugin in another language.
- [`plugins/pdf-text`](../../plugins/pdf-text/README.md): a binary format, page
  Parts, bounded warnings, terminal errors and reproducible fixtures.
