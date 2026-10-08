# core.retrieve

The first-party retrieval plugin, pinned by default beside the api; the engine refuses to start without one.

## What it does

Up to two rounds of candidates, then the ranking:

- **Round 1** asks for `k = candidate_count` (the search limit when unset) per request: `lexical` uses
  one `bm25` request with `group_by: record` on the title and body (`field: source`); `semantic` uses
  `near_vector` with `group_by: record` in every served space; `hybrid` uses `hybrid` with `group_by: record` in every served
  space with the configured weight and fusion (defaults: alpha 0.5 and relative score), in batches of at most eight requests.
  Without a served space, hybrid asks for `bm25`; explicit semantic returns `422 unsupported_search`.
  The engine keeps affected Corpora's keywords alongside hybrid candidates,
  reporting their ids in `retrieval_profile.degraded` (`vectors_unavailable`).
- **Ranking** merges candidates by descending index score and keeps each
  Record once across served spaces, with its best available passage and an explanation
  naming the primitive and space. Equal scores rank by segment id.

It makes no model call: the engine encodes the query with the space's owner:
the ingestion plugin that declares it or, for a Corpus not rebuilt
since THE-777, the engine's former E5 space. Configuration is optional.

## Configuration

Set these keys in the plugin pin's `configuration`; both core profiles use them. The manifest validates them.

| Setting | Meaning; default |
| --- | --- |
| `dense_weight` | Hybrid vector weight (Weaviate `alpha`), 0–1; default 0.5. Keyword weight is `1 - dense_weight`. |
| `candidate_count` | Candidates per served space, or one lexical request, 1–100; default the search limit. A smaller value can return a shorter page. |
| `hybrid_fusion` | `relative_score` (default) normalizes each side's scores then weights them; `ranked` uses reciprocal rank fusion with constant 60. |

Weight and fusion affect hybrid mode only; ranking remains capped by the search limit.
Omitting settings preserves default request bytes. See
[Search profiles](https://docs.quivr.thevibecompany.co/run-quivr/search-profiles#core-retrieval-settings)
for a deployment example, trial mapping and Jev shortlist settings.

## Profiles

| Profile | Budget | Strategy |
| --- | --- | --- |
| `default` | 500 ms, no paid call | the one above |
| `deep` | 3 s, 1 cent | the same, until re-ranking and query rewriting are measured (Spec 15) |

## Parity with the engine

New/rebuilt generations index keyword fields once per Version and fuse item
BM25 with maximum passage-vector scores. Legacy generations keep their keyword
recipe until rebuild. Candidates are hydrated and checked for currentness and access.
Individual keyword and vector candidate queries request at most 2,400 objects.
Lexical highlights inspect up to 2,400 canonical passages per field mapping; longer documents may have a better unseen passage.
The retrieval baseline (`make measure`) records hits of 24 CC0 queries and edge
cases in all three modes. Score ties may differ between installations because segment ids derive from the Corpus.

## Certification

`make check` vets and unit-tests it and runs `quivr plugin test .`: the
normative fixture and one fixture per mode (`fixtures/`), where each primitive
serves another order, so the first hits show which one was asked for.
