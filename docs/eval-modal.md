# Measure search configurations on Modal

Run a candidate and the current configuration on rented machines and read the
quality, regression, latency and price verdict. Passing tier 1 makes a candidate
an exploration finalist; full-engine and held-out confirmation are unavailable.

## Prerequisites

Use Python 3.12, a committed checkout, a configured Modal account and the shared
[results store](eval-results.md). Install measurement dependencies with
`pip install -r scripts/eval/requirements-modal.txt`. Measurements require
`--allow-paid` and are refused in CI. Dry runs need no keys or Modal account.
Set up account credentials with [Modal tokens](https://modal.com/docs/cli/latest/token).
Obtain tracking credentials from your store operator as described in the results
guide. Supply them locally too if you want dispatch receipts synced to MLflow.

Your store operator applies [eval-control.sql](../deploy/mlflow/eval-control.sql)
as the PostgreSQL owner. It creates the evaluation schema and a NOLOGIN role
`quivr_eval_control`; applying it again preserves data. Grant that role to a
dedicated login and provision its password through your secret manager. The
login needs no access to tracking or authentication tables. For example, using
your database owner's authenticated psql connection (not run against a hosted store):

```sh
psql -v ON_ERROR_STOP=1 -f deploy/mlflow/eval-control.sql
```

Supply `EVAL_CONTROL_DATABASE_URL` on the dispatch machine. Remote connections
require `sslmode=verify-full`, a trusted CA and an endpoint reachable from Modal.
Cache operations use one connection/transaction per chunk of at most 128 entries,
with ten-second statement timeouts. Reserve capacity for four workers per
dispatch. An unavailable store refuses paid work without local fallback.

For a private certificate authority, set `EVAL_CONTROL_CA_PEM` to the full PEM
certificate text, including real newlines, on the dispatch machine and in
`quivr-eval-results`. Use the same URL and PEM values in both environments.
Keep `sslmode=verify-full` in the URL and omit `sslrootcert`: combining that DSN
option with a PEM is rejected. Each connection writes a mode-0600 temporary CA
file and removes it when the transaction ends, including on failure. A wrong CA
or hostname refuses the connection; certificate verification is never disabled.

The operator creates these Modal Secrets; values never belong in configuration:
Jev is an optional paid [passage reranker](../plugins/jev-rerank/README.md).

| Secret | Environment names |
| --- | --- |
| `quivr-eval-results` | `EVAL_CONTROL_DATABASE_URL`, optional `EVAL_CONTROL_CA_PEM`, `MLFLOW_TRACKING_URI`, `MLFLOW_TRACKING_USERNAME`, `MLFLOW_TRACKING_PASSWORD` |
| `quivr-eval-embeddings` | `AZURE_FOUNDRY_ENDPOINT`, `AZURE_FOUNDRY_KEY` |
| `quivr-eval-rerank` | `TYPESAFE_API_KEY`, only for Jev |

## Prepare the comparison

Start with the [policy](../scripts/eval/examples/modal-policy.json) and
[candidate](../scripts/eval/examples/modal-candidate.json) examples. An empty
baseline selects pinned multilingual-e5-small. Explicitly configure another
baseline when it represents your current deployment. Hosted models require a
deployment `revision`, dimensions and provider price. Fusion uses weighted
reciprocal ranks of exact best-piece cosine and BM25; `dense_weight=1` is dense
only, `0` is BM25 only. `candidate_count` bounds optional Jev reranking.

In the policy, `modal_cpu` / `modal_memory_mib` bound containers (defaults: 2 cores / 4096 MiB). `modal_usd_per_second` = CPU × `modal_cpu_usd_per_second` + MiB / 1024 × `modal_gib_usd_per_second`.
Defaults estimate [Modal function pricing](https://modal.com/pricing) on 2026-10-05; review before dispatch. An explicit aggregate rate overrides the estimate; remove it for size-derived pricing. Underpricing is refused.
Provider defaults estimate 2026-10-03 prices. Change `prices` and `price_revision` as needed.

This dry run was exercised locally:

```sh
python scripts/eval/modal_search.py \
  --policy scripts/eval/examples/modal-policy.json \
  --candidate scripts/eval/examples/modal-candidate.json --dry-run
```

It prints the policy, candidate and maximum reservation per invocation, without
loading datasets or keys. The sample reserves about $0.074 for each dispatched worker.
The default daily caps are $1,000 each for provider APIs and runner compute;
the sample deliberately uses smaller caps.

## Add a private working set

Mix public sets with a private encrypted working set using a `working` input
descriptor. Follow [private working-set setup](eval-private-working.md) for the
Volume upload, age identity Secret, runtime references and aggregate-only outputs.

## Run and inspect the result

The following needs paid-provider and Modal credentials and was not run live:

```sh
python scripts/eval/modal_search.py \
  --policy scripts/eval/examples/modal-policy.json \
  --candidate scripts/eval/examples/modal-candidate.json \
  --campaign example-dev --allow-paid
```

The command prints JSON. A passing comparison returns `better` or `cheaper`,
`status=exploration_finalist`, four gate results, tracking receipts and ledger
totals. All other verdicts exit 2. A stopped budget returns `status=capped` and
its reason. Every complete measurement includes lineage. Public sets include per-query
scores; private working sets expose aggregates only. Inspect the experiment with the results CLI.
`cheaper` means all gates pass and search cost is lower on every set;
`better` means all gates pass without that strict cost improvement.

The campaign freezes its baseline, sets, diagnostics, prices, caps, thresholds,
code/scorer revision and latency mode. Changes require a new campaign identifier;
candidate settings may vary. Identical work reuses evidence or reports `leased`.
Expired claims can be recovered; stale owners cannot admit or publish work.
Unknown API effects may repeat on recovery; their reservations remain charged.

## Understand the gates and accounting

Quality requires at least +0.01 nDCG@10 with Holm-corrected paired significance
over the frozen set family. Corrected significant losses block eligible sets.
MLDR-fr, WebFAQ-fr, TREC-COVID and MKQA-fr are diagnostic because of saturation,
small samples or proxy questions; restricted-licence sets are also diagnostic.
Their scores remain reported. Diagnostics cannot supply the qualifying gain.

Quality uses every judged query with batched, cached embeddings. Fresh latency samples
up to 50 queries in SHA-256 ID order, after warming up the first judged ID (also eligible).
Public/private pairs prepare both indexes in one container with the same resources,
then alternate baseline/candidate warmups and samples (A/B/A/B).
Each candidate gets its own paired baseline; completed pairs replay without provider calls.
Up to the spec’s `parallelism` trials index and score concurrently. Only paired fresh warmups/samples
wait for an exclusive campaign window; other trials continue quality. Window waits stay outside timing. Busy trial slots return `leased` before paid dispatch; detached calls retain their bounded trial slots.
Failed sample loops release the window; control outages retain its bounded fence until recovery or expiry.
Public `cost.latency_sample` records sample/warmup IDs and policy; `gates.latency.samples`
rejects missing/mismatched evidence. Private samples remain internal; comparability is published.
P95 includes local embedding/retrieval/reranking and successful provider round trips;
its limit is 1.2 times baseline. Retry HTTP, backoff, admission and ledger waits are excluded.
`cost.search_timing_ms` reports their p50/p95, embedding/retrieval/rerank/wall time and
`retried_samples`. Wall time includes lease renewal; the service timer starts after renewal.
Warmup charges stay outside fresh serving price; `--cached-exploration` cannot pass latency.

Price limits are $0.0005/search for `default`, $0.05/search for `deep`, and
$10/1,000 original documents. Override `min_gain`, `latency_ratio`, `search_usd` or
`index_usd` in `gates`. Serving includes query embedding, reranking and compute.
Indexing covers document windows, excluding quality-query preparation. Cached usage is
repriced by input bounds and local compute time; campaign usage stays exact.
Serving compute excludes provider HTTP, retry and ledger waits; actual invocation spend
remains in the Modal ledger. `cost.search_provider_usd` and `cost.search_compute_usd`
split the average search price; `cost.search_timing_ms` splits provider and local time.
Timing-versioned caches prevent reuse of wall-time attributions.
Admission and planning share the UTF-8 byte-plus-eight-token bound at frozen prices.
Confirmed responses release unused reservations. This Azure hosted adapter settles
429 rejections at zero; other failed/unknown attempts stay reserved. Successful
responses without confirmed token usage are rejected and remain reserved.
Usage above the bound is charged at its actual amount and stops the campaign.
A daily cap hit stops that ledger for its UTC day, even after later settlements.
Other campaigns have independent caps. Exact reported `agent_token_usage` contains
input/output tokens; absent usage remains unknown, never inferred or capped.

**The compute cap covers runner reservations, not the full Modal invoice.**
It reserves configured CPU/memory cost for the enforced execution and startup
timeouts before each invocation, disables automatic retries and retains charges
when completion is unknown. Successful RPC elapsed time is a conservative
compute charge, including queue/transport time. Builds, Volume storage and other
account charges need separate operator budgets. Bounds depend on correct rates
and provider token limits; observed overages cannot undo already incurred bills.

Tier 1 accepts public campaign-dev sets and private working descriptors. An upstream
public `test` partition differs from campaign-heldout data, which tier 1 cannot consume.
The [trusted full-engine confirmation runner](eval-engine-confirmation.md) owns the maximum-ten confirmation counter.

## Recover and validate

The Volume `quivr-eval-embeddings-cache` holds immutable vectors and the outbox.
Hosted document fills overlap at most four 128-entry cache chunks; each provider
attempt reserves and settles independently. Campaign Azure/Cohere requests share SQL admission: at most four requests across containers,
halved after 429s with a bounded Retry-After cooldown. It recovers one slot after 16 times the current slot count in clean requests;
requests already in flight drain at the old limit. Local e5 and quality-query
embedding fills stay serial and batched. The policy field `quality_concurrency` bounds re-ranking waves (1–32, default 8), preserving rankings, scores and per-search prices. Fresh warmup and latency stay serial.
Attempts reserve independently; failed waves drain and retain uncertain charges. `cost.phase_usage` reports process CPU and elapsed seconds for indexing, quality, scoring and fresh latency, including warmup, excluding paired idle time. CPU/elapsed estimates average cores used, separately from serving cost.
Trials wait for shared in-flight cache fills and reload committed Volume files before reuse; claims precede paid work and commits/publication stay serial.
Chunks commit before publication; lost ownership rolls it back. Logs exclude texts.
Reruns recover evidence and tracking writes. Keep the Volume and schema until
results sync and campaign archival; never delete unknown reservations.

Killing the dispatcher does not guarantee cancellation. Remote work may continue
through `startup_seconds` and `max_seconds`; unknown compute stays charged.
Cache claims expire after 24 hours. Inspect Modal before retrying; reuse completed chunks.

Before live acceptance, run fake-provider unit tests and the SQL adapter tests
with `EVAL_CONTROL_TEST_DSN` pointing only to a disposable PostgreSQL database.
For example, after provisioning that local database (commands exercised locally):

```sh
python -m unittest discover -s scripts/eval -p 'test_*.py'
```

The operator checks claim reuse, cap admission, tracking replay and excessive-price
rejection on a measurement machine with reviewed rates and caps.

## Next

- [Query shared results](eval-results.md).
- [Deploy the tracking store](../deploy/mlflow/README.md).
