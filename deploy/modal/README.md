# Deploy inference on Modal

## Serve EmbeddingGemma 2

The coordinator deploys [`embeddinggemma.py`](embeddinggemma.py)
and opts api and worker into `QUIVR_DEMO_EMBEDDING=gemma`. The service runs the
text backbone of `google/embeddinggemma-2` on an L4 GPU, falling back to A10G then
L40S when Modal has no L4 capacity, pinned to
`914f7f89142e33e77833254d9c9b90c3cef7303b`. Its image checks that revision's
README for Apache-2.0 before downloading weights. Runtime is offline, uses
bfloat16, suppresses the library's default prompt and returns normalized
768-dimensional float vectors. No Railway embedding image or new engine plugin
is needed; the existing `text.embed` handles documents and search queries.

Only the coordinator performs the commands and live changes below. In a local
Python 3.12 environment, install `modal==1.6.1` and authenticate the Modal CLI.
Create a Modal secret named `quivr-embeddinggemma` containing `EMBED_API_KEY`, a
random ASCII bearer token without whitespace. Use the Modal secret editor or a
private, gitignored dotenv file; never pass the value in a command argument or
print it. The same value goes into Railway's secret variable `EMBED_API_KEY` on
both api and worker. For a private dotenv file, the CLI command is:

```sh
modal secret create quivr-embeddinggemma --from-dotenv /private/path/embedding.env
EMBED_MIN_CONTAINERS=1 EMBED_MAX_CONTAINERS=2 modal deploy deploy/modal/embeddinggemma.py
```

`EMBED_MIN_CONTAINERS` defaults to **0**, allowing the GPU to scale to zero after
60 idle seconds. Use **1** for the demo's interactive queries, with a maximum of
**2** GPU containers initially; each requests 4 CPUs and 8 GiB host memory. These
are deployment settings, so change them by redeploying the app. The CPU gateway
also scales to zero and holds the bearer secret; GPU containers do not receive
it. Modal combines concurrent text calls into batches of up to 32, waiting at
most 20 ms to fill a batch. Real GPU latency and throughput have **not** been
measured by this change: local contract checks use fake inference only. Confirm
live performance as part of coordinator rollout. With zero warm containers,
first requests can exceed the hosted client's 10-second attempt timeout;
bounded retries do not guarantee a cold query succeeds.

Copy the HTTPS origin printed for the `web` function, without a trailing path,
into `EMBED_URL` on both api and worker. Also set `QUIVR_DEMO_EMBEDDING=gemma`.
The entrypoint appends `/v1` as `base_url`, uses bearer authentication, 768
dimensions, query prefix `task: search result | query: ` and document prefix
`title: {headline} | text: `. Packed passages of 512 body tokens, counted with the
pinned tokenizer, batch size 32 and four concurrent provider calls stay within
the service's request limits. The full checkpoint is pinned in the image; the
plugin's bounded `model_revision` uses `914f7f89142e33e7` so model meaning has a
distinct vector-space identity. The token reaches only the hosted sidecar through
its existing credential interface, never the generated config or engine.

Before switching, send authenticated `GET /health` and `POST /v1/embeddings`
requests with `model: google/embeddinggemma-2`, `dimensions: 768` and a prefixed
text. Require a 768-value vector and reject an absent or wrong bearer token with
401. Health checks the gateway without starting the GPU; the embedding request
is the GPU readiness check. A public URL alone grants no access: both routes
require the bearer token. The endpoint accepts strings or batches of 1–32
strings, each at most 32 KiB of UTF-8 (8 MiB per request). It rejects invalid input before GPU work.

### Switch, rebuild and check

1. Save the current plugin plan, registration/configuration and api/worker
   variables. Pause connector polling and ingestion, wait for old pinned work
   to drain, and schedule a search maintenance window. A model switch changes
   the `text.embed` contract; the single sidecar cannot keep both model
   registrations reachable. Old served generations may need the old settings
   until rebuilt. Do not promise uninterrupted search across this switch.
2. Set the Gemma variables identically on api and worker and redeploy both.
   Inspect `GET /v0/admin/plugins/plan` with the operator key: require
   `ingestion-default` on `text.embed`, with the Gemma registration and no
   E5 evaluation routes. Keep core.ingest and TEI for historical E5 work.
3. List every Corpus using `GET /v0/corpora`, following pagination. For each,
   call `POST /v0/corpora/{corpus_id}/rebuilds` with the operator key and body
   `{"idempotency_key":"<unique rebuild key>"}`. Poll
   `GET /v0/operations/{operation_id}` until `succeeded`; inspect diagnostics
   and skipped Versions. A rebuild re-runs segmentation and embedding under
   the selected model and replaces the served generation when prepared.
