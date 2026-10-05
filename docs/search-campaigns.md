# Run a bounded search campaign

A campaign searches configuration settings over several days, keeping a persistent
quality/cost/latency Pareto front: candidates for which improving one score means
worsening another. It measures public development and private working sets on Modal and records evidence
through the [results store](eval-results.md). A lead can submit bounded proposals,
ingest exact usage receipts and send daily Linear/Slack summaries. Trusted full-stack
confirmation automatically checks configured finalists before a settings PR opens.

## Prerequisites

Use Python 3.12 in a committed repository checkout and install
`scripts/eval/requirements-campaign.txt`. The operator needs Modal access, the
[measurement secrets and cache](eval-modal.md), and two database URLs injected by a
secret manager:

- `EVAL_CONTROL_DATABASE_URL`: the evaluation control database with the role granted
  by `deploy/mlflow/eval-control.sql`.
- `EVAL_STUDY_DATABASE_URL`: PostgreSQL URL for a separate login granted
  `quivr_eval_optuna` by `deploy/mlflow/optuna.sql`. The supervisor initializes
  Optuna's tables in `eval_optuna`; the role needs CREATE only in that schema.

Apply both SQL files as the database owner. Grant the control login
`quivr_eval_control` (USAGE plus SELECT/INSERT/UPDATE in `eval_control`). Remote URLs require `sslmode=verify-full`.
If necessary, inject `EVAL_CONTROL_CA_PEM` for the database CA; the study adapter uses
that CA too. The operator supplies aggregate MLflow credentials for result uploads.
The aggregate MLflow variables are `MLFLOW_TRACKING_URI`,
`MLFLOW_TRACKING_USERNAME` and `MLFLOW_TRACKING_PASSWORD`. Modal uses its standard
CLI authentication. Its named secrets are `quivr-eval-results` (database/MLflow),
`quivr-eval-embeddings` (`AZURE_FOUNDRY_ENDPOINT`, `AZURE_FOUNDRY_KEY`) and, for Jev,
`quivr-eval-rerank` (`TYPESAFE_API_KEY`); see the linked measurement guide for setup.
Keep all credentials outside the campaign file and repository.

Run a supervisor and a separate watchdog under a process manager that restarts them.
They can run on any machine with this checkout, Python, Modal CLI/network access and
both databases. Preserve the committed checkout across restarts. Neither process
needs an engine stack locally. Measurement machines install the Modal requirements.
Automatic confirmation also needs the Go toolchain specified in `go.mod` to build
and run the hosted plugin's offline configure command before dispatch.

## Validate your campaign

Copy `scripts/eval/examples/search-campaign.yaml` to an ignored operator file.
Set an actual end timestamp, deployment revision, opaque ticket reference and caps.
The example includes public SciFact, MIRACL-fr and FiQA. The daily provider cap is
USD 1 and Modal cap USD 10; total caps are USD 3 and USD 30. They are conservative
reservation caps, not estimates of total vendor invoices. Unknown charges remain
reserved. Modal builds, storage and other account charges need separate budgeting.

To include a private set, add its encrypted working descriptor to `policy.sets`
and give every eligible private set a `goal.weights` entry. Follow
[private working-set setup](eval-private-working.md) to upload only
working ciphertext to a private Volume and provision its working-key Secret.
Supply `EVAL_WORKING_RUNTIME` on the supervisor machine for start and resume;
keep paths and identities outside the YAML. Validation needs no resources and
refuses held-out descriptors. Private trials expose aggregate paired evidence,
consume no held-out reads and still need confirmation before promotion.

The baseline must explicitly name Cohere-Embed-V5-Pro at 1024 dimensions with
`dense_weight: 0.5`. Match the production settings you intend to confirm. Character windows in exploration do not equal token/byte
windows in the engine. Tier 1 uses direct ranking, not the complete Quivr stack.

This command runs without keys or network:

```sh
python3 scripts/eval/search_campaign.py validate scripts/eval/examples/search-campaign.yaml
```

