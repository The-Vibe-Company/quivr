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
  A withdrawal notice is still committed while disabled and is sent after a
  re-enable (THE-696).
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
version; obtain the pinned evaluator's decision; record a Match and receive its
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
| POST `/v0/saved-queries/{id}/versions` | Edit: commit a new immutable Saved Query Version |
| POST `/v0/saved-queries/{id}/delete` | Logically delete a Saved Query no Subscription uses |
| POST `/v0/subscriptions` | Enable a pinned query/evaluator with one destination, from now |
| GET `/v0/subscriptions/{id}` and `/versions/{version_id}` | Inspect enabled state and immutable configuration |
| GET `/v0/subscriptions?owner=…` | List active Subscriptions of one Subscription Owner, or `none` for global ones |
| POST `/v0/subscriptions/{id}/disable` | Stop new evaluation commits and notification admissions |
| POST `/v0/subscriptions/{id}/enable` | Resume evaluation from now and admission of parked notices |
| POST `/v0/subscriptions/{id}/versions` | Edit: commit a new Subscription Version, effective from its commit |
| POST `/v0/subscriptions/{id}/delete` | Logically delete a Subscription for good; history stays readable |
| GET `/v0/matches?subscription_id=…` and `/v0/matches/{id}` | Read positive Match history and explanation |
| GET `/v0/deliveries/{id}` and `/attempts` | Observe notification state and transport outcomes |

The first tracer references a deployment-configured `destination_id`, bound to
one Organization, URL and signing secret. The local harness configures its one
receiver there. No secret is accepted or returned through these monitoring
resources. A destination registry is a later product feature. Re-enabling a
disabled Subscription on its same Version is implemented (THE-696), and so are
editing by new Versions and logical deletion (THE-724, below).

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

- **Evaluator:** a Subscription Version pins an installed evaluator by
  `plugin_id` and `version`: the `subscription` Contribution of a plugin
  pinned at startup (THE-722), or the deterministic fixture
  `quivr.fixture@1` where a test deployment sets
  `monitoring_fixture_evaluator`. Any other identity is rejected with 422
  `unsupported_evaluator`. The pinned Saved Query Version `expression` and the
  evaluator `configuration` are validated against the plugin's declared
  `expression_schema` and `configuration_schema` when the Subscription or a
  new Subscription Version is created: 422 `invalid_expression` (field
  `/saved_query_version_id`) or `invalid_subscription_configuration` (field
  `/evaluator/configuration/...`), with the first schema issue as the message.
  The `configuration` is pinned verbatim (at most 16 KiB, like the Saved Query
  `expression`) and echoed on every Subscription and Subscription Version read.
- **Destinations:** the deployment configuration's `destinations` map binds each
  `destination_id` to one Organization, URL and signing secret. Real deployments
  set `secret_env` to the name of an environment variable holding the secret;
  `secret` is accepted for local test values only. Secrets are never stored in
  PostgreSQL, logged, accepted or returned. An unknown or other-Organization
  destination is 422 `unknown_destination`.
- **Scope:** writes need `monitoring:write`, reads `monitoring:read`. Creating a
  Saved Query or Subscription over any Corpus the key does not grant, or that is
  not in its Organization, is 403 `forbidden`, as for search. Reads, disable and
  enable of definitions whose pinned Corpora are not all granted, or from another
  Organization, are 404. Only `balanced` is accepted as `retrieval_profile`
  (422 `unsupported_profile`); an unknown Saved Query Version is 422
  `unknown_saved_query`.
- **Idempotency:** route families are Saved Query creation, Subscription
  creation, disable and enable, and, since THE-724, each edit and delete route. The same key and canonical request return the same
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
  loop with four concurrent workers, separate from webhook delivery. This deliberately departs from "Temporal executes durable work":
  the durable state (checkpoints and intents) lives in PostgreSQL, one-minute
  leases recover work from crashed claims, the commit is idempotent on the
  unique Match identity, and a short in-process evaluator call needs no
  Temporal history. Monitoring therefore keeps evaluating during a Temporal
  outage. The evaluator sits behind `monitoring.EvaluationPort`; the plugin
  evaluator (`pluginhttp.Evaluator`) speaks Plugin Protocol v0 behind it
  without changing the commit path.
