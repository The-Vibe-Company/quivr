# Thin monitoring tracer and public change feed

Status: assembled contract, tracked in [THE-547](https://linear.app/thevibecompany/issue/THE-547).
This is a foundation decision, not implementation of the full monitoring product.

## Accepted boundary

The matching criterion belongs to a plugin and its algorithm is deliberately
deferred. The core does not choose keyword matching, a semantic threshold, or a
ranking cutoff as the product's alert rule. The existing `subscription` plugin
contribution is the extension point; no new plugin category is needed.

Monitoring retains ownership of scheduling evaluations, pinned Subscription and
Saved Query Versions, authorization/currentness checks, durable Match identity,
and atomic creation of the Match with its logical Delivery intent. A plugin
result does not grant access or authorize publication of a stale version.

An evaluation port will let the foundation harness supply deterministic fixture
results. This proves orchestration, recovery and notification behavior without
claiming to validate production relevance. Exact native plugin schemas, remote
execution and plugin lifecycle belong to the plugin work, rather than becoming
requirements of this tracer.

## Existing rules carried forward

- Only the active Subscription Version is newly evaluated. Activation starts
  from now; historical evaluation is a separate explicit Backfill.
- Evaluate eligible newly searchable Record Versions and relevant later
  enrichment. A retry or reevaluation converges on the same Match identity:
  `(subscription_version, record_version)`.
- A first successful result after enrichment may create a Match. Subsequent
  positive results for the same version cannot create another logical alert.
- A correction that still matches creates a new version's Match linked to its
  predecessor, according to the accepted source-correction policy.
- Match plus logical Delivery intent commit atomically in Monitoring. Network
  delivery and its append-only attempts happen afterward, with independent retries.
- Every delivery admission rechecks current rights, whether the Subscription is
  enabled, and withdrawal eligibility. Disabling the Subscription stops new
  evaluations and new notification attempts, including retries of pending work.
  Already admitted/in-flight requests may finish; history remains available.
  A withdrawn Record cannot generate another ordinary content notification;
  a dedicated withdrawal notification has different eligibility.
- Polling and SSE use the same opaque committed Change Cursor and the
  [accepted resynchronization contract](quivr-v2-ingestion-contracts.md).

## Corrections and withdrawal

When an already-alerted Record's correction no longer matches, send a notification
linked to the previous alert stating that it no longer matches. Preserve the old
Match as immutable history; do not create a Match for the nonmatching correction.

When an already-alerted Record is withdrawn, send a linked withdrawal notification.
The Tombstone immediately suppresses ordinary search/matching/notification
admission, regardless of when this separate notification reaches its destination.
No physical purge or recall of an already transmitted webhook is implied.

A pending, unavailable or failed evaluator result is not evidence that a
correction no longer matches. The integration must distinguish a negative
decision from evaluation that could not complete; retain the pending work and
its diagnostics rather than sending a false invalidation.

The ordinary correction and withdrawal paths retain the shared unique
`(match, destination, event_kind)` Delivery identity. Successive corrections do
not introduce a dedicated policy or required fixture in the first tracer.

## Reference-only webhooks

Webhooks carry the event type and identifiers needed to inspect the Match and
Record through authorized API reads, alongside the already required event ID,
schema version and timestamp. They do not embed title, document content, excerpts
or the plugin's full justification. Match reads expose the available explanation
and provenance under current access checks.

Delivery is signed and at least once. Repeated attempts preserve `event_id` so
consumers can deduplicate. A transport failure retries Delivery, not evaluation;
it never recreates the Match. Ordering across separate deliveries must not be
assumed by consumers unless an explicit later contract provides it.

## Responsibility sequence

```mermaid
sequenceDiagram
    participant C as Content
    participant M as Monitoring
    participant E as Evaluation adapter
    participant D as Delivery
    participant W as Webhook receiver
    C->>C: Commit searchable version and change/outbox intent
    C-->>M: Eligible Record Version changed
    M->>E: Evaluate pinned query/subscription against eligible input
    E-->>M: Decision or explicit inability to complete
    M->>M: Recheck rights, currentness and active Subscription Version
    M->>M: Atomically commit Match and Delivery intent when matched
    M-->>D: Durable notification work
    D->>D: Check enabled Subscription and eligibility; admit attempt
    D->>W: Signed reference-only event
    W-->>D: HTTP result
    D->>D: Append outcome; retry transport when appropriate
```

Corrections that cease to match and withdrawals use their own linked notification
intent path; the diagram's Match-creation transaction is not reused to fabricate
a new Match for them.

## First proof

The reference journey stays small: create a Saved Query and an enabled
Subscription with one webhook destination; ingest text; observe an eligible
version; obtain a fixture evaluator's decision; record a Match and receive its
signed reference-only notification. A temporary webhook failure exercises retry
without repeating evaluation or creating another logical Delivery.

The companion scenarios cover an ordinary correction, a withdrawal, and disabling
the Subscription while a notification is waiting to retry. They validate the
agreed behavior without a matrix of successive source corrections or destination
reconfiguration histories.

## HTTP surface

The [shared OpenAPI](../contracts/http/v0/openapi.yaml) defines these routes.
All read/write operations require `monitoring:read` or `monitoring:write`,
respectively, plus the relevant Organization/Corpus authorization. Existing
structured errors, per-route-family idempotency and opaque pagination apply.

| Route | Purpose |
| --- | --- |
| POST `/v0/saved-queries` | Save a definition and its first immutable version |
| GET `/v0/saved-queries/{id}` and `/versions/{version_id}` | Inspect current and pinned definitions |
| POST `/v0/subscriptions` | Enable a pinned query/evaluator with one destination, from now |
| GET `/v0/subscriptions/{id}` and `/versions/{version_id}` | Inspect enabled state and immutable configuration |
| POST `/v0/subscriptions/{id}/disable` | Stop new evaluation commits and notification admissions |
| GET `/v0/matches?subscription_id=…` and `/v0/matches/{id}` | Read positive Match history and explanation |
| GET `/v0/deliveries/{id}` and `/attempts` | Observe notification state and transport outcomes |

The first tracer references a deployment-configured `destination_id`, bound to
one Organization, URL and signing secret. The local harness configures its one
receiver there. No secret is accepted or returned through these monitoring
resources. A destination registry, editing/re-enabling subscriptions and changing
recipient routes are later product features, not required to prove this flow.

A Saved Query Version contains Corpus IDs, a plugin-interpreted expression,
retrieval profile and `from_activation` temporal policy. A Subscription Version
pins that version, evaluator implementation/version/configuration, and destination.
Create replies expose generated IDs and pinned versions. A replay never enables
a Subscription that has since been disabled.

A Match names its immutable Record Version, Subscription Version and Saved Query
Version, plus evaluator provenance and bounded explanation. A correction Match
may name `previous_match_id`. These are historical positive facts; Match reads
do not assert that the Record still corresponds or is currently searchable.
Current authorization is checked again before exposing explanations or content.

### Implemented definitions (THE-653)

- **Evaluator:** the only installed evaluator is the deterministic fixture,
  `plugin_id: "quivr.fixture"`, `version: "1"`. Any other identity is rejected
  with 422 `unsupported_evaluator`. Its `configuration` is pinned verbatim
  (at most 16 KiB, like the Saved Query `expression`) and echoed on every
  Subscription and Subscription Version read; its semantics belong to the
  evaluation slice.
- **Destinations:** the deployment configuration's `destinations` map binds each
  `destination_id` to one Organization, URL and signing secret. Real deployments
  set `secret_env` to the name of an environment variable holding the secret;
  `secret` is accepted for local test values only. Secrets are never stored in
  PostgreSQL, logged, accepted or returned. An unknown or other-Organization
  destination is 422 `unknown_destination`.
- **Scope:** writes need `monitoring:write`, reads `monitoring:read`. Creating a
  Saved Query or Subscription over any Corpus the key does not grant, or that is
  not in its Organization, is 403 `forbidden`, as for search. Reads and disable
  of definitions whose pinned Corpora are not all granted, or from another
  Organization, are 404. Only `balanced` is accepted as `retrieval_profile`
  (422 `unsupported_profile`); an unknown Saved Query Version is 422
  `unknown_saved_query`.
- **Idempotency:** route families are Saved Query creation, Subscription
  creation and disable. The same key and canonical request return the same
  resource in its current state; changed input is 409 `idempotency_conflict`.
  Names are immutable. Replaying Subscription creation after disable returns
  `enabled: false`. Disabling an already disabled Subscription succeeds without
  another event.
- **Activation boundary:** Subscription creation takes the Organization journal
  lock and commits the Subscription, its Version and `subscription.created` in
  one transaction. The Version records the journal position of that commit;
  every earlier change has a lower position and every later change a higher
  one, so evaluation considers only positions after it and never scans history.
  Disable updates the Subscription row under the same lock, so later Match and
  Delivery admission can serialize with it.
- **Per-Corpus events:** the change feed is read per Corpus. One logical
  monitoring fact (`saved_query.created`, `subscription.created`,
  `subscription.disabled`) is therefore committed as one event for each pinned
  Corpus. These events share the resource kind and ID and have distinct stable
  per-Corpus event IDs; consumers deduplicate by resource, not event ID.

## Evaluation and transactions

The initial adapter consumes pinned query/subscription/evaluator configuration,
an eligible Record Version and authorized input references. It returns `match`
with evidence, `no_match`, or `not_ready`; execution errors remain errors. This
logical seam is sufficient for the fixture evaluator. It does not freeze the
production plugin's wire protocol or matching algorithm.

On a `record.searchable`, `record.corrected` or relevant
`record.enrichment_available` event, enumerate active scoped Subscriptions in
bounded pages. All scoped candidates are considered in the first tracer; no
specialized reverse index or lossy top-N candidate selection is required. The
evaluation worker uses bounded concurrency separate from webhook delivery.

Subscription activation records a boundary in the same commit-ordered
Organization stream. Only later eligible changes are evaluated. This includes
later enrichment of existing content; it does not scan earlier history.
Dispatch checkpoints and durable work intents prevent loss across process
restarts. Repeated evaluation work does not change the unique Match identity.

| Boundary | Facts committed together |
| --- | --- |
| Subscription creation/disable | Configuration or enabled-state change, activation boundary when created, public event; commits serialize with Match creation and Delivery admission |
| Positive evaluation | Core eligibility guard, Match or existing Match, logical Delivery, immutable notice/event and outbox work |
| Ordinary correction no longer matching | Core guard on the current correction and enabled Subscription, recorded negative outcome, notice linked to the prior positive Match, unique Delivery and outbox/event; no new Match |
| Withdrawal | Content commits Tombstone, fence, Record event and withdrawal-notification intent; a worker idempotently creates the linked notice/Delivery with its event afterward |
| Delivery admission/outcome | Canonical admission checks and append-only attempt fact before I/O; append outcome and update logical delivery state after I/O |

The ordinary update notice references the prior positive Match for this Record
and pinned Subscription, and uses its pinned destination. This resolves the
single-destination journey without adding destination-reconfiguration policy.
Withdrawal notice creation does not run the positive-Match no-Tombstone guard;
it checks that the Tombstone exists and that the Subscription and recipient scope
remain eligible. Content suppression never waits for that worker.

## Events

Every notice below has a distinct immutable `event_id`; retrying its Delivery
preserves that ID and body. For monitoring notices, the polling/SSE event carries
the same ID/type/time and `monitoring` references as the webhook. Its normal
`resource` is the referenced Match. The webhook does not include a Change Cursor.

| Notice | Referenced Match | Record Version reference |
| --- | --- | --- |
| `match.created` | New positive Match | Matched version |
| `match.corrected` | New positive Match, with predecessor | Corrected matching version |
| `match.no_longer_matches` | Prior positive Match | Nonmatching correction |
| `match.withdrawn` | Prior positive Match | That Match's version; the Record carries withdrawal state |

Public feed events also cover committed `corpus.created`,
`corpus.retrieval_changed`, `receipt.pending`, `receipt.resolved`,
`record.materialized`, `record.searchable`, `record.corrected`,
`record.enrichment_available`, `record.withdrawn`, `saved_query.created`,
`subscription.created`, `subscription.disabled`, `operation.updated` and
`delivery.updated` transitions. A replay or duplicate no-op emits no new fact.
Delivery status events are feed-only; they never trigger another webhook.
Every catalog mutation retains the Record invalidation required by THE-543;
Receipt/Operation events cannot replace it. Internal heartbeat/retry mechanics
are not public Change Events.

The initial public event-retention default is seven days, configurable by the
installation. Cursors expire explicitly under the existing `/changes` contract.
Event bodies remain compact resource references and never become a second
content store. Scope changes invalidate cursors as already specified.

## Signing and retry defaults

Use the symmetric scheme from
[Standard Webhooks 1.0.0](https://github.com/standard-webhooks/standard-webhooks/blob/7537d2a2d3d52d8f2e0ecd12527af4a9307fd81b/spec/standard-webhooks.md):
HMAC-SHA256 of `webhook-id.timestamp.raw-body`, with headers `webhook-id`,
`webhook-timestamp` (Unix seconds) and `webhook-signature` (`v1,` plus base64 MAC).
The destination has its own randomly generated 32-byte secret, serialized with
the `whsec_` prefix and base64. Receivers verify the raw received bytes before
parsing, use constant-time comparison and reject attempt timestamps outside a
five-minute tolerance. Attempt timestamps/signatures refresh on retry; the event
ID and stored body bytes do not change. Key rotation management is outside this
tracer. This selects the signature scheme, not all optional standard features.

Use HTTP POST with JSON. A 2xx response acknowledges the event. Network errors,
timeouts, HTTP 408, 429 and 5xx are retryable; other responses end automatic
attempts. Do not follow redirects. The initial policy has a ten-second request
timeout, exponential jittered retry starting at one second and capped at five
minutes, with a 24-hour delivery window. Respect valid Retry-After for 429/503
within the remaining window. These are configurable evaluation defaults.

Existing Delivery states remain `pending`, `delivering`, `delivered` and
`exhausted`. Current `admission` is a separate read view: disabled, withdrawn,
superseded or inaccessible work makes no new attempt. Workers stop scheduling
ineligible work; an already admitted request can finish. Attempt reads expose
bounded outcome/error/status details without signatures, keys or receiver bodies.
Exhausted work remains inspectable; retry administration is later work.

## Verification and handoff

The [transport verification guide](../contracts/http/v0/README.md) and fixtures
cover schema validity and generated Go/Python/TypeScript representations. The
signature fixture checks the documented bytes and signature changes on tampering. These are
contract checks; they do not claim a running webhook or monitoring engine.

THE-548 uses the reference journey above to specify the public black-box harness;
THE-550 runs the integrated proof. Their runtime checks exercise ordinary
correction/withdrawal, retry/idempotence and disabling queued notifications.
They do not require a production relevance algorithm or successive-correction
matrix before the initial path can be demonstrated.

Production matching algorithms, digesting, advanced scheduling, many delivery
channels, plugin management, historical Backfills and a monitoring UI remain
outside this decision. They are not reopened by the plugin-owned criterion.
