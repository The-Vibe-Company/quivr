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

The sample uses a **conservative assumed compute rate**, not a Modal quote.
Review it for eight CPU cores and 16 GiB memory before paid dispatch. Provider
defaults are dated 2026-10-03 list-price estimates. Supply a complete `prices`
map and new `price_revision` when your deployment prices differ.

This dry run was exercised locally:

```sh
python scripts/eval/modal_search.py \
  --policy scripts/eval/examples/modal-policy.json \
  --candidate scripts/eval/examples/modal-candidate.json --dry-run
```

It prints the policy, candidate and maximum reservation per invocation, without
loading datasets or keys. The sample reserves $2.10 for each dispatched worker.
The default daily caps are $1,000 each for provider APIs and runner compute;
the sample deliberately uses smaller caps.

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
its reason. Every complete measurement includes lineage and public per-query
scores in the results wrapper. Inspect the experiment with the results CLI.
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

Quality covers every query with relevance judgments using batched, cached embeddings. Fresh latency
uses up to 50 serial queries, ordered by SHA-256 of the ID (ID breaks ties), after
warming up the lexicographically first judged ID, which may also be timed.
Both configurations use the same sample and resource class.
`cost.latency_sample` records ordered timed IDs, warmup ID and policy;
`gates.latency.samples` echoes both and rejects missing or mismatched evidence.
P95 includes query embedding, retrieval and reranking; its limit is 1.2 times baseline.
Serving price uses the fresh sample; warmup charges stay outside per-search metrics.
`--cached-exploration` reuses query vectors and cannot pass the unmeasured latency gate.

Price limits are $0.0005/search for `default`, $0.05/search for `deep`, and
$10/1,000 original documents. Override `min_gain`, `latency_ratio`, `search_usd` or
`index_usd` in `gates`. Serving includes query embedding, reranking and compute.
Indexing covers all document windows, excluding quality-query preparation. Cached
usage is repriced by input bounds and parallel wall time; campaign usage stays exact.

Admission and planning share the UTF-8 byte-plus-eight-token bound at frozen prices.
Confirmed responses release unused reservations; failed/unknown attempts stay reserved.
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

Tier 1 accepts public campaign-dev sets only; an upstream `test` partition differs
from campaign-heldout data, which tier 1 cannot consume. The store's maximum-ten confirmation counter
is owned by the [trusted full-engine confirmation runner](eval-engine-confirmation.md).

## Recover and validate

The Volume `quivr-eval-embeddings-cache` holds immutable vectors and the outbox.
Hosted document fills overlap at most four 128-entry cache chunks; each provider
attempt reserves and settles independently. Local e5 and quality-query fills stay
serial and batched. Claims and validation precede paid work; commits and fenced
publication stay serial. Failed waves drain attempts and retain uncertain charges.
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
