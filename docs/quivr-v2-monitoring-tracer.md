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

### Implemented evaluation (THE-654)

- **Triggers:** the committed `record.retrieval_ready` (a Version's lexical
  baseline became searchable) and `record.enrichment_available` events. Each
  carries an internal, never-exposed Record Version reference, so evaluation
  always targets the exact Version the event concerns and never "the Record's
  current Version at dispatch time", which could reach a Version made
  searchable before activation. Trigger events committed before this schema
  change have no Version reference and are skipped: in an existing evaluation
  database, such events are never evaluated (no backfill).
- **Dispatch:** a per-Organization checkpoint, starting at the first activation
  boundary, advances through committed positions. For each trigger it records
  one durable evaluation intent per enabled Subscription on the event's Corpus
  whose activation position precedes the event, in pages of 100 Subscriptions
  resumed from the checkpoint. Checkpoint and intents commit together, so a
  restart neither loses nor duplicates work. A later enrichment trigger is new
  work for the same Version.
- **Runtime:** evaluation runs in the `worker` process as a PostgreSQL claim
  loop with four concurrent evaluators, separate from future webhook
  delivery. This deliberately departs from "Temporal executes durable work":
  the durable state (checkpoints and intents) lives in PostgreSQL, one-minute
  leases recover work from crashed claims, the commit is idempotent on the
  unique Match identity, and a short in-process evaluator call needs no
  Temporal history. Monitoring therefore keeps evaluating during a Temporal
  outage. The evaluator sits behind `monitoring.EvaluationPort`; a later remote
  evaluator can run as a Temporal activity behind that port without changing
  the commit path.
- **Decisions:** `no_match` and `not_ready` complete the intent without a Match;
  a `not_ready` Version is evaluated again by its next trigger. An evaluator
  error keeps the intent pending with a bounded error code and jittered
  exponential backoff from one second to five minutes; it never becomes a
  negative decision. The worker logs `pending_intents` and `erroring_intents`
  every 30 seconds.
- **Fixture semantics:** the pinned `configuration` is
  `{"decisions": {"<marker>": "<decision>", …, "default": "<decision>"}}`.
  Markers are literal substrings of the Version's text Parts, checked in sorted
  order; the first present marker decides, otherwise `default` (absent:
  `no_match`). Decisions are `match`, `no_match` and `not_ready`, plus two
  fixture-only, test-oriented values: `error` makes the evaluation fail, and
  `match_after_enrichment` is `not_ready` until the Version has embedding
  coverage. Evidence names the marker and the Parts containing it.
- **Atomic commit:** under the Organization journal lock, which disable and
  withdrawal also take, Monitoring rechecks that the Subscription is enabled on
  the pinned Version, that the Record Version is current and eligible (baseline
  ready, not quarantined, no withdrawal or Tombstone) and that its Corpus is in
  the Subscription's scope. It then commits the Match
  (`match_id` derived from Subscription Version + Record Version), its pending
  Delivery, the immutable `match.created` notice bytes, the public event with
  the same `event_id` and `occurred_at`, and the delivery outbox work. An
  existing Match commits nothing.
- **Corrections:** a matching correction Version gets its own plain
  `match.created` Match for now; `previous_match_id`, `match.corrected`,
  `match.no_longer_matches` and withdrawal notices belong to THE-657.
- **Reads:** `GET /v0/matches?subscription_id=` pages by commit order with a
  signed cursor bound to the Subscription and key scope; `GET /v0/matches/{id}`
  and `GET /v0/deliveries/{id}` require `monitoring:read` and a key that
  covers every pinned Corpus, otherwise 404. A new Delivery is `pending` (the
  delivery worker below then attempts it); its admission view reports
  `subscription_disabled` or `record_withdrawn`. Change events for notices
  carry `monitoring` references in both polling and SSE.

### Implemented delivery (THE-655)

- **Runtime:** webhook delivery runs in the `worker` process as its own
  PostgreSQL claim loop over `delivery_outbox`, with two deliverers separate
  from the four evaluators and independent of Temporal, for the same reasons
  as evaluation. Idle deliverers back off from 200 ms to 2 s and reset when
  they find work.
- **Admission before I/O:** under the Organization journal lock, which disable
  and withdrawal also take, the worker rechecks that the Delivery is pending,
  its destination is configured for the Organization, the Subscription is
  enabled and the Record is neither withdrawn nor Tombstoned. An admitted
  attempt commits an append-only attempt fact, the Delivery's move to
  `delivering` and a `delivery.updated` feed event; only then is the HTTP
  request sent. A refused admission records no attempt and parks the work;
  the Delivery stays `pending` and its admission view explains why
  (`subscription_disabled`, `record_withdrawn`, or `destination_unavailable`
  when its destination is no longer configured for the Organization). Claims
  are fenced by their lease, so a step that outlived its lease admits nothing.
  The Match transaction never performs network I/O.
- **Request:** HTTP POST of the stored notice bytes, unchanged, with
  `webhook-id` (the notice `event_id`), a fresh `webhook-timestamp` and its
  `webhook-signature`. The client has a ten-second timeout, follows no
  redirects, keeps no cookies and discards at most 64 KiB of the response,
  which is never stored.