- **Batching (THE-722):** a worker claims one due intent, then leases the other
  due intents of the same Record Version whose Subscription Version pins the
  same evaluator (at most 64, and at most four times the plugin's
  `max_batch_size`), across Subscriptions and owners. Intents whose Saved
  Query expression and evaluator configuration are identical become one
  evaluation, sent once; its decision fans back out to every intent, and each
  intent then commits exactly as a single evaluation would (Match uniqueness,
  corrections, evidence bounds). Evaluations are sent in calls of at most
  `max_batch_size`, at most four at a time; a request over the protocol's
  16 MiB bound is halved before it is sent. Because the protocol reports an
  error per call, a call that fails with a terminal plugin error or an
  invalid answer is halved until the failing evaluation fails alone (within 18
  calls per batch), so it keeps only its own Subscriptions pending. An
  unavailable or timed-out plugin, or a `retryable` error envelope, fails the
  whole batch, which is retried as one. The call deadline is the declared
  `timeout_ms`, capped at 30 seconds, and a step never outlives its lease.
  The worker's `/metrics` reports `quivr_evaluation_calls_per_record_version`,
  `quivr_evaluation_expressions_per_call` and
  `quivr_evaluation_subscriptions_per_call`.
- **What a rule sees:** the Version's text Parts, whether it is enriched, and
  its metadata: Source Namespace, Record Key and Source Position, acceptance
  time, provenance (origin `client` or `connector`, producer, connector
  instance and kind, normalization) and extensions. The origin is `connector`
  only for revisions accepted under the idempotency-key family reserved to
  Connector Instances.
- **When rules run:** when the Version becomes searchable
  (`record.retrieval_ready`), and again on `record.enrichment_available`. A
  rule that needs enrichment answers `not_ready` until `enriched` is true;
  nothing is evaluated at acceptance time, before the Version is searchable.
- **Decisions:** `no_match` and `not_ready` complete the intent without a Match;
  a `not_ready` Version is evaluated again by its next trigger. An evaluator
  error keeps the intent pending with a bounded error code and jittered
  exponential backoff from one second to five minutes; it never becomes a
  negative decision. A plugin that cannot be reached, times out or serves
  another manifest is `evaluator_unavailable`; a declared plugin error is
  `evaluator_error`; an answer `CheckSubscriptionOutput` refuses (a missing
  or extra decision, evidence out of bounds) is `evaluation_invalid`. A plugin
  outage therefore delays alerts; it never loses or skips them. The worker logs `pending_intents` and `erroring_intents`
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
- **Corrections:** a matching correction Version of an already matched
  Record creates a linked `match.corrected` Match; see "Implemented
  corrections and withdrawal" below.
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
  enabled and the Record is neither withdrawn nor Tombstoned (a
  `match.withdrawn` notice is exempt, and a superseded notice is refused;
  see "Implemented corrections and withdrawal"). An admitted
  attempt commits an append-only attempt fact, the Delivery's move to
  `delivering` and a `delivery.updated` feed event; only then is the HTTP
  request sent. A refused admission records no attempt and parks the work;
  the Delivery stays `pending` and its admission view explains why
  (`subscription_disabled`, `record_withdrawn`, `superseded`, or
  `destination_unavailable` when its destination is no longer configured for
  the Organization). Claims
  are fenced by their lease, so a step that outlived its lease admits nothing.
  The Match transaction never performs network I/O.
- **Request:** HTTP POST of the stored notice bytes, unchanged, with
  `webhook-id` (the notice `event_id`), a fresh `webhook-timestamp` and its
  `webhook-signature`. The client has a ten-second timeout, follows no
  redirects, keeps no cookies, uses no proxy, refuses to connect to private or
  internal addresses (see "Security and limitations") and discards at most
  64 KiB of the response, which is never stored.