4. Check `GET /v0/corpora/{corpus_id}/vector-spaces`: compare the Gemma space's
   `coverage.segments` to its own `coverage.total_segments` and verify
   `coverage.versions_covered` for eligible current Versions. Run ordinary
   semantic and hybrid searches with `profile: default`; hits must name the
   Gemma space. Resume ingestion and search after every Corpus passes.

### Roll back to Cohere

In another maintenance window, pause ingestion and drain Gemma pinned work.
Set `QUIVR_DEMO_EMBEDDING=cohere` identically on api and worker, retain the
previous `AZURE_FOUNDRY_ENDPOINT` and `AZURE_FOUNDRY_KEY`, then redeploy both.
Rebuild **every** Corpus again with fresh idempotency keys and verify Cohere
coverage and ordinary searches before reopening. Merely changing the variable
or restoring a saved plan does not convert Gemma vectors to Cohere, and a saved
plan can refuse an unreachable old registration. Keep the Modal app available
until no Gemma work depends on it. The previous `QUIVR_DEMO_HOSTED_EMBED=1`
continues to select Cohere when the new model switch is omitted; an explicit
`gemma` or `cohere` takes precedence. Unknown model-switch values abort startup.

## Serve a TEI-compatible reranker on Modal

Deploy [the reranker app](reranker.py) from the repository root with Python
3.12 and `modal==1.6.1`. Create a Modal secret called `quivr-reranker` with
`RERANKER_API_KEY`, a random printable ASCII bearer token without spaces.
Use the Modal secret editor or a private dotenv file outside the repository.

```sh
modal secret create quivr-reranker --from-dotenv /private/path/reranker.env
modal deploy deploy/modal/reranker.py
```

The app defaults to [Qwen3-Reranker-4B](https://huggingface.co/Qwen/Qwen3-Reranker-4B/tree/22e683669bc0f0bd69640a1354a6d0aebcfeede5)
(Apache-2.0, 4,096 tokens per pair). Set `RERANKER_MODEL=BAAI/bge-reranker-base`
before deployment to select [the sequence classifier](https://huggingface.co/BAAI/bge-reranker-base/tree/2cfc18c9415c912f9d8155881c133215df768a70)
(MIT, 512 tokens per pair). The image verifies the pinned model card license
and bakes safetensors weights for offline inference. GPU capacity falls back
from L4 to A10G to L40S. Set `RERANKER_MIN_CONTAINERS=1` to keep a GPU warm.
The defaults are zero warm containers and a maximum of two, set by
`RERANKER_MAX_CONTAINERS`. Only the CPU gateway receives the bearer secret.

Replace the example origin with the HTTPS URL printed by Modal. Load
`RERANKER_API_KEY` into the shell through your secret manager before testing.

```sh
origin=https://reranker.example.org
printf 'Authorization: Bearer %s\n' "$RERANKER_API_KEY" |
  curl --fail-with-body -sS -H @- "$origin/health"
printf 'Authorization: Bearer %s\n' "$RERANKER_API_KEY" |
  curl --fail-with-body -sS -H @- -H 'Content-Type: application/json' \
    --data '{"query":"Which planet is called the Red Planet?","texts":["Venus has a thick atmosphere.","Mars appears red."],"truncate":true,"raw_score":true}' \
    "$origin/rerank"
```

Both routes return HTTP 401 without a valid token. Health returns the selected
model and revision without starting a GPU. Use the rerank request to verify
inference. Each result includes the original zero-based `index`, `score`,
and unsaturated `raw_score`, ordered by descending margin. The request flag
`raw_score: true` (or TEI's `raw_scores`) puts the margin in `score` too.
Omit it or set it to false for the sigmoid probability. Set `return_text: true`
to include the original text. The gateway accepts 1–128 texts, up to 8 KiB of
UTF-8 per query or text, 2 KiB per optional instruction, and 8 MiB of JSON.
Overlong tokenized pairs return HTTP 400 unless `truncate: true` is set.
Truncation defaults to the right. Qwen formats the instruction with the query
and document, uses its standard web search instruction when omitted, and scores
the yes minus no logits. For a sequence classifier, the instruction is prefixed
to the query and the single relevance logit is used directly.

Configure `newsroom.retrieve` with the full HTTPS `/rerank` URL as
`rerank_endpoint`, `rerank_protocol: tei`, and the same `RERANKER_API_KEY`
through the plugin's secret interface. Its default logit score space uses
the returned margins directly. The combined and API images include
`quivr-newsroom-retrieve` for opt-in registration.
