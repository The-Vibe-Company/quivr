# Record and compare search measurements

Use `scripts/eval/results` to share evaluation results through an MLflow tracking
server. Without a server, it records JSON locally and replays those records later.

## Prerequisites

Use Python 3.9+ from the repository root. Logging, listing and Pareto queries need
only Python's standard library. Paired comparisons need `scipy`, included in
`scripts/eval/requirements.txt`. The server uses HTTP Basic authentication;
get a tracking URL, username and password from its operator and supply
`MLFLOW_TRACKING_URI`, `MLFLOW_TRACKING_USERNAME`, `MLFLOW_TRACKING_PASSWORD`
through your secret manager. CI uses no live credentials. Telemetry is disabled.

## Log a measurement

Write a JSON record with these fields. Use `null` when a value was not measured.
Keep credentials and document/query text out of records and configuration.

| Field | Content |
| --- | --- |
| `schema_version`, `experiment` | `1`, aggregate experiment name |
| `git_sha`, `plugin_digest` | Code commit and plugin artifact digest |
| `config` | Full embedding, dimensions, chunking, fusion, candidate and reranker settings |
| `dataset` | `name`, `version`, `split`, `fingerprint`, `private` boolean |
| `tier`, `machine`, `duration_seconds` | `direct` or `engine`, machine label, duration |
| `cost` | Provider objects with `tokens`, `usd`, `modal_seconds`; extra accounting allowed |
| `metrics` | Numeric metric map, or null for unknown values |
| `per_query` | Metric → opaque query id → numeric score; no text |

Metric names normalize `@` to `_at_`. Use `ndcg@10`, `recall@10`, `mrr@10`,
`latency_p50_ms`, `latency_p95_ms`, `cost_per_search_usd` and
`cost_per_1000_documents_usd`. Numeric settings and provider accounting are stored
as metrics under `config.*` and `cost.*`, alongside bounded parameters.
Full configuration and public per-query scores remain downloadable artifacts.

For example, with your measurement file (not run here):

```sh
scripts/eval/results log measurement.json
scripts/eval/results log candidate.json --baseline <baseline-result-key>
```

The receipt contains `result_key`, `status` (`synced` or `pending`) and, when
synced, `run_id`. Use `--baseline` on the first log of a candidate to record paired
statistics as metrics and a comparison artifact. The key hashes experiment,
config, dataset, code, plugin and tier. A repeated log retains the first result;
partial uploads resume. Concurrent machines can race on search-before-create:
MLflow provides no atomic deduplication or immutable ledger.

## Read results

These commands were exercised with local records and a local MLflow server:

```sh
scripts/eval/results list --experiment public/embedding-comparison-2026-10-03
scripts/eval/results leaderboard --experiment public/embedding-comparison-2026-10-03
```

For your own runs, examples (replace the keys):

```sh
scripts/eval/results compare <candidate-result-key> <baseline-result-key>
scripts/eval/results leaderboard --experiment public/my-experiment --pareto quality,cost,latency
```

Commands emit JSON. Listing and leaderboards return aggregates and lineage only.
Pareto keeps runs that another run does not beat in every chosen objective:
quality is nDCG@10, cost is USD/search, latency is p95 milliseconds. It compares
only equal dataset lineage and tiers, and excludes rows missing a chosen metric.
Paired comparison requires the same dataset fingerprint/split/tier and identical
query ids; historical records without arrays get no invented significance.
The paired Student t-test is two-sided, alpha 0.05, with no multiple-comparison correction.

## Recover an offline run

The outbox defaults to `.scratch/eval/results`. Choose another public outbox with
`--directory` before the command. Files are atomic and owner-only. Preserve this
directory when shutting down a rented worker; copy it to an authorized machine
before replay. When disconnected, listing uses cached aggregates (`source=local`).
Authentication, permissions and invalid input are errors, not offline successes.
Once the tracking credentials are available:

```sh
scripts/eval/results sync
```

A transport failure keeps a record pending. A completed remote upload is skipped
on replay. Records downloaded while listing live separately from pending uploads.

## Keep private query scores private

Set `dataset.private=true`. Public artifacts contain aggregates only; per-query
arrays go to `private/<experiment>` using separate credentials in
`MLFLOW_PRIVATE_TRACKING_URI`, `MLFLOW_PRIVATE_TRACKING_USERNAME` and
`MLFLOW_PRIVATE_TRACKING_PASSWORD`. Its owner must provision that experiment and
ACL first. Ordinary agent credentials must not read it. Without private credentials,
sync refuses private arrays and compare returns aggregate deltas only.

Private offline arrays live in `~/.local/share/quivr/eval-private`, outside the
repository. Keep that directory on a private measurement machine; a shared OS
account is not an access boundary. The Python helper accepts `private_directory`
for an authorized storage location. `Results.get(key, queries=True)` reads arrays
only with the separate private credential. Never move private arrays into the
public outbox or a repository artifact.

## Import existing comparisons

The following import was run locally and retains 15 model measurements:

```sh
scripts/eval/results import docs/dated/evidence/2026-10-03-embedding-comparison \
  --experiment public/embedding-comparison-2026-10-03
```

It never edits dated evidence. These files lack per-query arrays, code SHA,
dataset fingerprint and machine; those values remain unknown. Pooled costs in
the dimensions rerun remain pooled. Query-encoding averages are not p50/p95
search latency, so these historical rows are excluded from that Pareto query.
For new direct comparison files, use `log-direct <file> --experiment <name>`;
they retain real query scores, code, machine and separate indexing/query usage.

## Import engine reports

For a completed `make eval` report, use this example with your report paths:

```sh
scripts/eval/results log-engine report.json --experiment public/engine \
  --lineage lineage.json --public-set miracl-fr --public-set scifact
```

Every set defaults to private; repeat `--public-set` only for known public sets.
The optional lineage JSON contains `machine`, `plugin_digest` and `config` with
the full embedding/chunking/fusion settings used. The original report supplies
code revision, dataset fingerprint, profile, candidate count, reranker, latency,
scored provider accounting and per-query arrays. Unrecorded values remain unknown;
duration describes the whole report. Ingestion costs shared across systems are
retained as contextual accounting, not summed across leaderboard rows. Failed
reports and incomplete sets are rejected. Never include endpoints or secrets
in lineage; keep private reports and outboxes on authorized measurement machines.
Private rows use the [separate experiment and credentials](#keep-private-query-scores-private).
Both sides of `--compare-to` reports are imported. Supply baseline settings under
`lineage.baseline` (`config`, `plugin_digest`, optional `machine`); otherwise they stay unknown.

## Next

- [Measure search quality](agents/evaluation.md) to produce measurements.
- [Deploy the shared store](../deploy/mlflow/README.md) to configure access and backups.