- **Outcome after I/O:** an append-only outcome fact per attempt. 2xx is
  `acknowledged` and marks the Delivery `delivered`. Network errors, timeouts,
  408, 429 and 5xx are `retryable_error`; other statuses, including an
  unfollowed 3xx or a status outside 100-599, and a refused destination
  address (`destination_address_refused`), are `permanent_error`. A failure
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
  disabled or deleted, the Record withdrawn or the destination unavailable
  makes no attempt, stays `pending` and is parked; the admission view says why. An
  attempt admitted before a disable finishes and records its outcome; its
  retry is then refused. A refused Delivery whose window passes stays
  `pending` (disabling fabricates no transport outcome). The window is never
  extended: parked work that becomes claimable after its window end (for
  example through a re-enable) ends `exhausted` with reason
  `window_elapsed` without an attempt. The one exception is a withdrawal
  notice committed while its Subscription is disabled, whose window starts at
  the re-enable (see THE-696 below). `access_denied` has no producer yet:
  there is no destination rights revocation.
  `superseded` is described under "Implemented corrections and withdrawal".
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

### Implemented corrections and withdrawal (THE-657)

- **Prior positive Match:** for one Subscription and Record, the latest Match
  by commit position on another Record Version. Follow-up notices use the
  destination pinned by the Subscription Version of the Match they reference,
  which is also the one its Delivery identity names.
- **Positive correction:** under the journal lock and the same guard as any
  Match, a matching correction creates its own Match (identity unchanged:
  Subscription Version + Record Version) with `previous_match_id`, and a
  `match.corrected` notice. That notice carries every `match.created`
  reference (Match, Record and corrected Version, Subscription and Version,
  Delivery) plus `previous_match_id`, so a consumer that never received the
  earlier notice can act on it alone.
- **Negative correction:** only a completed `no_match` decision, never
  `not_ready` or an evaluator error, reaches the negative commit. Under the
  same guard (enabled pinned Subscription Version, current eligible Version,
  Corpus in scope), when the Version has no Match and a prior positive Match
  exists, it commits `match.no_longer_matches` with `match_id` = prior Match
  and `record_version_id` = the correction, its Delivery, event and outbox,
  and records the intent outcome `no_longer_matches`. No Match is created.
  The notice and Delivery are unique per prior Match: a further non-matching
  correction of the same Match commits nothing (`duplicate`). Without a prior
  Match the outcome stays `no_match`.
- **Withdrawal:** the Content transaction intent is the `record.withdrawn`
  event that Withdraw commits with the Tombstone (once per Record). The
  evaluation dispatch checkpoint also consumes it and records one withdrawal
  intent (`evaluation_intents.kind = withdrawal`) per Subscription with a
  Match on the Record, enabled or not (THE-696), in the same paged, checkpointed transaction. No
  Match commits after the Tombstone, so every relevant Match precedes the
  event. The evaluation workers claim withdrawal intents with the same lease
  and backoff and run no evaluator: under the journal lock they require the
  Tombstone and the Record's Corpus in the Subscription's scope (not the
  positive no-Tombstone guard nor the enabled state), then commit
  `match.withdrawn` for the latest positive Match, with that Match's Version,
  once. A disabled Subscription's notice is delivered after re-enable; see
  THE-696 below. Withdraw itself is unchanged, so
  search, matching and ordinary admission stop at once, independently of
  this worker.
- **Identities:** each notice's `event_id` derives from its type and its
  referenced Match, and its Delivery from (Match, destination, type); every
  commit checks for an existing Delivery first, so a repeat commits nothing
  instead of failing. Feed events and webhooks share each notice's ID, type,
  time and references.
- **Admission by notice type:** after the destination check, a disabled
  Subscription refuses every notice (`subscription_disabled`). A withdrawn
  Record refuses `match.created`, `match.corrected` and
  `match.no_longer_matches` (`record_withdrawn`); `match.withdrawn` has its
  own eligibility and is admitted. The worker and `GET /v0/deliveries/{id}`
  share this rule.
