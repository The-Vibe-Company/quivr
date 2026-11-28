# `newsroom.retrieve` — High-Performance Editorial Retrieval Plugin

`newsroom.retrieve` (`0.2.0`) is a standalone, certified Quivr retrieval plugin (Plugin API `>=0.21.0 <0.22.0`) designed for inverted-pyramid newsroom and wire-service archives at million-document scale. **Best-performing settings are enabled by default**—operators only override knobs when opting out for lower latency or GPU cost, or when enabling deeper candidate pools (up to `400` per leg) or live-news recency decay.

## Profiles

- **`newsroom.retrieve/default`** (`max_latency_ms: 500`, `max_cost_cents: 0`):
  1. **Multi-View Editorial Hybrid Retrieval (up to `400` candidates per leg, `include_lead: true`):** Issues an ungrouped `near_vector` leg (`dense_k: 100`, max `400`), a stopword/elision-cleaned `bm25` leg (`lexical_k: 60`, max `400`), and boilerplate-stripped question views (`dense_view_mode: "question"`, `lexical_view_mode: "question"`) when the query has a structured clause delimiter or interrogative framing. Stage-1 requests set `include_lead: true` by default so deep-passage hits (`passage_ordinal > 0`) receive `Candidate.LeadPassage` directly from the engine's hydration cache with zero extra rounds (set `include_lead: false` to disable, or `dense_view_mode: "none"` / `lexical_view_mode: "none"` to reduce to 2 legs).
  2. **Bruch Min-Max Convex Fusion (`0.65` dense / `0.35` lexical + calibrated view weights):** Score-calibrated convex combination over top-K candidate pools (also supports `dbsf` and `rrf`).
  3. **Editorial Aboutness & Rich Metadata Signals (all default-safe, `0` disables):**
     - **Title + Lead Pool-IDF Coverage (`title_lead_weight: 0.12`, `lead_chars: 250`):** Prefers explicit `Candidate.Title` and hydrated `Candidate.LeadPassage` (with clean fallback to `SourceRanges`/offset extraction) to reward dispatches whose headline and opening lead cover query facets weighted by in-pool IDF.
     - **Multi-Passage Dense Corroboration (`passage_support_weight: 0.12`):** Rewards articles matching >= 2 distinct dense passages without penalizing single-passage dispatches.
     - **Lead-vs-Body Aboutness Ratio (`FirstP / MaxP`, `passage_ratio_weight: 0.10`):** Uses exact `Candidate.PassageOrdinal` and `Candidate.TotalPassages` (with offset fallback when absent) to identify single-passage records and demote buried passing mentions by relative depth.
     - **`CommonMetadata` Facet Overlap (`metadata_overlap_weight: 0.08`):** Pool-IDF query aspect overlap against `subjects`, `tags`, `place`, `country`, `author`, and `source` (zero effect when `common_metadata` is absent).
     - **Temporal Date-Mention Alignment (`date_alignment_weight: 0.06`):** Aligns explicit calendar years and EN/FR month+year or ISO dates in the query against `common_metadata.published_at`.
     - **Optional Pool-Relative Freshness (`recency_weight: 0`, `recency_half_life_days: 30`):** Deterministic exponential freshness decay relative to the newest `published_at` in the candidate pool (disabled by default for archival neutrality; set `> 0` for breaking-news feeds).

- **`newsroom.retrieve/deep`** (`max_latency_ms: 3000`, `max_cost_cents: 1`, requires `rerank_endpoint`—otherwise returns the Stage-1 ranking):
  - **Inverted-Pyramid Multi-Passage Surrogates (`rerank_top_k: 50`, max `400`, `rerank_max_doc_chars: 2000`):** Pairs `[TITLE]` + `[LEAD]` + `[MATCHED DEEP PASSAGE]` / `[SUPPORTING PASSAGE]`—now populating `[TITLE]` and `[LEAD]` on deep-passage hits directly from `Candidate.Title` and hydrated `Candidate.LeadPassage`—for an open-source HTTP cross-encoder (TEI or Cohere protocol, e.g. `Qwen3-Reranker-4B` Apache-2.0).
  - **Unsaturated Logit Calibration (`rerank_score_space: "logit"`, `rerank_ranking: "zscore"`, `rerank_weight: 0.90`):** Uses raw relevance margins (`raw_score`) or log-odds fallback standardized per query to prevent probability saturation near `1.0`.
  - **Recall Band (`rerank_band_start: 30`, `rerank_band_end: 80`, max `400`, `rerank_band_max_chars: 900`):** Scores Stage-1 ranks `51–80` with compact title+lead views (using hydrated `LeadPassage` for deep hits) in the same rerank call so relevant dispatches straddling the Recall@50 cut can move up without disturbing the top-30 head. Set `rerank_band_end: 0` to disable.
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
