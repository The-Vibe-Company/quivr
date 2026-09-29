# API walkthrough

A guided tour of the implemented `v0` HTTP API and of the local stack's behaviour.
[`contracts/http/v0/openapi.yaml`](../contracts/http/v0/openapi.yaml) is authoritative
for request and response shapes; this page explains the semantics around them.

## Local stack and API keys

`make dev` builds a single `quivr` binary, starts PostgreSQL, Temporal, SeaweedFS
(S3), Weaviate and TEI through Docker Compose, applies migrations and runs the API
and worker as local processes. It prints the API address and the path of the
generated `config.json`. Ports are dynamic and bound to loopback.

Throwaway keys, settings and logs live in the private `.scratch/quivr-dev-…`
directory. Never publish it: `state.json`, `config.json`, `worker.json` and `s3.json`
contain credentials. The `keys` field of `config.json` maps each Bearer token to an
Organization, a list of actions and a list of Corpora (`*` grants the whole
Organization).

| Command | Effect |
| --- | --- |
| `make down` | Stop the stack, keep development volumes |
| `make reset` | Stop the stack and delete its volumes |
| `make migrate` | Apply versioned migrations to the running stack |
| `make generate` | Regenerate transport bindings after a contract change |
| `GO=/path/to/go make …` | Use a specific Go toolchain |

Local logs are capped at four 1 MiB files per process; Compose services keep three
1 MiB files each. The private `/healthz` and `/readyz` probes use a separate port and
are not part of the public API. No hosted model service or external key is required.
Hot migrations can break running processes during evaluation; restart API and
workers after migrating.

## Corpora

`POST /v0/corpora` with `name` and `idempotency_key` creates a Corpus; `GET /v0/corpora`
and `GET /v0/corpora/{corpus_id}` read them. Replaying the same request under the same
key returns the same Corpus; changing the request under that key is a conflict.
Creating a Corpus requires `corpora:write` and the `*` Corpus scope, so a key bound to
existing Corpora cannot create new ones. Explicit retrieval field mappings are
validated and stored; an uninstalled `plugin_profile` is refused.

## Ingesting text

`POST /v0/records` takes an `idempotency_key`, a `source` (`corpus_id`, `namespace`,
`record_key`) and `content` (`{"kind": "text", "text": "…"}`). The API answers `202`
once the Receipt and the work to dispatch are committed, even if Temporal or S3 are
down. Follow the `Location` header to the Receipt, then read
`/v0/records/{record_id}/versions/{version_id}` for the Manifest and its text.
Permissions are `content:write` and `content:read`, limited to authorized Corpora.

- A Receipt resolves to `created`, `duplicate`, `withdrawal_applied` or `conflict`. It
  never becomes "failed" because of an infrastructure outage; work is retried.
- Without a `source_revision`, the canonical Manifest digest identifies the Version.
  Reusing a revision with different content keeps history and reports a conflict.
- `source_position` is an optional decimal string of 1 to 1000 digits; leading zeros
  are normalized.
- A single command is limited to 1 MiB.
- Structured Manifests (`kind: "manifest"`), extensions and relations are accepted
  and preserved; see [ingestion contracts](quivr-v2-ingestion-contracts.md).
- `POST /v0/records/withdrawals` withdraws a Record; withdrawn Records are fenced so a
  late or stale submission cannot resurrect them.

### Batches

`POST /v0/records/batch` with `{"items": [...]}` accepts up to 100 commands and
10 MiB, each entry at most 1 MiB raw; upload and processing share the request's 5 s
deadline. The `200` response lists, in order (`index`), either a Receipt or an error
per entry, with the same codes as a single submission; one invalid entry does not
block the others. A malformed envelope is refused as a whole (400, 413 or 422)
without any Receipt. Every entry keeps its own key: after a lost or interrupted
response, resending the same keys, as a batch or through `POST /v0/records`, returns
the same Receipts without duplicates.

### Uploads