It prints the normalized spec. Unknown fields, invalid ranges, unsupported
configurations and non-development splits are rejected. `goal.weights` names every
eligible set; diagnostic sets are excluded from the quality objective. The objective
maximizes weighted nDCG@10 and minimizes worst-set serving cost and measured p95
latency. Gate failures stay on the Pareto front for inspection; they cannot qualify
for promotion. Trials missing measurements never get synthetic scores.

The YAML supports `choices` or `low`/`high`/`step` distributions over supported search
configuration keys. `parallelism` is 1–4; `max_trials` counts all Optuna trials,
including failed ones. `confirmation_limit` can lower the maximum of 10 held-out
reads. Exploration never consumes a held-out read.

## Start and recover

Examples requiring operator credentials and rented compute; not run during development:

```sh
python3 scripts/eval/search_campaign.py start .scratch/pilot.yaml --allow-paid
python3 scripts/eval/search_campaign.py watchdog public-search-pilot --allow-paid
python3 scripts/eval/search_campaign.py status public-search-pilot
python3 scripts/eval/search_campaign.py resume public-search-pilot --allow-paid
```

Run the watchdog in its own managed process. `start` registers an immutable spec,
code revision, scorer and dataset registry. `resume` requires that frozen checkout.
A second live supervisor is refused. An expired supervisor is replaced only after
its compute has been reconciled. Results saved before an interrupted Optuna update
are replayed without re-measuring; measurement leases reuse canonical baseline and
candidate evidence. An incomplete run is retried only after old compute is stopped.

Daily exhaustion cancels active compute and pauses until the next UTC day.
A running supervisor resumes automatically that day; a dead one needs managed
restart or `resume`. Ownership leases last 120 seconds and renew every 10 seconds. Total
exhaustion, the end timestamp, an operator stop or the trial limit ends the campaign.
SQL admission checks every paid reservation against the same shared caps. The
watchdog checks independently of a blocked measurement. Trials reserve cache entries
in SQL before validating files, so file reads do not hold the campaign row lock.
Unpaid cache reservations expire within ten minutes if release fails; leases extend
for paid work only after validation. SQL lock conflicts and timeouts retry with
exponential backoff from 1 to 10 seconds within a 30-second retry window.
Persistent contention defers supervisor/watchdog passes. Their `--once` mode returns
`status: retrying` and exit 0; other incomplete commands return `retrying` and exit 2.
Normal processes continue when contention clears. An unreachable database refuses
further paid admission; there is no local budget fallback.

`status` returns aggregate trial reports, the Pareto trial numbers/objectives,
confirmed plus uncertain ledger amounts, held-out reads left and `cleanup_pending`.
No raw records, query IDs or latency sample IDs are exported. `agent_token_usage`
is `null` (unknown) without exact receipts; no token usage is estimated.

## Stop and verify cleanup

Example requiring Modal and control-store credentials; not run during development:

```sh
python3 scripts/eval/search_campaign.py stop public-search-pilot --allow-paid
```

Stop first closes paid admission, then terminates only the registered campaign apps.
Launch intents allow discovery if the process died before saving an app ID. A lost
creation acknowledgement remains pending even past the container startup window;
absence from a listing cannot prove a creation RPC failed. Retry discovery; if no
app becomes visible, an operator must resolve the provider uncertainty. Leases expire
only after compute termination is acknowledged. Uncertain charges and canonical
results are retained. `cleanup_pending: false` is the observable cleanup result.

A cancellation/listing failure returns `cleanup_pending` and a nonzero exit code.
Retry `stop` or let the managed watchdog retry. Do not report success while cleanup
is pending. SIGTERM and Ctrl-C request terminal stop; a hard kill is reconciled by
the independent watchdog after the supervisor lease expires. Never use a global
Modal stop: other campaigns may share the account.

## Next

Confirm exploration finalists on the complete stack before changing deployed settings.
Until the confirmation integration is configured, `confirmation_available` is false
and this command opens no promotion PRs. Configure frozen confirmation metadata with
`--confirmation-configuration` on `start` or `resume`; see [report and review candidates](search-campaign-reporting.md)
for notification credentials, exact receipts, proposals and the confirmation hand-off.
