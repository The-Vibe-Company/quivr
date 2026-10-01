# core.retrieve

The first-party retrieval plugin: the search the engine ran itself until
THE-779. The engine now ranks nothing itself, and the api refuses to start
without a retrieval plugin. Every stack pins this one unless another retrieval
plugin is pinned (`scripts/connector_plugin.py` `FIRST_PARTY`, Railway
`CONNECTORS` in `deploy/railway/core-entrypoint.py`, run beside the api only).

## What it does

One round of candidates, then the ranking:

- **Round 1** asks for one candidate list of `k = limit` that follows the
  search mode: `lexical` is `bm25` on the title and body (`field: source`),
  `semantic` is `near_vector` in the served space, `hybrid` is `hybrid` in the
  served space with alpha 0.5 and relative score fusion. A semantic or hybrid
  search whose Corpora serve no vector space is refused (422
  `unsupported_search`).
- **Round 2** returns the served candidates, in the order served, as the
  ranking, with the index's score and an explanation naming the primitive and
  the space. Candidates of equal score, which the index returns in the order
  their objects were written, rank by segment id, so the same search over the
  same Records always ranks the same way.

It makes no model call: the engine encodes the query with the owner of the
served space, core.ingest or, for a Corpus not rebuilt since THE-777, the
engine's former E5 space. It needs no configuration.

## Profiles

| Profile | Budget | Strategy |
| --- | --- | --- |
| `default` | 500 ms, no paid call | the one above |
| `deep` | 3 s, 1 cent | the same, until re-ranking and query rewriting are measured (Spec 15) |

## Parity with the engine

The engine asks the index for at least 150 objects per candidate request
whatever `k`, as its own search did for every limit, so the index query and
its order are unchanged; it keeps each segment's first object, then hydrates
the best ones in a few batches until it holds `k`, dropping what the caller
may no longer read. The
retrieval baseline (`make measure`) records every hit of the 24 CC0 queries
and the query edge cases in the three modes: before and after the move they
are identical, but for the order of keyword hits of exactly equal score, which
already changed from one run of the engine to the next (THE-779). Segment ids
derive from the Corpus, so on two installations such ties may still differ.

## Certification

`make check` vets and unit-tests it and runs `quivr plugin test .`: the
normative fixture and one fixture per mode (`fixtures/`), where each primitive
serves another order, so the first hits show which one was asked for.