- **Superseded:** a notice is superseded by a correction notice for the same
  Subscription and Record committed after it, by `monitoring_notices.position`
  (the journal position of the notice's event, assigned under the
  Organization journal lock), not by change events subject to retention. A
  `match.created` or `match.corrected` is superseded by a later
  `match.corrected` or `match.no_longer_matches`; a `match.no_longer_matches`
  by a later `match.corrected` (THE-694), so a stale invalidation still
  retrying never lands after the Record matches again; `match.withdrawn` is
  never superseded. Deliveries are unordered, so sending a stale notice after
  its correction would contradict what the consumer was just told; the later
  notice names the earlier Match, which stays readable. The SQL reports which
  later kinds exist and `monitoring.AdmissionReason` alone decides which kinds
  they supersede. A superseded Delivery is refused like other refusals: no
  attempt, it stays `pending` with admission `superseded` and its work is
  parked. A re-enable can make it claimable again, but it is refused again
  before its window is checked, so it stays `pending` rather than
  `exhausted`. An attempt already
  admitted finishes, and a delivered notice is unaffected.
- **Negative decisions stay cheap:** a `no_match` for a Record without a Match
  on another Version completes without the journal lock, as before; only a
  possible invalidation takes it. This is safe because a Match on another
  Version commits only while that Version is current, before this Version's
  intent is dispatched.

### Implemented withdrawal notices across disable and re-enable (THE-696)

Withdrawal is the notice whose absence has the highest editorial and legal
impact: a consumer that paused a Subscription must not keep showing an alert
for content withdrawn during the pause. So:

- **Commit whatever the enabled state:** the `match.withdrawn` notice, its
  Delivery, feed event and outbox work are committed for every alerted
  Subscription, disabled at dispatch or at commit alike. Committing a notice is
  not an attempt, so "disable stops new notification attempts" still holds.
  The notice identity is unchanged, so retries, re-dispatch, repeated
  withdrawals and disable/enable cycles commit it at most once.
- **No attempt while disabled:** admission refuses it (`subscription_disabled`)
  like any other notice of a disabled Subscription: it stays `pending`, is
  parked and appears on the feed and on `GET /v0/deliveries/{id}` with that
  admission reason.
- **Re-enable:** `POST /v0/subscriptions/{id}/enable` (`monitoring:write`, its
  own idempotency family, same visibility as disable) keeps the same
  Subscription Version. Under the journal lock it sets the Subscription
  enabled, commits `subscription.enabled` for each pinned Corpus and records
  its journal position, and makes the Subscription's parked Delivery work
  claimable again. Enabling an enabled Subscription commits nothing. Every
  claim is admitted through the usual checks, so a superseded or
  withdrawn-Record notice parks again. Each disable or enable after the first
  has its own event identity.
- **From now:** evaluation resumes after the re-enable's position. A trigger
  at or before it is neither dispatched nor committed, including intents still
  retrying from before the pause, so the pause is never backfilled.
- **Window:** a notice committed while its Subscription is disabled starts its
  delivery window at its first admissible moment, the re-enable, rather than at
  its commit. A withdrawal notice is therefore always sent after a re-enable,
  however long the pause lasted. Every other Delivery keeps THE-656's window
  from its creation, including one that was admissible and then parked by a
  disable: after a long pause it ends `exhausted` with `window_elapsed`
  without an attempt.

### Implemented editing and deletion (THE-724)

Saved Queries and Subscriptions are edited by committing new immutable
Versions, never by changing one. Editing applies from now on: nothing is
backfilled and past Matches keep the Versions that produced them.

- **Saved Query edit:** `POST /v0/saved-queries/{id}/versions` with an
  `idempotency_key` and a full `definition` commits a new Saved Query Version
  (same checks as creation, and the key must grant every Corpus of the current
  and new definitions) and makes it current. The name is unchanged. It moves
  **no** Subscription.
- **Pinning:** a Subscription pins a Saved Query Version explicitly. It moves
  to a newer one only through a new Subscription Version, so an edit of a Saved
  Query never silently changes what a Subscription alerts on.
- **Subscription edit:** `POST /v0/subscriptions/{id}/versions` with an
  `idempotency_key`, the `saved_query_version_id` (the current Version of the
  Subscription's own Saved Query, else 422 `unknown_saved_query`), an
  `evaluator` and a `destination_id` commits a new Subscription Version and
  makes it current. Enabled state is unchanged.
- **Effective position:** like creation, the new Version records the journal
  position of its commit as its `activation_position`. A trigger is judged by
  the Version effective at its position: the latest one activated before it.
  Dispatch picks that Version, so a change committed before the edit but
  dispatched after it is still judged by the earlier Version, and every later
  change by the new one. The commit guard refuses an intent whose Version was
  superseded at the trigger's position (outcome
  `subscription_version_superseded`). A correction notice for a Match of an
  earlier Version (`match.corrected`, `match.no_longer_matches`) is decided by
  the Version effective at the correction and references the prior Match with
  its own Version and destination.
