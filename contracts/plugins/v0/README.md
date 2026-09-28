# Plugin Protocol v0

The authoritative, language-neutral contract between the Quivr engine and an
external plugin. It covers **Plugin API version `0.1.0`**. JSON Schemas in this
directory are the source of truth; SDKs and the Contract Runner implement them,
not the other way round. Design context: [ADR 0001](../../../docs/adr/0001-plugin-cli-and-contract-runner-in-quivr-binary.md),
[ADR 0002](../../../docs/adr/0002-record-version-identity-from-submitted-input.md)
and the glossary in [CONTEXT.md](../../../CONTEXT.md).

| Schema | Describes |
| --- | --- |
| `plugin-manifest.schema.json` | `quivr-plugin.yaml`, validated as its JSON equivalent |
| `discovery.schema.json` | `GET /v0/discovery` response |
| `health.schema.json` | `GET /v0/health` 200 response |
| `normalizer-request.schema.json` | `POST /v0/contributions/normalizer` request |
| `normalizer-response.schema.json` | `POST /v0/contributions/normalizer` 200 response |
| `error.schema.json` | Body of every non-2xx response |

The Manifest, Part, Extensions, Relation, SourceIdentity and Provenance shapes
are **not** defined here. They come from
[`contracts/shared/v0/manifest.schema.json`](../../shared/v0/manifest.schema.json),
the single source also used by the public HTTP contract, so a normalizer
returns exactly the Manifest a client could submit as `kind: "manifest"`.
`go test ./contracts/` (part of `make test`) fails if an `openapi.yaml`
component for one of those shapes is anything but an alias to the shared file,
or if a plugin schema declares a `$defs` entry with a shared name; the bundler
used by `make contracts` also rejects a malformed alias. Reviewers still keep
equivalent copies under other names out of both contracts.

## Contributions

Plugin API 0.1 accepts one Contribution: **`normalizer`**. The names
`connector`, `enricher`, `validator`, `projector`, `retriever` and
`subscription` are **reserved**. A v0 manifest that declares them is rejected
(`reserved_contribution`).

## HTTP routes

The plugin serves JSON over HTTP. Paths are versioned by the Plugin API major
version.

| Route | Success | Purpose |
| --- | --- | --- |
| `GET /v0/discovery` | 200 discovery document | Identity, implemented Plugin API version, Contributions and `manifest_digest` |
| `GET /v0/health` | 200 `{"status":"ok"}` | Ready to accept invocations; otherwise 503 with the error envelope |
| `POST /v0/contributions/normalizer` | 200 normalizer response | Normalize one input Blob |

- **Errors.** Every non-2xx response carries the error envelope
  `{code, message, retryable}`. `retryable: true` asks the engine to retry
  within the declared retry intent. `retryable: false` is terminal.
- **Unavailability.** A connection failure, a timeout, or a 5xx without a valid
  envelope counts as plugin unavailability, not a plugin decision.
- **Manifest digest.** `manifest_digest` is `sha256:` followed by the lowercase
  hex SHA-256 of the exact bytes of the `quivr-plugin.yaml` the plugin was built
  from. `quivr plugin inspect` prints the same value.
- **Future Contributions** use `/v0/contributions/<name>`.

## Manifest (`quivr-plugin.yaml`)

| Field | Meaning |
| --- | --- |
| `id` | Lowercase dotted or dashed identifier, at most 64 characters |
| `version` | SemVer 2.0.0 plugin version |
| `description` | Optional human description |
| `compatibility.engine`, `compatibility.plugin_api` | Version ranges (grammar below) |
| `contributions.normalizer.media_types` | Exact Blob media types the normalizer accepts; startup configuration routes them |
| `contributions.normalizer.timeout_ms` | Per-invocation timeout, 1000–300000, default 30000 |
| `contributions.normalizer.retry.max_attempts` | Retry intent for retryable errors, 1–10, default 3; the engine may cap it |
| `contributions.normalizer.limits` | Declared `max_response_bytes` (default 4 MiB, at most 16 MiB) and `max_parts` (default and maximum 256) |
| `configuration.schema` | JSON Schema 2020-12 for installer configuration |
| `secrets[]` | Secret names (`^[A-Z][A-Z0-9_]*$`), description, `required` (default true). Values never appear in the manifest |
| `extensions` | Owned extension namespaces: namespace, then schema version, then JSON Schema 2020-12 |
| `run.command` | Local development argv, without a shell |

