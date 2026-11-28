# `newsroom.retrieve` — High-Performance Editorial Retrieval Plugin

`newsroom.retrieve` is a standalone, certified Quivr retrieval plugin (Plugin API `>=0.17.0 <0.21.0`) designed for inverted-pyramid newsroom and wire-service archives at million-document scale. **Best-performing settings are enabled by default**—operators only override knobs when opting out for lower latency or GPU cost.

## Profiles

- **`newsroom.retrieve/default`** (`max_latency_ms: 500`, `max_cost_cents: 0`):
  1. **Multi-View Editorial Hybrid Retrieval (bounded candidate-pool cost):** Issues an ungrouped `near_vector` leg (`dense_k: 100`), a stopword/elision-cleaned `bm25` leg (`lexical_k: 60`), and boilerplate-stripped question views (`dense_view_mode: "question"`, `lexical_view_mode: "question"`) when the query has a structured clause delimiter or interrogative framing. Set `dense_view_mode: "none"` and `lexical_view_mode: "none"` to reduce to 2 legs.
  2. **Bruch Min-Max Convex Fusion (`0.65` dense / `0.35` lexical + calibrated view weights):** Score-calibrated convex combination over top-K candidate pools (also supports `dbsf` and `rrf`).
  3. **Three Orthogonal Editorial Aboutness Signals (all enabled by default):**
     - **Title + Lead Pool-IDF Coverage (`title_lead_weight: 0.12`, `lead_chars: 250`):** Rewards dispatches whose headline and opening lead cover query facets weighted by in-pool IDF.
     - **Multi-Passage Dense Corroboration (`passage_support_weight: 0.12`):** Rewards articles matching >= 2 distinct dense passages without penalizing single-passage dispatches.
     - **Lead-vs-Body Aboutness Ratio (`FirstP / MaxP`, `passage_ratio_weight: 0.10`):** Compares lead segment similarity against max segment similarity to demote buried passing mentions while keeping single-passage records neutral.

- **`newsroom.retrieve/deep`** (`max_latency_ms: 3000`, `max_cost_cents: 1`, requires `rerank_endpoint`—otherwise returns the Stage-1 ranking):
  - **Inverted-Pyramid Multi-Passage Surrogates (`rerank_top_k: 50`, `rerank_max_doc_chars: 2000`):** Pairs `[TITLE]` + `[LEAD]` + `[MATCHED DEEP PASSAGE]` / `[SUPPORTING PASSAGE]` for an open-source HTTP cross-encoder (TEI or Cohere protocol, e.g. `Qwen3-Reranker-4B` Apache-2.0).
  - **Unsaturated Logit Calibration (`rerank_score_space: "logit"`, `rerank_ranking: "zscore"`, `rerank_weight: 0.90`):** Uses raw relevance margins (`raw_score`) or log-odds fallback standardized per query to prevent probability saturation near `1.0`.
  - **Recall Band (`rerank_band_start: 30`, `rerank_band_end: 80`, `rerank_band_max_chars: 900`):** Scores Stage-1 ranks `51–80` with compact lead views in the same rerank call so relevant dispatches straddling the Recall@50 cut can move up without disturbing the top-30 head. Set `rerank_band_end: 0` to disable.
  - **Interleaved Sharding (`rerank_parallel_requests: 2`):** Splits rerank batches across 2 concurrent HTTP requests over a pooled transport (set `1` for single-container endpoints).
  - **Optional Cost-Saving Cascade (`adaptive_cascade: true`) & Graceful Fallback (`fallback_on_error: true`):** Early-exits high-agreement queries at Stage 1 (`$0` GPU) when enabled, and falls back cleanly to Stage 1 on timeout or HTTP error.

## Local verification

```sh
cd plugins/newsroom-retrieve
go vet ./...
go test -race ./...
quivr plugin inspect .
quivr plugin test .
```