`POST /v0/uploads` with `size_bytes`, `sha256` and `media_type` (1 GiB maximum)
returns `201` with an `upload_id`, a presigned PUT URL, its mandatory signed headers
and `expires_at` (15 minutes). The client uploads the bytes, then calls
`POST /v0/uploads/{upload_id}/confirm`, which re-reads the object to check size and
checksum and exposes a stable `blob_id` once `verified` (read back through
`GET /v0/uploads/{upload_id}` and `GET /v0/blobs/{blob_id}`). A missing, altered or
cross-Organization transfer is rejected (`rejected`, or 404 for another tenant;
holding an ID is not access).

Then submit `POST /v0/records` with
`content: {"kind": "blob", "blob_id": …, "media_type": "text/plain"}`. The verified
text follows the same Receipt/Version path, keeps its original bytes, and the source
Blob is recorded in `provenance.source_blob_ids`. `text/*` media are accepted, and
so are media types that the installation routes to an external normalizer (next
section). Other media types and unverified references return `422 unverified_blob`.
Permissions are `blobs:write` (create, confirm) and `blobs:read` (read session and
Blob). Expired and absent sessions stay readable in their terminal state. There is no
orphan sweep or retention yet.

### External normalizers

An installation can pin one external plugin in its startup configuration
(`QUIVR_CONFIG`) and route Blob media types to that plugin's normalizer:

```json
"plugin": {
  "manifest": "/etc/quivr/plugins/markdown/quivr-plugin.yaml",
  "endpoint": "http://127.0.0.1:9900",
  "configuration": {"max_sections": 32},
  "routes": [{"media_type": "text/markdown", "mode": "required"}]
}
```

- **Startup checks.** `quivr api` and `quivr worker` validate the pin before
  starting and refuse to start with the list of issues if any check fails. They
  check:
  - the manifest schema and its engine and Plugin API ranges;
  - `configuration` against the manifest's configuration schema;
  - that the endpoint is an http(s) URL;
  - that every route names a media type the normalizer declares, and that no media
    type is routed twice;
  - that an `optional` route names a `text/*` media type.

  A route's mode is `required` (the default) or `optional`; see failure handling
  below. The plugin itself is never contacted at startup, so an unreachable plugin
  does not affect `/healthz` or `/readyz`.
- **Acceptance.** A routed Blob is accepted by reference without being read.
  The Version identity is the digest of the submitted input: the verified Blob, its
  checksum and media type, plus the submitted extensions. Replaying the same input
  converges on the same Version.
- **Invocation.** In the worker, before publication, the engine:
  - checks that the plugin's discovery document matches the pinned manifest digest;
  - sends the invocation context with a short-lived signed GET URL to the input Blob,
    never its bytes;
  - waits at most the declared timeout, capped at 2 minutes;
  - reads at most the declared `max_response_bytes`, capped at 16 MiB.
- **Output checks.** The output goes through the same validation as a
  `kind: "manifest"` submission, plus the declared `max_parts`. The stored Manifest
  must fit in 2 MiB, like any canonical object. Blob Parts may
  reference only the input Blob. Extensions, top-level and on Parts, must use a
  namespace and schema version the plugin declares, with valid data
  (`undeclared_namespace`, `undeclared_schema_version`, `invalid_extension`);
  otherwise nothing is recorded and the Receipt is blocked with
  `normalizer_invalid_output`.
- **Plugin-owned namespaces.** At startup the pinned plugin's declared namespaces are
  registered beside the built-in ones; a namespace not prefixed by the plugin id or
  clashing with a built-in one refuses startup. Valid top-level extensions are
  published on the Version beside the submitted ones (Part extensions stay in the
  Manifest), and retrieval mappings may point at `/extensions/{namespace}/...`. A
  client submission, single or batch, that writes a plugin-owned namespace returns
  `422 extension_namespace_owned`.
- **Publication.** The validated Manifest is stored once per Version, so re-running
  the step converges on it. It is then published and searchable like any Manifest.
  Rebuilds and retrieval generations read the stored Manifest and never call the
  plugin again.
- **Provenance.** `GET /v0/records/{id}/versions/{version_id}` shows
  `provenance.normalization`: `plugin_id`, `plugin_version`, `plugin_api`,
  `contribution`, `invocation_id`, `idempotency_key` and `input_sha256`. `producer`
  keeps naming the acquirer, and `source_blob_ids` keeps the input Blob. Clients
  cannot submit `normalization`.