- **Scope across Versions:** candidate enumeration and withdrawal scope cover
  every Corpus any Version of the Subscription pinned; the eligibility guard
  checks the Corpora of the Version that judges the change. A key sees a
  Subscription, its Matches and Deliveries only when it grants every Corpus
  any of its Versions pinned, so narrowing the scope never exposes earlier
  Matches to a narrower key.
- **One alert per content:** a Record Version matched by one Version of a
  Subscription is not matched again by a later one (for example when its
  enrichment trigger arrives after the edit); the commit reports `duplicate`.
- **Version reads:** `GET …/versions/{version_id}` serves every Version of the
  resource, current or earlier. A Saved Query Version read needs the Corpora of
  the current and requested Versions; a Subscription Version read, like every
  Subscription read, needs every Corpus any of its Versions pinned.
- **Subscription delete:** `POST /v0/subscriptions/{id}/delete` (an
  `ActionRequest`) sets `deleted: true` and disables the Subscription in one
  row update under the journal lock, with `subscription.deleted` per Corpus of
  its current Version. Every disable guarantee applies: no new evaluation
  commit, no new Delivery Attempt admission (admission reason
  `subscription_deleted`, parked for good), an attempt already admitted
  finishes. Deletion is permanent: enable and edit then return 409
  `subscription_deleted`; disable is a no-op. The Subscription, its Versions,
  Matches, Deliveries and attempts stay readable.
- **Withdrawal after delete:** a deleted Subscription follows the disabled
  rule of THE-696. The `match.withdrawn` notice for one of its Matches, its
  Delivery and feed event are still committed once, so polling consumers see
  it; as a deleted Subscription is never re-enabled, its window never opens and
  it is never attempted. It stays `pending` with reason `subscription_deleted`
  and does not count as stuck backlog.
- **Saved Query delete:** `POST /v0/saved-queries/{id}/delete` sets
  `deleted: true` with `saved_query.deleted` per Corpus, only when no
  Subscription that is not deleted belongs to it (409 `saved_query_in_use`
  otherwise). A deleted Saved Query gets no new Version (409
  `saved_query_deleted`) and no new Subscription (422 `unknown_saved_query`,
  checked again under the journal lock). Its Versions stay readable.
- **Idempotency:** replaying an edit with the same key and request returns the
  same Version, even after later edits or a deletion; a changed request is 409
  `idempotency_conflict`. Repeating a delete, under any key, commits nothing
  more. Replaying a creation still returns its resource after its Saved Query
  moved on; a new Subscription may only pin the current Saved Query Version.
- **Events:** `saved_query.updated` and `subscription.updated` are committed in
  every Corpus of the previous and new scope, `saved_query.deleted` and
  `subscription.deleted` in every Corpus of the current Version.

### Implemented Subscription Owners (THE-727)

A client application built on Quivr can create alerts for its own end users
while keeping organization-wide ones. Quivr has no end-user accounts: an
Organization authenticates with API keys, and the Subscription Owner is an
opaque reference the client defines. Quivr stores, filters and echoes it and
never interprets it.

- **Creation:** `POST /v0/subscriptions` accepts an optional `owner`, a string
  of 1 to 128 characters without control characters (for example
  `user-123`). `none` is reserved for the listing filter; it and a control
  character are refused with 422 `invalid_owner`, other malformed values with
  422 `invalid_schema`. Without an owner the Subscription is global. The owner
  is part of the idempotent request: a replay with another owner is 409
  `idempotency_conflict`.
- **Immutability:** the owner is fixed at creation. A new Subscription
  Version keeps it and no edit accepts one.
- **Echo:** Subscription and Subscription Version reads, Match reads, newly
  committed notices (`references.owner` in the webhook body) and change-feed
  monitoring references (`monitoring.owner`) carry the owner so the client can
  route an alert. It is absent for a global Subscription, and from notice
  bodies committed before owners existed (their bytes are immutable).
