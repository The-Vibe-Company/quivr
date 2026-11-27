# newsroom.retrieve

Prototype retrieval plugin for fast, zero-cost hybrid fusion and inverted-pyramid editorial aboutness ranking.

## Design

- **Two-leg retrieval (single round-trip):** Issues one record-grouped `bm25` leg (`k=60`) and one ungrouped `near_vector` leg (`k=100`) instead of delegating fusion to the index's built-in hybrid operator. The single ungrouped dense call supplies both `MaxP` and per-record passage counts without a third index query.
- **Bruch min-max convex fusion (default `alpha=0.65`):** Independently normalizes lexical and dense scores to `[0, 1]` before convex weighting (also supports `dbsf` and weighted `rrf`).
- **Two O(1) inverted-pyramid editorial aboutness signals (pruned by ablation):**
  1. **Title + Lead IDF-weighted query-term coverage (`title_lead_weight=0.12`, `lead_chars=250`):** Accent/ligature folding with generic French/English stopword removal and in-pool IDF over shortlisted candidates.
  2. **Multi-passage concentration (`concentration_weight=0.12`):** Log-saturated bonus `min(1, ln(chunks)/ln(4))` for records matching multiple distinct dense passages in the top-100 ungrouped pool (zero penalty on concise single-chunk articles).

## Verification

```bash
go test -race ./...
quivr plugin inspect .
quivr plugin test .
```