### Version ranges

A range is one or more comparators separated by whitespace, and every
comparator must hold. A comparator is an optional operator (`>=`, `>`, `<=`,
`<`, `=`) directly followed by `MAJOR.MINOR.PATCH`. No operator means `=`.
Shorthands (`^`, `~`, `x`), `||` and pre-release versions inside a range are
not part of the grammar.

Versions compare by SemVer 2.0.0 precedence, so `0.2.0-rc.1` satisfies
`<0.2.0`. A range that no release version satisfies, such as
`>=0.3.0 <0.2.0`, is invalid. `fixtures/ranges.json` is normative for every
implementation.

This engine implements Plugin API `0.1.0` and reports engine version `0.1.0`.
Release builds may override the engine version. `quivr plugin inspect --json`
reports both.

## Invocation context

The normalizer request carries:

- the invocation id and idempotency key;
- the Organization, Corpus, Record and Record Version ids;
- the source namespace and Record Key;
- the input Blob id, media type, size and SHA-256;
- the submitted extensions and provenance;
- the validated plugin configuration;
- a reference to the input Blob: a short-lived signed GET URL or, in local
  development only, a `file://` URL.

Bodies are never inline. The idempotency key is opaque to plugins and stable
across retries of the same logical invocation. The same key must produce the
same logical output.

## Validation rules

**Checked today.** `quivr plugin inspect` checks a manifest against:

- the manifest schema, including unknown fields;
- well-formed and satisfiable ranges;
- compatibility with this engine and Plugin API version;
- reserved Contributions;
- extension namespaces equal to the plugin id or prefixed by `<id>.`;
- configuration and extension schemas that compile as JSON Schema 2020-12;
- duplicate secret names.

The normalizer response fixtures are checked against the response schema plus
the engine's structural Manifest rules: unique Part keys, known and acyclic
parents, valid text, complete Relation targets, and the Part count and
structure bounds.

**Protocol rules that are documented but not enforced yet.** These rules are
part of the v0 contract, but no code checks them yet. Nothing in this
repository enforces them before the slices below land.

1. **Blob Parts may reference only the input Blob.** A normalizer response
   must not introduce other Blobs. *Enforced from THE-682 (Contract Runner) and
   THE-683 (engine invocation).*
2. **Response size cap.** A response larger than the declared
   `max_response_bytes`, itself bounded by the engine, is invalid output.
   *Enforced from THE-682 (Contract Runner) and THE-683 (engine invocation).*
3. **Namespace ownership.** Response extensions may use only namespaces the
   plugin declares, and clients may not write plugin-owned namespaces.
   *Enforced from THE-684.*

## Normative fixtures

`fixtures/index.json` lists every fixture with its schema and two outcomes.
`schema_valid` is the result of JSON Schema validation alone. `valid` is the
result after the semantic rules. For manifests and normalizer responses,
`errors` lists the exact set of error codes. `fixtures/ranges.json` binds range
parsing and matching.

Go tests (`go test ./internal/plugins/...`) run all fixtures through the
engine's validation. `checks/validate.py`, run by `make contracts`, checks the
schema outcomes with an independent JSON Schema implementation.

## Try it

```bash
go run ./cmd/quivr plugin inspect contracts/plugins/v0/fixtures/manifests/valid/full.yaml
go run ./cmd/quivr plugin inspect --json contracts/plugins/v0/fixtures/manifests/invalid/reserved-contribution.yaml
```

Exit codes: `0` valid, `1` invalid or incompatible, `2` usage error.
