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
require `sslmode=verify-full`, the host's trusted CA, and a PostgreSQL endpoint
reachable from Modal. The runner uses short connections and ten-second statement
timeouts; reserve connection capacity for up to four workers per dispatch.
An unavailable control store refuses paid work, with no local admission fallback.

The operator creates these Modal Secrets; values never belong in configuration:
Jev is an optional paid [passage reranker](../plugins/jev-rerank/README.md).

| Secret | Environment names |
| --- | --- |
| `quivr-eval-results` | `EVAL_CONTROL_DATABASE_URL`, `MLFLOW_TRACKING_URI`, `MLFLOW_TRACKING_USERNAME`, `MLFLOW_TRACKING_PASSWORD` |
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

The campaign freezes the baseline, complete set family, diagnostic roles,
prices, caps, thresholds, code/scorer revision and latency mode on first dispatch.
Changing them requires a new campaign identifier. Candidate settings may vary.
Identical config/dataset/tier/code work reuses completed evidence or reports
`leased` while another worker owns it. Expired claims can be recovered; stale
owners cannot admit batches or publish canonical evidence. Unknown API effects
after a crash may repeat on recovery, and their reservations remain charged.

## Understand the gates and accounting

Quality requires at least +0.01 nDCG@10 with Holm-corrected paired significance
over the frozen set family. Corrected significant losses block eligible sets.
MLDR-fr, WebFAQ-fr, TREC-COVID and MKQA-fr are diagnostic because of saturation,
small samples or proxy questions; restricted-licence sets are also diagnostic.
Their scores remain reported. Diagnostics cannot supply the qualifying gain.

Fresh searches run serially after one fixed first-query warmup. Warmup spend is
in the campaign ledger and excluded from per-search metrics. Their p95 includes query embedding,
retrieval and reranking and must be at most 1.2 times baseline. Both configurations
use the same resource class. `--cached-exploration` reuses query vectors for cheap
fusion sweeps; it reports no measured p95 and cannot pass the latency gate.

Price limits are $0.0005/search for `default`, $0.05/search for `deep`, and
$10/1,000 original documents. Configure `gates` to override `min_gain`,
`latency_ratio`, `search_usd` or `index_usd`. Serving prices include query
embedding, reranking and attributable compute; indexing includes every document
window and indexing compute. Cached usage is repriced, rather than treated as
free serving. Token/time attribution across a cached batch is proportional to
its input bound; campaign confirmed provider usage remains exact.

Provider admission and planning share the UTF-8 byte-plus-eight-token bound.
Each confirmed response releases unused reservation. Failed/unknown attempts
remain reserved; a provider exceeding the supported bound stops the campaign.
A token reservation is multiplied by the frozen provider price. Confirmed usage
above the bound is charged at its actual amount before stopping future work.
A daily cap hit stops that ledger for its UTC day, even if a later settlement
releases money. Other campaigns have independent caps. Exact reported agent
input/output tokens may be supplied as `agent_token_usage`; absent usage remains
unknown, and agent tokens are never capped or inferred from text.

**The compute cap covers runner reservations, not the full Modal invoice.**
It reserves configured CPU/memory cost for the enforced execution and startup
timeouts before each invocation, disables automatic retries and retains charges
when completion is unknown. Successful RPC elapsed time is a conservative
compute charge, including queue/transport time. Builds, Volume storage and other
account charges need separate operator budgets. Bounds depend on correct rates
and provider token limits; observed overages cannot undo already incurred bills.

Tier 1 accepts public campaign-dev sets only. An upstream benchmark's original
`test` partition is distinct from a campaign's frozen heldout. No tier-1 command
can consume campaign-heldout data. The store's maximum-ten confirmation counter
is reserved for a future trusted full-engine confirmation runner.

## Recover and validate

The Volume `quivr-eval-embeddings-cache` holds immutable vectors and the results
outbox. SQL stages canonical records before tracking uploads. Each new remote
measurement replays the committed Volume outbox. Re-running the
same command recovers completed evidence and replays pending tracking writes.
Preserve the Volume and control schema until all results are synced and the
campaign is archived. Never delete an unknown reservation to free a cap.

Before live acceptance, run fake-provider unit tests and the SQL adapter tests
with `EVAL_CONTROL_TEST_DSN` pointing only to a disposable PostgreSQL database.
For example, after provisioning that local database (commands exercised locally):

```sh
python -m unittest discover -s scripts/eval -p 'test_*.py'
```

The operator then checks two concurrent dispatches reuse claims, a small cap
stops before another paid attempt, a pending tracking write replays, and a
deliberately excessive price is rejected. Live Modal/provider acceptance belongs
on a measurement machine, with reviewed rates and caps.

## Next

- [Query shared results](eval-results.md).
- [Deploy the tracking store](../deploy/mlflow/README.md).
