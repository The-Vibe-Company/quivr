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
  - Applies **score-anchored tail stitching** past `rerank_top_k: 30` (`rerank_weight: 0.90` linear blend). A single reranker or parallel ensemble at K ≤ 50 preserves the Stage-1 top-50 membership.
  - Supports **Distribution-Shape QPP Adaptive Confidence Cascade** (`adaptive_cascade: true`, `jaccard10 >= 0.50`, `margin >= 0.15`, `dgap >= 2.25`) to early-exit high-confidence queries at `$0` GPU cost.
  - Supports **graceful fallback** (`fallback_on_error: true`, default) returning the Stage-1 `default` ranking on reranker timeout or transient HTTP error.

## Two rerankers

- Set `rerank_secondary_endpoint` and `rerank_cascade_k: 80` to use a TEI filter before the primary reranker. The filter selects the top `rerank_top_k` from that pool. Remaining candidates keep their Stage-1 order below the selected head. A wider pool can change top-50 membership.
- For a three-tier cascade, also set `adaptive_cascade: true` and `rerank_medium_margin`. High-confidence Stage-1 queries exit before either request. A secondary score gap at or above the configured probability margin exits after the filter. Other queries continue to the primary reranker.
- Set `rerank_secondary_endpoint` and `rerank_ensemble` to issue both requests concurrently over identical surrogates. `logit` uses per-query standardized margins and Stage-1 scores. `rank_prob` averages each model's rank percentile and probability, with min-max normalization for Stage 1. Default primary, secondary, and Stage-1 weights are 0.55, 0.35, and 0.10. Both model weights must be positive and all three must sum to 1.
- Logit modes prefer a response's optional `raw_score` and compute a logit from `score` when it is absent. The configured instruction applies to the primary TEI endpoint. Both HTTPS endpoints use the same optional `RERANKER_API_KEY` and share one request deadline.

## Verification

```bash
go vet ./...
go test -race ./...
quivr plugin inspect .
quivr plugin test .
```
