# hosted.embed

Reference for operators choosing a text embedding model. This optional Go
plugin cuts text and embeds it through OpenAI-compatible or Cohere v2 HTTP
APIs. It declares one configured vector space on Plugin API 0.13.0. The
engine's evaluation, backfill, promotion and rollback operations use that
space normally. Keep `core.ingest` pinned as the rollback target when
installing this separate plugin.

## Configuration examples

These are illustrative provider configurations; replace the endpoint and
model with the ones your server serves. Generating a manifest makes no HTTP
request. The repository's checks use a shared fake and never call a paid API.

Azure AI Foundry, Cohere v2 (`examples/foundry-cohere.json`):

```json
{"format":"cohere","base_url":"https://resource.example.org/providers/cohere/v2",
 "auth":"api-key","model":"Cohere-Embed-V5-Pro","dimensions":1024,
 "max_tokens_per_segment":2048,"max_batch_tokens":32768}
```

The plugin appends `/embed`. Cohere's own API uses
`https://api.cohere.com/v2` with `auth: "bearer"`. Document requests use
`input_type: "search_document"`; queries use `"search_query"`. It requests
`embedding_types: ["float"]` and reads `embeddings.float`.

Azure AI Foundry, OpenAI format (`examples/foundry-openai.json`):

```json
{"format":"openai","base_url":"https://resource.example.org/openai/v1",
 "auth":"api-key","model":"text-embedding-3-large","dimensions":3072}
```

OpenAI (`examples/openai.json`):

```json
{"format":"openai","base_url":"https://api.openai.com/v1",
 "auth":"bearer","model":"text-embedding-3-large","dimensions":3072}
```

Local TEI (`examples/tei.json`):

```json
{"format":"openai","base_url":"http://127.0.0.1:8080/v1","auth":"none",
 "model":"intfloat/multilingual-e5-small","dimensions":384,
 "send_dimensions":false,"query_prefix":"query: ","document_prefix":"passage: "}
```

The OpenAI format appends `/embeddings`, sends `model` and `input`, and reads
`data` by its `index`. `dimensions` is required to declare the space. Set
`send_dimensions: false` for servers using their fixed output dimension;
returned vectors must still match the declared size. Cohere normally receives
`output_dimension` instead. For an instruction model, use a `query_prefix`
such as `Instruct: Find relevant passages.\nQuery: ` and its document template.

## Optional local CPU queries

An OpenAI-format deployment can run a pinned local text encoder beside the API
and set `QUIVR_HOSTED_QUERY_URL=http://127.0.0.1:9995/v1` in the **plugin process**.
Leave the variable unset on workers. Query requests then use that loopback
endpoint without the remote bearer key; documents still use `base_url` and its
existing authentication. A local outage returns a bounded query error.

The local `/health` response must advertise `status: "ok"`, the configured
`model`, `model_revision` and `dimensions`, and a full 40-character hexadecimal
`source_revision` starting with `model_revision` (at least 16 characters).
The plugin refuses mismatches, redirects, non-loopback addresses and Cohere
format. The encoder receives the configured prefix once and must not add its
own prompt. Its OpenAI text interface can accept document-prefixed input too;
this option routes only queries.

