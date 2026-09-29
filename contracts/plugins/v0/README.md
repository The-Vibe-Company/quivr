# Plugin Protocol v0

The authoritative, language-neutral contract between the Quivr engine and an
external plugin. It covers **Plugin API version `0.2.0`**, which adds the
`subscription` Contribution to Plugin API `0.1.0`. JSON Schemas in this
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
| `subscription-request.schema.json` | `POST /v0/contributions/subscription` request (since 0.2) |
| `subscription-response.schema.json` | `POST /v0/contributions/subscription` 200 response (since 0.2) |
| `error.schema.json` | Body of every non-2xx response |
| `plugin-fixture.schema.json` | Invocation fixture: a local test input that tools turn into a normalizer request |
| `subscription-fixture.schema.json` | Subscription fixture: a local test input that tools turn into subscription requests (since 0.2) |
| `reports/contract-report.schema.json` | JSON report of `quivr plugin test --report` (tooling, not protocol) |

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

Plugin API 0.2 accepts two Contributions, and a manifest declares at least one:

| Contribution | Since | Purpose |
| --- | --- | --- |
| **`normalizer`** | 0.1 | Turn one input Blob into the Parts, Relations and extensions of a Record Version |
| **`subscription`** | 0.2 | An alert rule: decide whether one Record Version matches each Saved Query expression of a batch ([below](#subscription-contribution)) |

The names `connector`, `enricher`, `validator`, `projector` and `retriever`
are **reserved**. A manifest that declares them is rejected
(`reserved_contribution`).

### Plugin API versions

A minor Plugin API version only adds to the previous one. This engine
implements `0.2.0` and still serves every `0.1` plugin unchanged: a manifest
is compatible when its `plugin_api` range admits any supported version
(`0.1.0` or `0.2.0`), and the engine speaks the highest one the range admits.
`quivr plugin inspect` reports that negotiated version. Discovery must serve
a supported version inside the declared range that is at least the version
each declared Contribution needs; SDKs serve the negotiated one. A
Contribution needs a range that admits the version that introduced it:
`subscription` with `plugin_api: ">=0.1.0 <0.2.0"` is
`incompatible_plugin_api` at `/contributions/subscription`. Discovery lists
Contributions as a set, in any order.

## HTTP routes

The plugin serves JSON over HTTP. Paths are versioned by the Plugin API major
version.

| Route | Success | Purpose |
| --- | --- | --- |
| `GET /v0/discovery` | 200 discovery document | Identity, implemented Plugin API version, Contributions and `manifest_digest` |
| `GET /v0/health` | 200 `{"status":"ok"}` | Ready to accept invocations; otherwise 503 with the error envelope |
| `POST /v0/contributions/normalizer` | 200 normalizer response | Normalize one input Blob |
| `POST /v0/contributions/subscription` | 200 subscription response | Decide a batch of evaluations for one Record Version |

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
| `contributions.subscription.expression_schema` | JSON Schema 2020-12 of the Saved Query expression (a JSON object) the rule interprets |
| `contributions.subscription.configuration_schema` | Optional JSON Schema 2020-12 of the per-Subscription evaluator configuration (a JSON object); absent accepts any object |
| `contributions.subscription.max_batch_size` | Most evaluations per request, 1–256, default 32; the core splits larger batches |
| `contributions.subscription.timeout_ms`, `.retry.max_attempts`, `.limits.max_response_bytes` | As for the normalizer |
| `contributions.subscription.vectors` | Reserved for local-vector matching in a later minor version (`reserved_field`) |
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

This engine implements Plugin API `0.2.0` (and serves `0.1.0`) and reports
engine version `0.1.0`. Release builds may override the engine version.
`quivr plugin inspect --json` reports both, and the negotiated Plugin API
version under `compatibility.plugin_api.version`.

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
- compatibility with this engine and a supported Plugin API version, and
  with the version each declared Contribution needs;
- reserved Contributions and reserved fields;
- subscription expression and configuration schemas that compile as JSON
  Schema 2020-12 (`invalid_expression_schema`, `invalid_config_schema`);
- extension namespaces equal to the plugin id or prefixed by `<id>.`;
- configuration and extension schemas that compile as JSON Schema 2020-12;
- duplicate secret names.

The normalizer response fixtures are checked against the response schema plus
the engine's structural Manifest rules: unique Part keys, known and acyclic
parents, valid text, complete Relation targets, and the Part count and
structure bounds.

**Protocol rules checked at certification time and enforced at ingestion.**
These rules are part of the v0 contract. `quivr plugin test` (the Contract
Runner, below) checks them on every normalizer response with the validation in
`internal/plugins` (`CheckNormalizerOutput`), and the engine applies the same
function to every invocation of a pinned normalizer before anything is
recorded.

1. **Blob Parts may reference only the input Blob.** A normalizer response
   must not introduce other Blobs. A Blob Part must name the input Blob id
   with its media type (`foreign_blob`, `unverified_blob`). The public Blob
   Part carries no checksum. The reference is resolved to the verified input
   Blob, and its SHA-256 must equal the request's `input.sha256`. The runner
   re-hashes the file it served. *Checked by the Contract Runner; enforced by
   the engine (THE-683).*
2. **Response size cap.** A response larger than the declared
   `max_response_bytes` (default 4 MiB) is invalid output
   (`response_too_large`). The engine caps the declared value at 16 MiB.
   *Checked by the Contract Runner; enforced by the engine (THE-683).*
3. **Namespace ownership.** Response extensions, top-level and on Parts, may
   use only namespaces and schema versions the plugin declares, with data
   valid against the declared schema (`undeclared_namespace`,
   `undeclared_schema_version`, `invalid_extension`). Clients may not write
   plugin-owned namespaces. *The output side is checked by the Contract
   Runner. The engine enforces both sides (THE-684): it registers the pinned
   plugin's namespaces at startup (refusing a namespace not prefixed by the
   plugin id, `foreign_namespace`, or clashing with a built-in one,
   `namespace_conflict`), publishes valid output extensions on the Version,
   fails invalid ones as `normalizer_invalid_output`, and rejects client
   writes with 422 `extension_namespace_owned`. Retrieval mappings may point
   at `/extensions/{plugin namespace}/...`.*

A response with more Parts than the declared `max_parts` is also invalid
(`too_many_parts`).

## Subscription Contribution

An alert rule (since Plugin API 0.2). The core sends one Record Version and a
batch of distinct evaluations; the plugin answers one decision per
evaluation. The core has not called it yet; the Contract Runner certifies it.

**Request** (`subscription-request.schema.json`):

- `invocation_id`, `idempotency_key`, `organization_id` and the validated
  plugin (installer) `configuration`, as for every Contribution;
- `record`: the Corpus, Record and Record Version ids, `enriched` (embedding
  coverage in the active generation) and the canonical text `parts`
  (`key`, `role`, `text`; at most 256, unique keys, Blob Parts are not sent);
- `evaluations`: 1 to `max_batch_size` items, each with an `id` unique in the
  request, the pinned Saved Query Version `expression`, the pinned Subscription
  evaluator `configuration`, and the `subscriptions` (Subscription, Subscription
  Version, Saved Query and Saved Query Version ids) it stands for.

The core **deduplicates**: Subscriptions that share the same expression and
configuration become one evaluation. The ids are informational (logs,
tracing). A decision must depend only on the record, the expression and the
configurations, never on the ids, the position in the batch or the other
evaluations, because the core batches freely. The same idempotency key must
yield the same decisions and evidence. Requests stay within 16 MiB.

Per-Part `vector` and per-evaluation `query_vector` are **reserved** for
local-vector matching; Plugin API 0.2 never sends them and the schemas reject
them.

**Response** (`subscription-response.schema.json`): `decisions`, exactly one
per requested evaluation id.

| `decision` | Meaning |
| --- | --- |
| `match` | The Record Version satisfies the expression. Evidence is required; the core stores it with the Match |
| `no_match` | It does not. Evidence is optional |
| `not_ready` | It cannot be decided yet, for example before enrichment. The core evaluates again on a later trigger |

A rule that cannot decide at all (a backend down) answers with the error
envelope. `retryable: true` makes the core retry, and plugin unavailability
never becomes a negative decision. A request the schemas reject is refused
with `retryable: false`.

**Evidence bounds.** They mirror the bounds the monitoring engine applies to
Match evidence (`internal/monitoring`), and `CheckSubscriptionOutput` in
`internal/plugins` judges every answer:

| Rule | Code |
| --- | --- |
| `explanation` non-empty, at most 4096 Unicode code points | `evidence_too_large` (schema: `schema_violation`) |
| `part_keys` at most 100, each a Part key of the request | `evidence_too_large`, `unknown_part_key` |
| `details` a JSON object of at most 16 KiB once serialized | `details_too_large` |
| No NUL character in the evidence | `invalid_evidence` |
| One decision per requested id, none for other ids | `missing_decision`, `duplicate_decision`, `unknown_decision` |
| A `match` carries evidence | `missing_evidence` |
| Response within `max_response_bytes` | `response_too_large` |

**Expression and configuration schemas.** The core validates Saved Query
expressions against `expression_schema` and Subscription configurations
against `configuration_schema` when it pins them, so a rule receives only
values its schemas accept (`ValidateSubscriptionItem`, codes
`invalid_expression` and `invalid_subscription_configuration`). To offer
several kinds of alert, discriminate them with a `oneOf` over a constant
`kind` property; `quivr plugin inspect` lists the kinds.

## Normative fixtures

`fixtures/index.json` lists every fixture with its schema and two outcomes.
`schema_valid` is the result of JSON Schema validation alone. `valid` is the
result after the semantic rules. For manifests, normalizer responses and
subscription responses, `errors` lists the exact set of error codes. A
subscription response is judged against the request its `request` field
names. `fixtures/ranges.json` binds range parsing and matching.

Go tests (`go test ./internal/plugins/...`) run all fixtures through the
engine's validation. `checks/validate.py`, run by `make contracts`, checks the
schema outcomes with an independent JSON Schema implementation.

## Local development

These conventions are part of the v0 tooling contract. They bind SDKs in every
language and the Contract Runner, but not the engine.

**Run convention.** `quivr plugin dev` starts the manifest's `run.command`
(argv, no shell) in the plugin directory, in its own process group, with:

| Variable | Value |
| --- | --- |
| `QUIVR_PLUGIN_HOST` | Interface to bind, `127.0.0.1` |
| `QUIVR_PLUGIN_PORT` | Port assigned for the session |
| `QUIVR_PLUGIN_MANIFEST` | Absolute path of the inspected `quivr-plugin.yaml` |

The plugin must serve the routes above on that address. `dev` stops it with
SIGTERM, then SIGKILL after five seconds.

**Invocation fixtures** (`plugin-fixture.schema.json`) name an input file,
relative to the fixture, with its media type and optional `configuration`,
`source`, `extensions` and `provenance`. A tool turns one into a normalizer
request:

- `input.reference` is `{"kind": "file", "url": <absolute file:// URL>}`, and
  `size_bytes` and `sha256` are computed from the file;
- the ids are development values derived from the first 16 hex digits of the
  input SHA-256: `dev-invocation-…`, `dev-record-…`, `dev-version-…`,
  `dev-blob-…`, plus `organization_id` `dev-organization`;
- `idempotency_key` is `dev:<input sha256>`;
- `source` defaults to `{"corpus_id": "dev-corpus", "namespace": "dev",
  "record_key": <input.path>}`, and `corpus_id` follows `source.corpus_id`;
- `configuration` defaults to `{}` and is validated against the manifest
  configuration schema.

`fixtures/invocations/markdown.json` is a normative example.

**Subscription fixtures** (`subscription-fixture.schema.json`) hold the
record's text Parts (and `enriched`), optional plugin `configuration`, and the
evaluations, each with an `expression`, an optional `configuration` and an
optional `expect`ed decision. A file is a subscription fixture when it has a
top-level `evaluations` property. A tool turns one into requests:

- evaluations are numbered `e1`, `e2`, … in order; evaluation *n* stands for
  Subscription `dev-subscription-n` (Version `dev-subscription-version-n`) of
  Saved Query `dev-saved-query-n` (Version `dev-saved-query-version-n`);
- the Record Version ids derive from the first 16 hex digits of the SHA-256
  of the fixture bytes: `dev-record-…`, `dev-version-…`, Corpus `dev-corpus`,
  Organization `dev-organization`;
- evaluations are split into batches of `max_batch_size`; batch *i* has
  invocation id `dev-invocation-…-i` and idempotency key `dev:<sha256>:i`;
- the configuration, every expression and every evaluation configuration are
  validated against the manifest schemas, and Part keys must be unique.

`fixtures/subscriptions/strike.json` is a normative example.

## Try it

```bash
go run ./cmd/quivr plugin inspect contracts/plugins/v0/fixtures/manifests/valid/full.yaml
go run ./cmd/quivr plugin inspect contracts/plugins/v0/fixtures/manifests/valid/subscription.yaml
go run ./cmd/quivr plugin inspect --json contracts/plugins/v0/fixtures/manifests/invalid/reserved-contribution.yaml
```

Exit codes: `0` valid, `1` invalid or incompatible, `2` usage error.

Scaffold and run a Python normalizer with the SDK in [`sdks/python`](../../../sdks/python/README.md):

```bash
quivr plugin init demo && cd demo
python3 -m venv .venv && . .venv/bin/activate && pip install -e <quivr-v2 checkout>/sdks/python
quivr plugin dev --fixture fixtures/sample.json
```

`quivr plugin init alerts --kind subscription` scaffolds an alert rule
instead: a case-insensitive phrase rule with a kind-discriminated
`expression_schema`, a subscription fixture with expected decisions, and tests.

`quivr plugin dev [--fixture <file>] [--watch] [--port <n>] [--startup-timeout <duration>] [<plugin-dir>]`
runs these steps:

1. inspects the manifest;
2. starts `run.command`, waits for `GET /v0/health` and checks that
   `GET /v0/discovery` matches the manifest: digest, id, version, Plugin API
   range and Contributions;
3. with `--fixture`, sends the fixture's request, validates the answer with the
   same output checks as the Contract Runner (engine Manifest rules, response
   size, Blob Parts, declared namespaces), and prints the response on stdout.
   With a subscription fixture, it sends every batch, validates each answer
   with `CheckSubscriptionOutput` and the expected decisions, and prints the
   decisions of all batches.

It exits `0` when every check passes and `1` otherwise. Without `--fixture`,
or with `--watch`, it keeps running and restarts the plugin when a file in the
plugin directory changes; hidden directories, `__pycache__`, virtual
environments and build outputs are ignored.

## Contract Runner

`quivr plugin test [--endpoint <url>] [--report <file>] [--fixture <file>]... [--startup-timeout <duration>] [<plugin-dir>]`
certifies that the engine can safely invoke every Contribution a plugin
declares. It talks
only the public protocol, so it applies to a plugin in any language, and it
judges answers with the engine's own validation, never a copy. It starts
`run.command` like `dev`. With `--endpoint`, it targets a running plugin
instead and still reads `quivr-plugin.yaml` from `<plugin-dir>`, because
discovery carries only the manifest digest.

| Check | Passes when |
| --- | --- |
| `manifest`, `compatibility` | `quivr plugin inspect` accepts the manifest, and this engine and Plugin API version satisfy its ranges. Otherwise the plugin is not started. |
| `health`, `discovery` | `GET /v0/health` answers 200, and discovery matches the manifest (digest, id, version, Plugin API, Contributions). |
| `fixtures` | At least one invocation fixture applies. The runner uses the normative `fixtures/invocations/*.json` whose media type the plugin declares and whose configuration it accepts, plus the plugin's `fixtures/*.json` (or `--fixture`). |
| `invoke` (per fixture) | The answer is a 200 within the declared `timeout_ms` (`deadline_exceeded` otherwise) that passes the output checks above: response schema (unknown fields rejected), engine Manifest rules, `max_parts`, response size, input-Blob-only Blob Parts and declared namespaces. |
| `replay` (per fixture) | The same `idempotency_key` with a new `invocation_id` yields the same Manifest, extensions and language (`nondeterministic_output` names the first difference). |
| `invalid_request` | A non-JSON body, an unknown request field and the normative invalid requests in `fixtures/requests/` are refused with a non-2xx error envelope and `retryable: false`. A request the schema rejects can never succeed, so `retryable: true` is `wrong_error_class`. |

These normalizer checks carry `"contribution": "normalizer"` in the JSON
report. When the manifest declares `subscription`, the runner adds checks with
`"contribution": "subscription"`:

| Check | Passes when |
| --- | --- |
| `fixtures` | At least one of the plugin's own subscription fixtures applies (no normative fixture can know a rule's expression shape), and each builds valid requests. |
| `invoke` (per batch) | The answer is a 200 within `timeout_ms` that passes `CheckSubscriptionOutput` (one decision per evaluation, evidence bounds above) and matches every `expect`ed decision (`unexpected_decision`). |
| `replay` (per batch) | The same `idempotency_key` with a new `invocation_id` yields the same decisions and evidence (`nondeterministic_output`). |
| `batch` (per batch) | The same evaluations sent in reverse order get the same decisions and evidence (`batch_dependent_decision`). Skipped for a single evaluation. |
| `invalid_request` | A non-JSON body, an unknown field and the normative invalid requests in `fixtures/requests/subscription/` are refused with a terminal error envelope. |

The human report goes to stdout; the plugin's own output goes to stderr.
`--report <file>` writes the JSON report described by
`reports/contract-report.schema.json`. Exit codes: `0` certified, `1` not
certified, `2` usage error.

The deliberately broken plugins in `tests/plugin-contract/` show one failure
per rule, for both Contributions. CI certifies both `quivr plugin init`
templates and publishes their reports as the `plugin-contract-report` and
`subscription-plugin-contract-report` workflow artifacts.
