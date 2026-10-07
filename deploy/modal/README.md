# Serve EmbeddingGemma 2 on Modal

The coordinator deploys [`embeddinggemma.py`](embeddinggemma.py)
and opts api and worker into `QUIVR_DEMO_EMBEDDING=gemma`. The service runs the
text backbone of `google/embeddinggemma-2` on an L4, pinned to
`914f7f89142e33e77833254d9c9b90c3cef7303b`. Its image checks that revision's
README for Apache-2.0 before downloading weights. Runtime is offline, uses
bfloat16, suppresses the library's default prompt and returns normalized
768-dimensional float vectors. No Railway embedding image or new engine plugin
is needed; the existing `hosted.embed` handles documents and search queries.

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
**2** L4 containers initially; each requests 4 CPUs and 8 GiB host memory. These
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

## Switch, rebuild and check

1. Save the current plugin plan, registration/configuration and api/worker
   variables. Pause connector polling and ingestion, wait for old pinned work
   to drain, and schedule a search maintenance window. A model switch changes
   the `hosted.embed` contract; the single sidecar cannot keep both model
   registrations reachable. Old served generations may need the old settings
   until rebuilt. Do not promise uninterrupted search across this switch.
2. Set the Gemma variables identically on api and worker and redeploy both.
   Inspect `GET /v0/admin/plugins/plan` with the operator key: require
   `ingestion-default` on `hosted.embed`, with the Gemma registration and no
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

## Roll back to Cohere

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