**Failure handling.** A misbehaving or absent plugin never publishes partial output
and never blocks the API, text ingestion or search.

- **Unavailable plugin** (connection failure, a 5xx answer without an error envelope,
  or a discovery document that does not match the pinned manifest): the step retries
  with backoff, without limit, and never quarantines. The Receipt stays `pending` with
  the retryable diagnostic `plugin_unavailable`, and the Version is published once the
  plugin is back.
- **Retryable plugin error or timeout:** retried until the manifest's
  `retry.max_attempts` (capped at 5) invocations have failed this way, then
  quarantined with `normalizer_retries_exhausted` or `normalizer_timeout`.
- **Terminal plugin error or invalid output** (schema, malformed or duplicate Parts, a
  Blob Part that is not the input Blob, an undeclared extension namespace, too many
  Parts, a response over the size bound): quarantined at once with
  `normalizer_failed` or `normalizer_invalid_output`.
- **Quarantine** publishes the Version with only its submitted input Blob Part, never
  the plugin's output. Its availability is `quarantined`, and it is announced by a
  `record.quarantined` change event. The Version read lists the reason in
  `diagnostics`:

  ```json
  {"code": "normalizer_invalid_output", "message": "invalid normalizer output: …",
   "retryable": false, "plugin": "acme.markdown", "contribution": "normalizer",
   "invocation_id": "inv_…"}
  ```

  The input reference, the reason and the invocation are kept for a later
  reprocessing, which is not available yet.
- **Optional routes** (`"mode": "optional"`, `text/*` only): instead of quarantining,
  the Version is published through the built-in text path and stays searchable. Its
  `provenance.normalization` names the failed invocation and carries
  `fallback: {code, message}`, and the failure is listed in `diagnostics`. An
  unavailable plugin is still retried rather than bypassed.
- **Divergent output** for the same idempotency key (`normalizer_conflict`): the first
  recorded output is kept and published. The conflict is listed in the Version's
  `diagnostics` with the divergent invocation.
- **Skipped normalization.** A withdrawn Record, or a Version superseded by a newer
  accepted revision before it was normalized, never calls the plugin. A withdrawal
  resolves the Receipt as `conflict`, and a superseded Version is quarantined with
  `normalization_superseded`. If a route is removed while work is pending, a `text/*`
  Blob takes the built-in text path; any other media type is quarantined with
  `normalizer_unrouted`.

