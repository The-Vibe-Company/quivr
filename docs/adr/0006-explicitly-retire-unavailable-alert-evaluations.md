# Explicitly retire unavailable alert evaluations

Date: 2026-10-01

Status: accepted

A Subscription Version is an immutable alert configuration that pins an alert-rule plugin version. Migrating a Subscription creates a new Version for future changes; evaluation intents already queued against the old Version keep its pin. If an image stops shipping that plugin version, those intents keep retrying with `evaluator_unavailable`. [THE-897](https://linear.app/thevibecompany/issue/THE-897) adds an explicit way to abandon them without changing their meaning.

## Decision

Never change queued Subscription Version pins. Missing evaluators keep retrying as before; migration, activation and deployment do not automatically retire evaluations. An operator chooses between restoring the exact old implementation to finish its work and explicitly retiring a bounded batch, accepting that potential Matches will be lost.

`POST /v0/admin/subscriptions/evaluation-retirements` accepts `key`, `plugin_id`, `version`, `reason`, `dry_run` and `limit` (default 100, maximum 500). It requires `plugins:admin` and operates only within the API key's Organization and Corpus grants. It closes only pending evaluation intents pinned to that exact plugin version whose latest recorded error is `evaluator_unavailable` and whose leases have expired. Live leases and other errors are not eligible.

Each closed intent has the terminal outcome `evaluator_retired`, never `no_match`. Retirement does not run the alert rule and creates no Match or Delivery. Each closed intent emits `evaluation.retired`, with `resource.kind=evaluation_retirement` and `resource.id=retirement_id`.

### Receipts and dry runs

The response preserves the full request body with a normalized `limit`, plus `retirement_id`, `outcome`, `items`, `remaining` and `leased`. Each item preserves its original `subscription_id`, `subscription_version_id`, `sequence`, `corpus_id`, `record_id` and `record_version_id`. A real retirement also records `created_at` and each item's `event_id`.

`remaining` counts eligible unavailable intents left after the batch, including those still leased; `leased` counts the ones with live leases. A dry run reports the selected items and the remainder as if that batch were closed, without recording events, changing intents or reserving the key. It has no `created_at` or item `event_id`. The same or a different key can be used for the real request.

Within an Organization, replaying a real key with the same normalized request and original Corpus scope returns the original durable receipt, not a fresh batch or fresh counts. A different request or scope with that key returns `409 idempotency_conflict`. Even an empty real batch reserves its key and stores a receipt, initializing the Organization journal if needed. Dry runs never initialize it.

`GET /v0/admin/subscriptions/evaluation-retirements/{retirement_id}` reads that receipt with `plugins:admin` and grants covering the original request's Corpus scope, not just the Corpora of selected items. A missing or hidden receipt returns `404`.

### Backlog and draining

`GET /v0/admin/subscriptions/evaluation-backlog` requires `plugins:admin` and uses the API key's Organization and Corpus scope. It returns paged `items`, each containing `plugin_id`, `version`, `pending`, `erroring`, `unavailable` and `retired`, plus `next_after` when another page exists. The query accepts `limit` up to 500 and `after`, the last `plugin_id@version` returned.

`pending` counts unfinished evaluations; `erroring` counts pending evaluations with a recorded error; `unavailable` counts pending evaluations whose latest error is `evaluator_unavailable`; `retired` counts evaluations closed with `evaluator_retired`. These are recorded states, not probes of current endpoint health. Restoring an endpoint does not clear an error until work records a new result.

Retirement is a bounded batch, not a permanent policy against a plugin version. Old triggers not yet dispatched can still fan out into pinned evaluations. Operators must wait for live leases, let dispatch progress, and repeat dry-run/real batches with new keys until `pending` and `unavailable` are zero. Other errors must be resolved separately. Replaying a key cannot close later arrivals.

## Considered Options

- **Keep the old implementation running until drained.** This is the zero-loss upgrade path. Run exact old and new implementations concurrently, register, certify and activate the new one, then dry-run and migrate Subscriptions. Keep the old endpoint until registry `subscriptions` and `pinned_work` are both zero, including undispatched triggers. For plugins baked into an image, retain and run the old code separately; never relabel the new implementation as the old version.
- **Move queued evaluations to the new version.** Rejected: it would judge already accepted work under a rule it did not pin and would break future-only Subscription edits.
- **Automatically retire on activation or endpoint failure.** Rejected: temporary unavailability is not consent to lose alerts. Activation alone cannot establish that an old implementation is gone permanently.
- **Record `no_match` for unreachable evaluators.** Rejected: no rule ran, so the engine has no evidence that the Record Version did not match.

## Consequences

- Operators can intentionally end an unavailable version's backlog with a reason, a durable receipt and per-intent events. Earlier Matches and their Delivery history are unchanged.
- Retirement abandons potential Matches. The separate terminal outcome keeps that loss visible instead of presenting it as an evaluated non-match.
- Migration remains future-only. Registry drain checks must cover both current Subscriptions and pinned work; a retirement receipt or a zero-count snapshot alone is not proof that dispatch has drained.
- The [Railway deployment guide](../../deploy/railway/README.md) describes overlapping implementations; the [alert guide](../../docs-site/guides/keyword-alerts.mdx) describes the operator recovery procedure. Generated API references remain sourced from the HTTP contract.