- **Listing:** `GET /v0/subscriptions?owner=<ref>` or `?owner=none` pages the
  active (enabled, not deleted) Subscriptions of that owner, or the global
  ones, in Subscription ID order. `limit` is 1 to 100 (default 100) and the
  signed `page_cursor` is bound to the owner filter and the key scope (409
  `cursor_scope_changed` otherwise). Like every Subscription read, a
  Subscription is listed only when the key grants every Corpus any of its
  Versions pinned. `owner` is required; an unknown or repeated parameter is
  422 `invalid_query`.
- **No business rules:** Quivr is an engine. Per-user limits, quotas, billing
  and plans belong to the application layer above it, which can count a
  user's active Subscriptions with this listing before creating one.

## Evaluation and transactions

The initial adapter consumes pinned query/subscription/evaluator configuration,
an eligible Record Version and authorized input references. It returns `match`
with evidence, `no_match`, or `not_ready`; execution errors remain errors. The
plugin wire protocol behind this seam is the `subscription` Contribution of
Plugin Protocol v0 (`contracts/plugins/v0/README.md`).

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
| Subscription creation/edit/disable/enable/delete | Configuration, enabled or deleted state change, activation boundary when created or edited, public event; commits serialize with Match creation and Delivery admission |
| Positive evaluation | Core eligibility guard, Match or existing Match, logical Delivery, immutable notice/event and outbox work |
| Ordinary correction no longer matching | Core guard on the current correction and enabled Subscription, recorded negative outcome, notice linked to the prior positive Match, unique Delivery and outbox/event; no new Match |
| Withdrawal | Content commits Tombstone, fence, Record event and withdrawal-notification intent; a worker idempotently creates the linked notice/Delivery with its event afterward |
| Delivery admission/outcome | Canonical admission checks and append-only attempt fact before I/O; append outcome and update logical delivery state after I/O |

The ordinary update notice references the prior positive Match for this Record
and pinned Subscription, and uses its pinned destination. This resolves the
single-destination journey without adding destination-reconfiguration policy.
Withdrawal notice creation does not run the positive-Match no-Tombstone guard;
it checks that the Tombstone exists and that the recipient scope remains
eligible, whether or not the Subscription is enabled (THE-696). Content suppression never waits for that worker.

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
`saved_query.updated`, `saved_query.deleted`, `subscription.created`,
`subscription.updated`, `subscription.disabled`, `subscription.enabled`,
`subscription.deleted`,
`operation.updated` and
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
- **Private destinations are refused.** The delivery client checks the
  address it actually dials, after DNS resolution and for every connection,
  so a destination cannot resolve or later rebind to an internal address.
  Unspecified, loopback, private (RFC 1918), ULA, link-local (including
  169.254.169.254 and other cloud metadata addresses), multicast,
  carrier-grade NAT, documentation, benchmarking and reserved ranges are
  refused, as are IPv6 forms that embed an IPv4 address (IPv4-mapped,
  IPv4-compatible, NAT64, 6to4, Teredo). The client uses no proxy, so the
  check always applies to the receiver itself. A refused attempt makes no
  request and records `permanent_error` with code
  `destination_address_refused` and a fixed message; the Delivery ends
  `exhausted`. The refused address appears only in the worker's operator
  log. A destination hostname must resolve only to public addresses: when it
  also resolves to a refused one, an attempt whose public addresses all fail
  can be recorded as refused rather than retryable. At startup, a destination whose host is a literal non-public IP or a
  `localhost` name is rejected with an error naming the destination; other
  hostnames are judged at dial time. The deployment allowance
  `delivery.allow_private_destinations` (default `false`) lifts the refusal
  for the local harness, whose receivers listen on loopback; hosted
  deployments must not set it. RSS feed fetching shares the same guard.
- **Remaining exposure.** A public hostname that the operator configures is
  trusted: the guard does not stop delivery to a public host an attacker
  controls, and there is no egress proxy or per-destination allowlist.
  Refusing redirects keeps a receiver from bouncing requests elsewhere, and
  response bodies never reach the API.
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