- **Outcome after I/O:** an append-only outcome fact per attempt. 2xx is
  `acknowledged` and marks the Delivery `delivered`. Network errors, timeouts,
  408, 429 and 5xx are `retryable_error`; other statuses, including an
  unfollowed 3xx or a status outside 100-599, are `permanent_error`. A failure
  is never reported as delivered; retry scheduling and exhaustion are
  described under "Implemented retries" below. The Delivery keeps the latest
  outcome (`last_outcome`). Error text is a fixed, bounded message per code
  and never contains the destination URL.
- **Crash recovery is at least once:** if a worker dies between admission and
  outcome, the lease expires and the next claim records that attempt as
  `unknown`, then admits a new attempt for the same notice. That attempt resends
  the same `event_id` and body bytes with a new timestamp and signature, so a
  receiver may see an event it already accepted. A request interrupted by
  worker shutdown is handled the same way rather than recorded as a failure.
  Receivers deduplicate on `webhook-id`.
- **Reads:** `GET /v0/deliveries/{id}` adds `last_error` for a failed latest
  attempt and reports admission `terminal` once delivered;
  `GET /v0/deliveries/{id}/attempts` pages the attempt history (number,
  outcome, HTTP status, bounded error) with a cursor bound to the Delivery and
  key scope. Neither exposes signatures, secrets, receiver bodies or
  destination URLs. `delivery.updated` events are feed-only: they create no
  Delivery, so they never cause a webhook.

### Implemented retries (THE-656)

- **Policy:** after a `retryable_error`, the next attempt waits
  `min(initial·2^(n-1), max)` with equal jitter (uniformly between half and
  all of that step). Defaults are 1 s initial, 5 min cap, 24 h window and a
  10 s request timeout; the worker's `delivery` configuration block
  (`retry_initial`, `retry_max`, `window`, `timeout`, Go durations) overrides
  them. A valid `Retry-After` (delta-seconds or HTTP-date) on 429 or 503
  replaces the backoff, floored at the initial delay; it is ignored on other
  statuses and when invalid.
- **Window:** it starts when the Delivery is created (`deliveries.created_at`;
  Deliveries that existed before this slice start at migration time) and is
  judged on the database clock. The upgrade ends earlier permanent failures
  `exhausted` and makes other parked Deliveries due, so admission is
  rechecked and retryable failures resume within their new window. Every wait, including a long `Retry-After`,
  is capped at the window end, so one final attempt can happen at the edge.
  A retryable failure recorded after the window end ends the Delivery
  `exhausted`; a `permanent_error` ends it `exhausted` at once. Exhaustion
  removes the work, keeps `last_outcome` and `last_error` (then reported as
  not retryable) and appends `delivery.updated`. A crash-recovered `unknown`
  attempt is re-admitted immediately, as before, and its outcome follows the
  same rules.
- **Admission per retry:** every retry is claimed and admitted through the
  same canonical checks as the first attempt, under the journal lock that
  disable and withdrawal also take. Work refused because the Subscription is
  disabled, the Record withdrawn or the destination unavailable makes no
  attempt, stays `pending` and is parked; the admission view says why. An
  attempt admitted before a disable finishes and records its outcome; its
  retry is then refused. A refused Delivery whose window passes stays
  `pending` (disabling fabricates no transport outcome). The window is never
  extended: parked work that becomes claimable after its window end (for
  example through a future re-enable) ends `exhausted` with reason
  `window_elapsed` without an attempt. `superseded` and `access_denied` have
  no producer yet: there is no Subscription reconfiguration or destination
  rights revocation, and newer-Record-Version semantics belong to the
  correction notices (THE-657).
- **Reads:** `GET /v0/deliveries/{id}` adds `next_attempt_at` while the
  Delivery is `pending`, admission is allowed and a retry is scheduled. No
  retry administration or other delivery channel exists.
- **Metrics:** the worker's probe listener serves `GET /metrics` in the
  Prometheus text format: `quivr_delivery_attempts_total{outcome}` counts the
  outcomes (`acknowledged`, `retryable_error`, `permanent_error`) of requests
  this process sent; `unknown` outcomes are inferred from lost leases and read
  from attempt history. The gauges `quivr_delivery_pending` and
  `quivr_delivery_oldest_pending_age_seconds` cover admissible scheduled work only. Parked work refused by admission is
  excluded, so a disabled Subscription's Deliveries never read as stuck work;
  a Delivery disabled while already scheduled is counted until its next
  claim parks it. No identifier is used as a label.

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

## Security and limitations

- **Destinations are configuration only.** Webhook URLs and signing secrets
  come from deployment configuration (`secret_env` for real deployments),
  never from the API, and are validated at startup (http/https URL with a
  host, `whsec_` secret of 24 to 64 bytes). Subscriptions only name a
  configured destination of their own Organization.
- **Residual SSRF exposure.** Delivery does not filter private, loopback or
  link-local addresses and does not pin DNS resolution: an operator-configured
  destination is trusted, and the local harness itself delivers to loopback.
  Refusing redirects keeps a receiver from bouncing requests to another
  address, and response bodies never reach the API. A hosted multi-tenant
  deployment in which tenants influence destination URLs needs an egress
  policy (address filtering after resolution, or an egress proxy) first.
- **Signatures.** Attempts are signed with the destination's own key; the key
  and signatures are never stored in attempt facts or returned by the API.
  Key rotation remains outside this tracer.

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
