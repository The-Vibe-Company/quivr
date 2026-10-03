# Jev reranking

An optional retrieval plugin (`jev.rerank`), not an engine dependency. Pin it
beside `core.retrieve` to enable Jev in `deep`. Jev 1.0.0 serves only `deep`
and requires `core.retrieve` 1.1.x's `default` profile and Plugin API 0.13.
Map `default` to `core.retrieve/default` and `deep` to `jev.rerank/deep`.

## Configuration

Install the Python SDK and this package, then run `python3 -m jev_rerank`
with `QUIVR_PLUGIN_MANIFEST` pointing at its `quivr-plugin.yaml`. Put
`TYPESAFE_API_KEY` in the sidecar environment, never in a pin. The optional
`TYPESAFE_API_URL` selects a test or private gateway; the default is TypeSafe.
See [Re-rank with Jev](../../docs-site/guides/rerank-with-jev.mdx) for pinning.

The manifest defines the settings: `candidate_count` (20, 30 or 50),
`trim_tokens` (`128`, `256` or `full`), `ranking` (`noul` or `rrf`), and
`cache_entries` (0 disables the cache). Defaults are 30, full, noul and 4096.
Trimming needs a local `tokenizer_path`, loaded once and checked against
`tokenizer_sha256` (default: core.ingest's pinned E5 tokenizer). Trim lengths
are this tokenizer's tokens, not TypeSafe billing tokens. Full needs no
tokenizer. No model download occurs in the search path.

## Search and failure

Round 1 asks the engine for `core.retrieve/default` with mode `hybrid` and
the configured shortlist size, regardless of the caller's search mode.
The engine runs normal search under the same authorized scope. Round 2 sends one batch:
state contains only the query; each Noul question contains its own passage
and an explicit untrusted-material rubric. The model is `jev-1.13.0`, the
rubric is `answers-query-v1`. Probabilities break ties by hybrid score and
segment id. RRF instead combines the two ranks with constant 60; explanations
still show the probability. The returned page may be shorter than `limit`.

Overlapping windows of the same Record Version and Part are not scored twice.
Successful pairs are cached in a locked bounded LRU, scoped by organization,
model, rubric, whitespace-normalized query, immutable segment id and trimming
recipe. Changing case or the trimming recipe does not reuse evidence.

The paid round has at most a 2-second absolute deadline, leaving room within the
3-second profile objective for retrieval and hydration. At most three HTTP
attempts retry only 429 or 5xx. Each attempt reserves its maximum input cost
within the engine's remaining chain allowance; the paid deadline also respects
the remaining time, with 10 ms left to return. Usage reports only Jev's own
paid work; the engine aggregates both profiles. `Retry-After` that cannot fit the remaining
budget causes immediate fallback. Oversized input, invalid answers, missing
usage/model, outages and missing keys return hybrid order with
`re-ranker unavailable: <reason>`, with no Jev score, including for cached hits.
No query, passage, provider body, identifiers or key is logged.

Input costs $0.042/M tokens; output is free. Usage reports actual input tokens
when the provider returns them. A sent request without valid usage conservatively
reserves the model's 65,536-token maximum, so total reported cost stays below
1 cent even on three failures. This is a cost upper bound, not evidence that
the provider billed a rejected request. Structured round logs distinguish
these estimated tokens and report cache hits and fallbacks for measurement.
TypeSafe's organization-wide request and token rate limits remain external;
check your account's limits. Batching, trimming and caching matter more than price.
There is no per-user quota in the engine.

## Verification

With Go and the Python dependencies installed, run
`python3 -m unittest discover -s tests` in this directory. The offline
candidate fixture builds and runs the real `core.retrieve` plugin. Tests use only a
loopback fake TypeSafe server. `quivr plugin test .` certifies deep's
no-key fallback fixtures; unset the TypeSafe environment for that replay.
Paid dispatches measure only K=30, trim=256, Noul on miracl-fr (150 searches max),
with no paid probes, warmups or replay. A run reserves retries before each search
and stops paid admission before its 5M-token budget can be exceeded; logs print
actual/reserved tokens and costs. K/trim/fusion sweeps are offline or deferred.
Without a key the lane skips deep; nightly unpaid sets remain unchanged.

## Measurement

The paid lane above is the only measured setting. Before relying on `deep`,
measure your own workload: relevance, p95 latency,
input tokens, cost, fallback rate, and cold and warm cache hit rates. The
profile objective is 3 seconds and 1 cent per search.
