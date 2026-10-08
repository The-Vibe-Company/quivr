# Confirm a search finalist on the full engine

Use the trusted confirmation runner to measure a synced exploration finalist and
its frozen production baseline on temporary Quivr stacks. A confirmation passes
only when the full-engine held-out measurements pass all four search gates.

## Prerequisites

Use a committed Linux x86_64 checkout, Python 3.12 and an authenticated Modal
account with VM Sandboxes enabled. Install `scripts/eval/requirements-engine.txt`
and `numpy==2.0.2 scipy==1.17.1` for local finalist checks. Provision the [evaluation control
schema](../deploy/mlflow/eval-control.sql) again to add the confirmation tables;
existing campaign rows and frozen policies are preserved. Configure the local
control-store and aggregate MLflow credentials described in [Modal
evaluation](eval-modal.md#prerequisites). Canonical dev evidence must be synced
before dispatch. There is no local admission fallback.

The coordinator provisions a Modal Volume named `quivr-eval-heldout`, mounted at
`/protected`, and a Secret named `quivr-eval-engine-confirmation`. The Secret holds
`EVAL_CONTROL_DATABASE_URL`, optional `EVAL_CONTROL_CA_PEM`,
`AZURE_FOUNDRY_ENDPOINT`, `AZURE_FOUNDRY_KEY`, and `EVAL_CONFIRM_INPUTS`.
The provider endpoint must support Cohere v2 embeddings. Each input reference
names a frozen held-out TREC directory/archive and its disjoint dev directory.
Private input requires an age-encrypted archive, an `age_identity` file reference
and explicit `provider_consent=true`. Keep keys and reference paths in protected
runtime configuration; they never belong in a request, ticket evidence or git.

For example, not run: the Secret's `EVAL_CONFIRM_INPUTS` JSON maps set names to
references under the protected mount. Provision files before dispatch:

```json
{"scifact":{"heldout":"/protected/heldout/scifact.age",
 "dev":"/protected/dev/scifact","age_identity":"/protected/keys/heldout.agekey",
 "provider_consent":true}}
```

This keyless preview was run locally; it creates no Modal resources:

```sh
python scripts/eval/engine_confirmation.py --dry-run
```

## Prepare the trusted configuration

The operator supplies four fields in `confirmation.json`. It contains metadata
and fingerprints, never protected records, query IDs, credentials or locations.

| Field | Meaning |
| --- | --- |
| `heldout_family` | Named set descriptors, optionally inside a full family envelope with `sets` and opaque family metadata. |
| `datasets` | Each set's actual dev TREC `fingerprint` and `split_fingerprint`. |
| `dev_evidence` | Each set's `baseline` and `candidate` canonical SQL lease keys. |
| `mapping_policy` | Explicit frozen production ingestion and bounded Modal resource policy. |

Each set descriptor has `version`, `split="heldout"`, `digest`, `fingerprint`,
and `private` or `privacy="public"|"private"`. Optional `name` is an opaque label;
optional `diagnostic` must match the original campaign policy. `digest` checks
archive/ciphertext bytes, or TREC content for a public directory. `fingerprint`
checks materialized TREC content. Family envelopes may also carry safe
`name`, `version`, `split`, `digest`, `fingerprint`, `private` and `privacy` fields.
The full descriptor is preserved in the immutable policy identity.

Use `trec.fingerprint(directory)` and `engine_measurement.split_identity(dev)[0]`
in the trusted provisioning environment. The latter binds all dev query IDs and
texts internally. The runner rejects dev/held-out overlap by either ID or text,
including a changed ID for the same question.

`mapping_policy.production` contains `ingestion` and `hybrid_fusion`.
Hosted ingestion requires explicit `kind="hosted"`, `max_tokens_per_segment`
and `body_tokens`, plus frozen `batch_size`, `max_batch_tokens`, `request_timeout_ms`,
`call_budget_ms`, `max_concurrent_requests` and `max_retries`. Standard Cohere
input types, empty prefixes and cosine distance are supported; other settings
are refused. The model, deployment revision and dimensions come from each
configuration. Core E5 ingestion instead requires `kind="core"` and the
canonical digest of `plugins/core-ingest/profile.json`; it cannot substitute for
hosted ingestion. Baseline fusion is explicit; candidate fusion is `ranked`.
The final result limit is 10; `candidate_count` sets retrieval depth per space.
`mapping_policy.resources` uses the [smoke resource fields](eval-engine-smoke.md#prepare),
with the same experiment, daily compute cap and rate as the original campaign.
Daily/total provider and compute caps, end time, prices and gates stay frozen.

Supported candidate changes are model/revision/dimensions, `dense_weight` and
`candidate_count`; production segmentation and execution tuning stay fixed.
Hosted Pro/Fast models require explicit revisions and dimensions. Depth above
100, changed exploration character windows/overlap, and Jev reranking return
`unavailable` with a stable refusal code. Character settings are never converted
to token or byte limits. Actual production ingestion settings remain separately
frozen across attempts. A candidate that needs an incompatible ingestion mode
is refused.

## Run confirmation

Coordinator-only example, not run on Modal by the worker:

```sh
python scripts/eval/engine_confirmation.py --allow-paid \
  --campaign example-search --trial 7 --candidate finalist.json \
  --configuration confirmation.json --outbox .scratch/eval/confirmation
```

The campaign must already exist; this command never registers a second campaign.
It checks synced canonical finalist evidence, reserves bounded compute, persists
an app intent, binds its app ID, and dispatches. CI refuses paid confirmation.
Each side ingests once per set. Quality and fresh latency searches use the same
queries and alternate order. Any search error, unknown usage or input mismatch
invalidates confirmation. Images, storage and other Modal account charges remain
outside the runner compute reservation; review rates and external budgets first.

One fenced, single-use SQL admission increments the shared held-out counter
before any protected load/decryption. It covers the whole family and both sides.
Each campaign allows at most 10 admissions, or its smaller configured limit.
Failed openings remain counted. Duplicate delivery with the same owner cannot
open input twice; an expired owner cannot read or publish. New finalists share
the same counter and immutable engine policy.
Retrying failed work acquires a new owner and consumes another admission when
the protected runner opens input; it never restores an earlier ordinal.

## Check the result

`confirmed` and `rejected` exit 0 with `confirmation_available=true`, four gates,
aggregate set measurements and `heldout:{passed:true,fingerprint:...}`.
Here `heldout.passed` acknowledges frozen input integrity; all four gates must
also pass for `confirmed`. Quality requires corrected gain of at least 0.01,
no eligible set may lose significantly, p95 must be within 1.2 times baseline,
and search/index costs must fit the original policy. Policy thresholds override
these defaults. Paired statistics and Holm correction run inside the trusted
runner. Raw query scores, IDs, latency samples and records never reach SQL,
MLflow, stdout or the outbox, including for public held-out data.

The result also contains exact engine/exploration `bindings`, immutable
`confirmation_policy_digest`, `confirmation_key`, `read_ordinal`, aggregate
tracking `receipts`, `compute_ids:{app_id,sandbox_id}` and `cleanup_verified`.
Receipts contain only `result_key`, `run_id` and `status`; promotion requires
`synced`. Pending tracking can be retried by repeating the identical command.
Canonical SQL replay resends aggregates without a new sandbox or held-out read.
`unavailable`, `leased`, `capped` and `failed` exit 2 and cannot authorize promotion.

## Campaign adapter and cleanup

`build_request(store, campaign, trial, candidate, heldout_family, datasets,
dev_evidence, mapping_policy, engine_runner_git_sha)` builds the native request.
`lineage()` returns `engine_runner_scorer_digest` and `engine_source_hashes` from
canonical hashes of actual runner, engine, SDK, plugin and deployment sources.
The scorer digest is opaque and distinct from the exploration scorer algorithm.
Bind it with the committed engine SHA, which can be later than exploration.
`digest(invariants(request, store.policy(campaign)))` is the trusted engine policy
identity; candidate settings and dev evidence are attempt-specific.

`ModalAdapter(store,outbox)(request,resource)` calls
`confirm(store,request,invoke,outbox)`. Resource hooks `app_name`, `on_app(app_id)`
and `check()` provide durable intent, app registration before dispatch and owner
renewal. Campaign translators must validate native bindings before translating
receipt aliases; they supply trusted metadata, never imported success JSON.
Native bindings are the allowlisted `BINDINGS` in the source, not the entire
supervisor request. Canonical replay does not create a remote app.

SIGINT/SIGTERM, lease loss, keepalive expiry and deadlines stop owned resources.
Success requires stack cleanup, observed Sandbox termination and closing the
App scope. The remote supervisor and Sandbox lifetime also bound abrupt
connection loss. Unknown cleanup keeps conservative charges and cannot publish
confirmation. Inspect `outbox/active/` and the standalone
`eval_control.confirmation_apps` intents when a creation response is lost.
Use the [smoke recovery procedure](eval-engine-smoke.md#recover-a-stopped-dispatcher)
to stop only the recorded app/sandbox; never erase uncertain reservations.
Failure output exposes a sanitized class and safe image-build log ID.

## Next

- [Run the full-stack smoke](eval-engine-smoke.md).
- [Query aggregate results](eval-results.md).