This process setting leaves the manifest, installed configuration and vector
space identity unchanged. Before enabling it, compare at least 200 distinct
queries with the remote model: cosine similarity must be at least 0.999 and
ordered evaluation top-10 results must match. Measure query latency under your
CPU allocation and concurrent traffic. The
[Railway instructions](../../deploy/railway/README.md#optional-cpu-query-encoding)
cover the offline image, resource settings, parity CLI and rollback.

## Generate and pin a package

Run from the repository root. Build a persistent executable before generating
its manifest; the manifest's `run.command` points to that executable. Choose
an example and edit its JSON offline. Do not put a key in it.

```sh
mkdir -p .scratch/hosted-embed/package
(cd plugins/hosted-embed && go build -o ../../.scratch/hosted-embed/hosted-embed .)
cp plugins/hosted-embed/examples/tei.json .scratch/hosted-embed/configuration.json
.scratch/hosted-embed/hosted-embed configure .scratch/hosted-embed/configuration.json \
  > .scratch/hosted-embed/package/quivr-plugin.yaml
```

For authenticated providers, inject `AZURE_FOUNDRY_KEY` from your secret
manager into the **plugin process** environment. It is a declared plugin
secret, required for `bearer` and `api-key`; `none` sends no credential.
`AZURE_FOUNDRY_KEY` and `AZURE_FOUNDRY_ENDPOINT` are example environment variable
names. The endpoint is configuration: copy its URL plus the format's base path
into `base_url` before generating the manifest; do not declare it as a secret. The
plugin refuses redirects and never logs keys, provider bodies or input text.

Run the executable with `QUIVR_PLUGIN_MANIFEST` pointing to the generated
manifest, and `QUIVR_PLUGIN_HOST` / `QUIVR_PLUGIN_PORT` for its HTTP address.
Illustrative engine pin, in each API, worker and migrate configuration:

```json
{"ingestion":{"default":"hosted.embed"},
 "plugins":[{"manifest":"/opt/hosted-embed/quivr-plugin.yaml",
             "endpoint":"http://hosted-embed:8081",
             "configuration":{"format":"openai","base_url":"http://tei:80/v1",
                              "auth":"none","model":"intfloat/multilingual-e5-small",
                              "dimensions":384,"send_dimensions":false,
                              "query_prefix":"query: ","document_prefix":"passage: "},
             "spaces":{"<space id from the generated manifest>":"served"}}]}
```

Generate that pin's manifest from **its exact configuration**, including the
endpoint. The generated schema binds settings to the manifest, so an invocation
cannot select a different model behind the same space. For an evaluation
installation beside your existing ingestion plugin, keep this owner's single
space role `served` and list `hosted.embed` under `ingestion.evaluation` for the
source media types you want to measure. Leave your existing served routing in
place. Evaluation is the additional owner's role; each owner still declares a
primary served space. To run several configurations together, give each a distinct
`plugin_id`, such as `hosted.embed.pro` and `hosted.embed.fast`, before generating
its manifest. Pin every package and list those IDs in `ingestion.evaluation`.
The generated space belongs to that ID. See
[ingestion routing](https://docs.quivr.thevibecompany.co/reference/configuration#ingestion-routing)
and [backfill](https://docs.quivr.thevibecompany.co/plugins/backfill-a-vector-space).

Each changed configuration is a new immutable registration: increment
`plugin_version` (default `1.0.0`), regenerate and certify its package, then
install it. The space id includes the model, dimensions and a hash of the wire
format, metric, model revision and input templates. Changing any of those
creates a new space. Set `model_revision` when a deployment name starts serving
new weights; the plugin cannot detect a provider changing weights behind a
stable name. Changing batching or timeouts preserves the vector space. Existing
Corpora need a rebuild or backfill before they carry a newly configured space.

## Limits and defaults

| Setting | Default | Meaning |
| --- | --- | --- |
| `plugin_id` | `hosted.embed` | Independent owner ID, lowercase plugin identifier, at most 40 characters |
| `metric` | `cosine` | `cosine`, `dot`, or `l2` |
| `model_revision` | `"1"` | Space version, 1–32 letters, numbers, dots, underscores or hyphens |
| `query_prefix`, `document_prefix` | empty | Text prepended before embedding |
| `query_input_type`, `document_input_type` | `search_query`, `search_document` | Cohere retrieval modes |
| `send_dimensions` | `true` | Send dimensions to the provider; always validate returned size |
| `max_tokens_per_segment` | `512` | Maximum estimated model input tokens, 8–32768 |
| `overlap` | `48` | Overlapping source bytes, always at code point boundaries |
| `batch_size` | `16` | Most document inputs per provider request, across Versions, 1–32 |
| `batch_wait_ms` | `25` | Document collection window, 0–100 ms and less than `call_budget_ms`; 0 disables cross-Version batching |
| `max_batch_tokens` | `8192` | Maximum summed input estimate per request; at least the segment limit |
| `request_timeout_ms` | `4000` | Per-request timeout, 100–10000 ms |
| `call_budget_ms` | `30000` | Document invocation budget, 100–90000 ms |
| `max_concurrent_requests` | `4` | Provider requests in flight per plugin process, shared by document and query calls, 1–32 |
| `max_retries` | `2` | Retries after the first attempt on 429 or 5xx, 0–5 |
| `usd_per_million_tokens` | absent | Optional operator-supplied price for backfill estimates |

Concurrent document calls in the same Organization and plugin process share a provider batch.
Each batch respects `batch_size` and `max_batch_tokens`. A batch can keep
collecting while waiting for provider admission; the configured window is
additional collection time, not a bound on provider queueing. Query encoding
bypasses collection and retains its separate retrieval mode. The queue admits
at most `min(256, max_concurrent_requests × batch_size)` document subrequests;
additional callers wait within their invocation deadline. Cancellation drops
inputs that have not reached the provider and does not cancel siblings. A
provider input refusal splits a shared batch to isolate the refused Version;
healthy Versions continue independently. HTTP 413 is a size refusal; HTTP 400
or 422 needs a structured `error.param` targeting `input` or `texts`, or a
recognized input-validation `error.code`. Unknown validation errors and shared
authentication or endpoint failures are not split by Version. Retryable
failures keep the configured retry policy.

Usage logs report `input_count` per provider request. A shared request lists
`invocation_ids`; a single invocation retains `invocation_id`. `input_tokens`
counts the whole provider request once, including retry attempts, rather than
attributing its full usage to every Version. No source text or credentials enter
these logs. Batching preserves vector-space identity and resume-cache keys.

A 429 response shares its `Retry-After` delay across subsequent calls in the
plugin process, including calls from other documents and query encoding.
Already outstanding requests may finish. Without `Retry-After`, the bounded
retry backoff supplies the shared delay. This cap and the engine's evaluation
and backfill concurrency limits apply per process; tune all of them to your
provider's allowance.

Windows prefer paragraph, line and sentence endings in their latter half.
Offsets refer to Unicode code points in the original body Part; if there is no
nonempty body, the title is segmented instead. No tokenizer or model download
is needed. One UTF-8 byte counts as one estimated token, plus eight reserved
special tokens and the full prefix. This deliberately underfills byte/subword
models. Configure the limit below the provider's actual maximum, and raise the
reserve in a future plugin version for models with more special tokens. Queries
over this bound are refused, without silent truncation.

A Version may have at most 64 Parts, one title, 256 KiB of text and 256 windows.
Invalid text or oversized content is terminal. Provider 4xx responses other
than 429 are terminal; transport failures, malformed vectors, 429 and 5xx are
retryable. `Retry-After` seconds and HTTP dates take precedence over exponential
delay. If its delay exceeds the invocation deadline, the call returns control
to the engine without starting another request.

Completed document batches stay in a per-organization, per-process LRU cache
(up to 4096 vectors or 32 MiB of vector data). A retryable failure returns
`embedding_incomplete`; retrying on that process reuses its completed batches.
A process restart, another replica or eviction re-embeds missing inputs.

## Usage and certification

Every provider attempt emits one JSON log record with
`event: "hosted_embedding_usage"`, `invocation_id` (or `invocation_ids` for a
shared request), `space`, `mode`, `attempt`, HTTP `status` (0 for a transport
failure), `input_count`, `input_tokens` and `estimated`.
Successful OpenAI calls use `usage.prompt_tokens`; Cohere uses
`meta.billed_units.input_tokens`. Without provider usage, or on a failed
attempt, the count is the conservative input estimate. Sum these records by
space and mode to account for document and query calls, including retries.
Failed-attempt estimates make this an upper bound on billed input when a
provider rejected the request before processing. Cached document vectors
produce no provider call or token charge. API 0.13 has no ingestion response
usage field; this accounting interface is the plugin's structured log.

`make check` certifies generated packages in both formats against the shared
fake. Reports are `.scratch/plugin-sdk/hosted-embed/hosted-embed-*-contract-report.json`
and the CI artifact `hosted-embedding-contract-reports`.
`make verify part=plugins` pins both formats, ingests text and finds it by a
sentence it contains. These checks make no live paid call.