`make dev` pins the reference `pdf-text` plugin for `application/pdf`;
`QUIVR_NORMALIZER=template` pins the `quivr plugin init` template for
`text/markdown` instead, and `none` pins nothing
([local harness](quivr-v2-local-harness.md#plugin-substitution-and-handoff)). To
write your own, follow [Write a normalizer](plugins/write-a-normalizer.md).

### PDF documents

With the [`pdf-text`](../plugins/pdf-text/README.md) plugin pinned for
`application/pdf` (the `make dev` default), a PDF is uploaded through an Upload
Session and ingested with `content: {"kind": "blob", "blob_id": …, "media_type":
"application/pdf"}`, like any Blob. Runnable commands are in
[Write a normalizer, step 7](plugins/write-a-normalizer.md#7-ingest-a-blob).

- The published Manifest has one `body` Part `page-<n>` per page with text, and a
  `source` Blob Part that references the PDF. A search hit names its page
  through `part_key`, for example `page-2`.
- The Version shows `provenance.normalization.plugin_id: "pdf-text"` with its
  version, and `extensions["pdf-text.document"]` holds `page_count` and
  `text_pages`.
- Pages without text, such as scans, are skipped and listed in an `empty_pages`
  warning. There is no OCR.
- An encrypted or damaged PDF is quarantined with a `normalizer_failed`
  diagnostic that names `pdf-text`.

Limits and configuration are in the [plugin README](../plugins/pdf-text/README.md#limits).

## Processing: segmentation and embeddings

Text Parts go through `quivr.normalized-text.token-windows.v1`, using the pinned E5
tokenizer and Hugging Face Tokenizers 0.23.2: 384-token windows, 48 tokens of overlap
(up to 56 to step back to a word start), preferring paragraph, line, sentence and
then space boundaries. Excerpts are exact slices of the Part; no first line is turned
into a title. Unicode and UTF-8 offsets, forced cuts and checksums are persisted. An
explicit title Part next to body Parts is supported; its model view is capped at 64
tokens while its lexical text stays complete.

Preparation installs the verified wheel and downloads the pinned E5 snapshot (weights
and ONNX export, about 940 MB, plus tokenizer and configuration). Token offsets and
counts come from a local, offline Python subprocess; the recipe and its checks are in
Go. See [tokenizer provenance](../third_party/tokenizer/NOTICE.md).

Processing limits: 256 KiB of UTF-8 per processing input, 64 Parts, 256 segments,
4,096 code points per excerpt, 512 tokens per assembled model input, and 2 MiB of text
/ 4 MiB of JSON per assembled batch. Exceeding a limit, or a NUL character, keeps the
accepted content readable but blocks processing with `segmentation_limit` and a
`quarantined` availability; no partial result is published as successful.

After a verified publication to Weaviate, the lexical coverage, the promotion of the
desired revision and its event are committed atomically. A failure leaves the
previous Version current and the new one in recovery. A separate activity then
embeds segments with local E5. The float32 little-endian payloads (1,536 bytes) and
their immutable manifests are verified in S3, then referenced in PostgreSQL before
projection. Retries reuse artifacts; divergent output for the same derivation blocks
enrichment with `derivation_conflict` without removing lexical coverage.

TEI 1.9.3 uses mean pooling, L2 normalization, 384 dimensions and `float32`, with
`passage: …` and `query: …` prefixes and no truncation. The image and the seven
snapshot files are pinned; TEI mounts its cache read-only on an internal Docker
network without outbound access. No hosted provider and no fake vectors are used. A
TEI outage keeps lexical search available and returns 503 for semantic and hybrid
search. See [E5 provenance](../third_party/e5/NOTICE.md).

## Search

```http
POST /v0/search
Authorization: Bearer <key with content:read and search:query>
Content-Type: application/json

{"query":"eclipse","corpus_ids":["<corpus_id>"],"mode":"lexical","profile":"balanced","limit":10}
```

- Modes are `lexical`, `semantic` and `hybrid`. Defaults: `hybrid`, `balanced`, 10
  results; 50 maximum. Other profiles return 422.
- The resolved profile `balanced.e5-token-windows.v1` accepts non-empty queries of at
  most 256 tokens (8,192 code points on the wire), without truncation. CRLF/CR become
  LF and surrounding whitespace is trimmed; case, accents and language are kept.
- Hybrid uses alpha 0.5, relative score fusion and title/body weights of 2/1.
- Every requested Corpus must be authorized. PostgreSQL selects the logical
  generation and physical routing; rehydration re-reads the S3 bytes, validates
  excerpts and rechecks access, current Version, quarantine and withdrawal.
- Excerpt coordinates are Unicode code points. No raw score, physical collection name
  or vector is exposed. A dependency outage returns 503, never an empty success.

`POST /v0/corpora/{corpus_id}/rebuilds` starts an asynchronous projection rebuild from
durable artifacts and returns an Operation readable at `/v0/operations/{operation_id}`.
`POST /v0/operations/{operation_id}/cancel` cancels queued work at once; running work
moves to `cancel_requested` and settles as canceled at its next fenced step, so a
partial target never activates. `POST /v0/operations/{operation_id}/rerun` requires a
terminal source (otherwise `409 operation_not_terminal`) and creates a new linked
Operation. Both take an `idempotency_key` body, return 202 and need `operations:write`;
rerun also rechecks `projections:rebuild` and the Corpus scope. Only projection rebuild
Operations are supported.

## Changes, catalog and monitoring

- `GET /v0/changes` (polling) and `GET /v0/changes/stream` (resumable SSE) expose
  committed changes per Organization behind opaque cursors.
- `GET /v0/records?corpus_id=…` traverses the authorized Record catalog; when a change
  cursor expires, use it as the `resync_url` to resynchronize.
- `/v0/saved-queries` and `/v0/subscriptions` create pinned, versioned Saved Queries and
  activate, disable or re-enable Subscriptions. Re-enabling resumes evaluation from
  that point, with no backfill of the pause.
- Edit a Saved Query or a Subscription by committing a new Version:
  `POST /v0/saved-queries/{id}/versions` (a new definition; Subscriptions keep the
  Version they pin) and `POST /v0/subscriptions/{id}/versions` (pin the Saved Query's
  current Version, an evaluator and a destination). A new Subscription Version judges
  only changes committed after it; earlier Matches keep their Versions, all readable
  through `…/versions/{version_id}`. `POST /v0/subscriptions/{id}/delete` stops
  evaluation and deliveries for good while its history stays readable;
  `POST /v0/saved-queries/{id}/delete` works once no Subscription uses the Saved Query.
- Give a Subscription an optional `owner` at creation, an opaque reference to one of
  your application's end users (for example `"owner": "user-123"`); without it the
  Subscription is global to the Organization. The owner never changes and is echoed on
  the Subscription, its Versions, its Matches, its webhooks (`references.owner`) and its
  change-feed events (`monitoring.owner`), so you can route each alert to its user.
  `GET /v0/subscriptions?owner=user-123` (or `?owner=none` for global ones) pages the
  active Subscriptions your key can see; per-user limits or quotas belong in your
  application, which can count with this listing.
- Enabled Subscriptions evaluate Record Versions that become searchable or enriched
  after their activation boundary. Each positive evaluation creates one unique Match
  with a pending Delivery, an immutable `match.created` notice and a change-feed event. Read them through
  `GET /v0/matches`, `GET /v0/matches/{match_id}` and `GET /v0/deliveries/{delivery_id}`.
  The evaluator is the `subscription` Contribution of a plugin pinned at startup
  (`plugins` in the configuration), named by `plugin_id` and `version`; a Saved Query
  expression or evaluator configuration its schemas refuse is 422 `invalid_expression`
  or `invalid_subscription_configuration`. A plugin outage delays alerts, never skips them.
  The first-party keyword alerts plugin (`{"plugin_id": "alerts", "version": "0.1.0"}`,
  pinned in the local stack) takes an expression such as
  `{"kind": "keywords", "match": {"all": [{"term": "Airbus"}, {"not": {"term": "sport"}}]}}`.
  Its Match evidence names the matched terms and their Parts
  ([Keyword alerts](keyword-alerts.md)).
- The worker POSTs each notice to the Subscription's destination, signed with
  Standard Webhooks headers (`webhook-id` is the notice `event_id`). A 2xx response
  marks the Delivery `delivered`. Network errors, timeouts, 408, 429 and 5xx are
  retried with jittered exponential backoff (1 s doubling to 5 min, or a valid
  `Retry-After` on 429/503) within a 24-hour window; other responses, or a failure
  after the window, mark it `exhausted`. Retries resend the same `event_id` and body
  bytes, so receivers deduplicate on `webhook-id`. `GET /v0/deliveries/{delivery_id}`
  shows `state`, `admission`, `last_error` and, while a retry is scheduled,
  `next_attempt_at`; `GET /v0/deliveries/{delivery_id}/attempts` pages the append-only
  attempt history. Disabling the Subscription stops further attempts.
- When an alerted Record changes, the Subscription receives a linked follow-up notice
  (reference-only, like `match.created`, with the same feed and webhook identity):
  - a correction that still matches creates a new Match with `previous_match_id` and a
    `match.corrected` notice carrying every `match.created` reference plus
    `previous_match_id`, so it can be acted on alone;
  - a correction that no longer matches sends `match.no_longer_matches`, whose
    `match_id` is the earlier Match and `record_version_id` the correction; no Match
    is created. An evaluator failure never sends it;
  - a withdrawal sends `match.withdrawn` for the latest Match. Search stops returning
    the Record at once; the notice follows asynchronously and is delivered even though
    the Record is withdrawn. A Subscription disabled at that point gets the notice too.
    It stays pending with no attempt until `POST /v0/subscriptions/{id}/enable`, then
    it is delivered: its delivery window starts at the re-enable.

  A `match.created` or `match.corrected` not yet delivered when a later correction
  notice exists for the same Subscription and Record is not sent any more, nor is a
  `match.no_longer_matches` once a later `match.corrected` shows the Record matches
  again: its Delivery stays `pending` with admission `superseded`. The earlier Match
  stays readable through `GET /v0/matches/{match_id}`.

## Connectors

`/v0/connectors` configures Connector Instances that pull content from an external
source into one Corpus and Source Namespace on a schedule. Collected items go through
the same ingestion path as `POST /v0/records`. Credentials are write-only and never
returned. Kinds that have not shipped yet are refused with
`422 unsupported_connector_kind`. A deployment without `credential_key` refuses
credential deposits and rotations with `503 credentials_unavailable`; instances without
a credential, such as public RSS feeds, work normally. See the [operator guide](connectors/README.md).

The `x_list` kind polls one X list with a deposited bearer token:

```http
POST /v0/connectors
Authorization: Bearer <key with connectors:write>
Content-Type: application/json

{"idempotency_key":"x-watchlist-1","corpus_id":"<corpus_id>","source_namespace":"x-watchlist","kind":"x_list","config":{"list_id":"1234567890123456789"},"credential":{"secret":{"bearer_token":"<X app bearer token>"}}}
```

Each post becomes a Record keyed by its original post id; an edit is a correction,
and a post deleted or made protected on X within the recheck window is withdrawn.
`GET /v0/connectors/{id}` shows `health.usage` (estimated billed reads per UTC day)
and `health.diagnostics` (recheck coverage). See the [X guide](connectors/x.md).

Clients discover what they can configure with `GET /v0/connector-kinds`
(`connectors:read`). It lists the kinds enabled on this deployment, each with its
`config_schema` and `credential_schema` (JSON Schema, with `title`, `description`,
`examples` and `writeOnly` annotations), whether it takes a credential (`none`,
`optional`, `required`) and its default interval. `credential_deposits` says whether
this deployment accepts credentials at all, and `min_interval_seconds` is its
interval floor.

```http
PUT /v0/connectors/{connector_id}/schedule
Authorization: Bearer <key with connectors:write>
Content-Type: application/json

{"interval_seconds":600}
```

Setting the current value changes nothing. A new value commits
`connector.schedule_changed`. A shorter interval brings the next run forward; a
longer one applies after the run already scheduled. A disabled instance is
`409 connector_disabled`.

A `422` on connector commands carries `field`, a JSON Pointer to the rejected
member of your request, for example `{"code":"invalid_config","field":"/config/url"}`
or `/credential/secret/token`. For a choice between credential formats it points at
`/credential/secret`.

## Verification

`make verify` regenerates and compares transports, checks the examples in all three
languages, runs Go and Python unit tests, then drives the HTTP journeys against an
isolated real stack: restart, isolation, pagination and concurrency; ingestion,
duplicates, revision conflicts and recovery after stopping Temporal/S3 and
interrupting the worker; the three search modes, long texts, Unicode excerpts,
permissions, limits and Weaviate and model outages. Reports stay in
`.scratch/quivr-verify-…` (including `embedding-outage.json`,
`embedding-provenance.json` and `relevance-report.json`) after processes, containers
and volumes are removed. The first run downloads pinned dependencies and images.

## Retrieval measurement

`make measure` (Linux x86_64) runs the frozen
[workload](../tests/measurement/workload-v1.json) against an isolated real stack and
writes `measurement.json` and `measurement.md` under `.scratch/quivr-measure-*`:
lexical, semantic and hybrid MRR and Recall, p50/p95 latency per load condition against
a p95 < 1 s target, cold/warm phase timings, resource peaks and pins. It is not part of
`make verify`; the non-required `Retrieval baseline` workflow runs it on manual
dispatch (`gh workflow run measure.yml --ref <branch>`). Recorded results are in
[`docs/evidence/`](evidence/).

Separately, `make verify` scores an original CC0 fixture of 24 French and English
queries, with explicit title/body Parts, through the real adapters in a collection
reserved for the fixture (to isolate BM25 statistics) and writes
`relevance-report.json`. Last recorded result:

| Mode | MRR@10 | Recall@3 |
| --- | ---: | ---: |
| Lexical | 0.6993 | 0.8333 |
| Semantic | 0.9583 | 1.0000 |
| Hybrid | 0.7969 | 0.9583 |

Hybrid currently trails semantic on this fixture. These small synthetic judgements do
not measure production relevance or latency.
