# `newsroom.retrieve` — Experimental Multi-Branch Algorithmic Retrieval Plugin

`newsroom.retrieve` is an experimental Quivr retrieval plugin (Plugin API `>=0.17.0 <0.21.0`) designed for inverted-pyramid newsroom and editorial wire archives at million-document scale without requiring per-query LLM expansion or neural reranking in the fast `default` tier.

## Profiles

- **`newsroom.retrieve/default`** (`max_latency_ms: 500`, `max_cost_cents: 0`):
  1. **Multi-Branch Retrieval + Calibrated Fusion:** Parallel `near_vector` (dense), full-query `bm25`, and zero-LLM focused entity/key-term `bm25` legs fused via Distribution-Based Score Fusion (DBSF), Bruch Convex Combination, or weighted RRF, with optional query-adaptive dense weighting and soft CombMNZ consensus bonus.
  2. **Stage-1.5 Candidate-Pool Scoring (O(1) at corpus scale):**
     - Pool-IDF-weighted **Query-Aspect Coverage** + superlinear deficit penalty (`gamma * (1 - cov)^rho`) to suppress missing-element false positives.
     - **Proximity Scoring** (`pairwise` min-span log decay or Clarke shortest-cover + Metzler & Croft SDM ordered/unordered bigrams).
     - **Inverted-Pyramid Salience** (zone weighting + continuous earliest-mention exponential decay `exp(-pos_1/600)` + single-tail-mention detector) to suppress deep passing mentions.
  3. **Lead-vs-Body Aboutness Ratio (`FirstP / MaxP`):** Optional ungrouped `near_vector` pass comparing lead segment (`Start == 0`) similarity against max segment similarity without penalizing single-passage records.

- **`newsroom.retrieve/deep`** (`max_latency_ms: 3000`, `max_cost_cents: 1`):
  - Constructs compact **Inverted-Pyramid Multi-Passage Surrogates** (`[LEAD]` + `[MATCHED DEEP PASSAGE]` / `[SUPPORTING PASSAGE]`) for an optional HTTP cross-encoder reranker (TEI or Cohere protocol).
  - Applies **score-anchored tail stitching** past `rerank_top_k` so Recall@50 is never truncated.
  - Supports **Adaptive Confidence Cascade** early-exit using Stage-1 dense/lexical top-10 agreement and score margin.

## Verification

```bash
go test ./...
quivr plugin inspect .
quivr plugin test .
```
