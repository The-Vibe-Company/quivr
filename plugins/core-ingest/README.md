# core.ingest

The first-party ingestion plugin: it cuts every Record Version into windows of
tokens and embeds each window with `intfloat/multilingual-e5-small`. The engine
did this itself until THE-777; it now segments and embeds nothing, and the API
and worker refuse to start without an ingestion plugin. Every stack pins this
one unless another ingestion plugin is pinned (`scripts/connector_plugin.py`
`FIRST_PARTY`, Railway `CONNECTORS` in `deploy/railway/core-entrypoint.py`).

## What it does

- **Segments.** Title and body text Parts only. Each body Part is cut into
  windows of at most 384 tokens of the pinned tokenizer that prefer paragraph,
  line and sentence ends, overlapping by 48 tokens. Recipe values are in
  `profile.json`. A request with no space answers the segments alone (Plugin
  API 0.8), which is how a Version becomes searchable by keyword while the
  embedding service is down.
- **Embeds** each window as `passage: <title, at most 64 tokens>\n\n<window>`
  through the deployment's TEI (`core.ingest.e5-small@1`, 384 dims, cosine). A
  call stops after 30 s with the retryable `embedding_incomplete`, keeping its
  vectors for the next one. `embed_query` encodes `query: <at most 256 tokens>`.
- **Refuses** (`segmentation_limit`, the Version is blocked as
  `ingestion_refused`) text over 256 KiB, more than 64 Parts or 256 windows, a
  window over 4,096 code points or a model input over 512 tokens. A window TEI
  refuses (`inference_refused`) blocks enrichment: keyword search only.
- **Provenance.** Each segment records its token range, overlap, hard cuts,
  title and model-input token counts and the SHA-256 of its model input.

The pinned tokenizer (`tokenizers` 0.23.2 and the checksummed `tokenizer.json`,
prepared by `scripts/prepare_tokenizer.py`) runs as one helper process per
plugin process, loaded once (`tokenizer.py`).

## Configuration

```json
{"tei_url": "http://tei:80",
 "tokenizer": {"python": "/app/.scratch/tokenizer/venv/bin/python",
               "model": "/app/.scratch/tokenizer/tokenizer.json"}}
```

Optional `batch_size` (1 to 32, default 1): windows per TEI request. Same
vectors, but a search waits behind bigger requests.

## Parity and certification

`testdata/golden.json` holds what the engine produced before the move, for
`testdata/parity-input.json`: segments, offsets, derivations, refusals and the
SHA-256 of every float32 vector. `parity_test.go` holds the plugin to it bit
for bit, with one window per TEI request and with 8. On another processor
family than the capture's, where TEI rounds differently, it compares with the
engine's former request to the same TEI. The verify stack runs it and certifies
the plugin (`scripts/core_ingest_plugin.py`); `make check` only unit-tests it.

## Moving an existing deployment

Corpora built before this plugin keep being served by the engine's former E5
space: search works in every mode, and new Versions are searchable by
keyword at once, but their vectors wait. Rebuild each Corpus
(`POST /v0/corpora/{corpus_id}/rebuilds`); the rebuild re-embeds with the same
TEI and model, so results do not change, and the waiting vectors attach.
