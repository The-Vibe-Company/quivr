# `newsroom.retrieve` — Unified Editorial Retrieval Plugin

`newsroom.retrieve` is a standalone, certified Quivr retrieval plugin (Plugin API `>=0.17.0 <0.21.0`) designed for inverted-pyramid newsroom and wire-service archives at million-document scale. It requires zero Quivr engine modifications and zero per-query LLM expansion.

## Profiles

- **`newsroom.retrieve/default`** (`max_latency_ms: 500`, `max_cost_cents: 0`):
  1. **Streamlined 2-Leg Hybrid Retrieval (`O(1)` at corpus scale):** Issues a single ungrouped `near_vector` leg (`dense_k: 100`) and a stopword/elision-cleaned `bm25` leg (`lexical_k: 60`, `clean_lexical_query: true`) in Round 1. Cleaning generic multilingual stopwords and French elision prefixes inside the plugin prevents raw BM25 branches from masking analyzed stems on unmodified Quivr `main`.
  2. **Bruch Min-Max Convex Fusion (`0.65` dense / `0.35` lexical):** Score-calibrated convex combination over the top-K candidate pools (also supports `dbsf`, `rrf`, optional 3rd focused entity `bm25` leg, and optional grouped dense leg).
  3. **Three Orthogonal O(1) Editorial Aboutness Signals:**
     - **Title + Lead Pool-IDF Coverage (`title_lead_weight: 0.12`, `lead_chars: 250`):** Rewards dispatches whose headline and opening lead cover query facets weighted by in-pool IDF.
     - **Multi-Passage Dense Corroboration (`passage_support_weight: 0.12`):** Rewards articles matching >= 2 distinct dense passages in the ungrouped pool without penalizing single-passage dispatches.
     - **Lead-vs-Body Aboutness Ratio (`FirstP / MaxP`, `passage_ratio_weight: 0.10`):** Compares lead segment (`Start == 0`) similarity against max segment similarity to demote buried passing mentions while keeping single-passage records neutral.
     *(Optional pool-IDF superlinear coverage deficit penalty, pairwise/SDM proximity, and exponential earliest-position salience remain configurable for custom corpora.)*

- **`newsroom.retrieve/deep`** (`max_latency_ms: 3000`, `max_cost_cents: 1`):
  - Constructs **Inverted-Pyramid Multi-Passage Surrogates** (`[LEAD]` + `[MATCHED DEEP PASSAGE]` / `[SUPPORTING PASSAGE]` or `[DEEP BODY EXCERPT — NOT IN ARTICLE LEAD]`, `rerank_max_doc_chars: 2000`) for an HTTP cross-encoder reranker (TEI or Cohere protocol, with optional custom `rerank_instruction`).
  - Applies **score-anchored tail stitching** past `rerank_top_k: 30` (`rerank_weight: 0.90` linear blend) so Recall@50 is never truncated.
  - Supports **Distribution-Shape QPP Adaptive Confidence Cascade** (`adaptive_cascade: true`, `jaccard10 >= 0.50`, `margin >= 0.15`, `dgap >= 2.25`) to early-exit high-confidence queries at `$0` GPU cost.
  - Supports **graceful fallback** (`fallback_on_error: true`, default) returning the Stage-1 `default` ranking on reranker timeout or transient HTTP error.

## High-Recall Extensions (all opt-in; defaults keep the 2-leg champion request list)

- **Wide funnel under the `k <= 100` contract:** diverse k=100 requests instead of one deep request. These are a boilerplate-stripped dense view (`dense_view_mode`, `dense_view_weight`, `dense_view_grouped`), a record-grouped full-query dense recall leg (`dense_recall_weight`), a stripped lexical view (`lexical_view_mode`, `lexical_view_weight`), and optional zero-LLM Round-2 consensus PRF over dense and/or BM25 (`round2_consensus_weight`, `round2_lexical_weight`). Each extra leg is a separately calibrated convex component, and Round-1 legs are attributed by `request_index`.
- **Head-calibration depth:** `lexical_norm_depth` and `dense_norm_depth` compute the min-max floor over the first N hits, so deeper admission lists do not compress the head.
- **Head/tail decoupled fusion:** `tail_fusion: rrf` re-orders positions after `tail_start` by weighted RRF over leg ranks. It is experimental and was measured worse than convex for Recall@50 on newsroom data.
- **Deep-tier calibration:** `rerank_score_space: logit` uses unsaturated raw relevance margins (`raw_score`, e.g. `logit(yes) - logit(no)`) or log-odds of probabilities. `rerank_ranking: zscore` standardizes reranker and Stage-1 scores per query before blending.
- **Recall band:** `rerank_band_start` / `rerank_band_end` / `rerank_band_max_chars` score Stage-1 ranks `rerank_top_k+1..band_end` with compact lead views in the same call. Only the region straddling the Recall@50 cut is re-sorted. The reranked head above it is untouched and scores stay monotonic.
- **Multi-passage dual views:** `rerank_multi_passage_top_n` scores a lead view and a best-body view separately and combines them (`blend`, `max`, `maxmin`, `lead_gated`). `surrogate_mode: rich` builds `[LEAD] + [BEST BODY PASSAGE]` surrogates.
- **Sharded reranking:** `rerank_parallel_requests` splits one rerank call into N interleaved concurrent requests, so multi-container endpoints finish within the host's hard 9 s retrieval deadline.
- **Diagnostics:** `dump_pool` emits the fused pool with per-leg ranks to stderr for offline pool-recall analysis.

## Verification

```bash
go vet ./...
go test -race ./...
quivr plugin inspect .
quivr plugin test .
```
