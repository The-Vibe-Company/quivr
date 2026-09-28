# Quivr V2 — Local harness and operational baseline

Decision ticket: [THE-548](https://linear.app/thevibecompany/issue/THE-548).

Status: proposed handoff for THE-550. Stan accepted correlated logs, a few
metrics and an error report as the initial diagnostics; distributed tracing and
dashboards are deferred. This document specifies the harness, not an implemented
stack or a completed end-to-end proof. Commands below are implementation targets.

## Scope and inherited decisions

Start the reference stack with one command and verify behavior through public
HTTP contracts and captured webhooks. Reuse the accepted
[module boundaries](quivr-v2-module-boundaries.md),
[ingestion contract](quivr-v2-ingestion-contracts.md) and
[monitoring tracer](quivr-v2-monitoring-tracer.md).

The [THE-549 spike](https://github.com/The-Vibe-Company/quivr-v2/tree/4196f51/prototype/runtime-spike)
provides recovery scenarios and operational evidence. Its tests also inspect
PostgreSQL, Temporal and Weaviate, so they are not the public acceptance suite.
Its fake embeddings and external fake normalizer are not the reference baseline.
Use THE-553's [pinned local E5/TEI profile and CC0 fixture](https://github.com/The-Vibe-Company/quivr-v2/blob/9e59d3bf12afe5d20ce1b0afd5775e464b2ebddf/research/text-segmentation-embedding-profile.md).
The monitoring predicate remains a deterministic fixture adapter; production
matching belongs to a future plugin.

## Commands and isolation

| Target | Required behavior |
| --- | --- |
| `make dev` | Build or obtain pinned artifacts, start dependencies, run initializer, then API/worker; return only when startup checks succeed |
| `make verify` | Run static/contract checks and tests in an isolated Compose project; collect a report and return nonzero on failure |
| `make down` | Stop the development project and preserve its data volumes |
| `make reset` | Explicitly remove only this project's local data and initialize again |
| `make migrate` | Run the versioned initializer against the selected project; report failures and required restarts |

`verify` uses its own project name, volumes, network, fixture identities and
temporary credentials. It must not reuse or reset the developer's stack. Publish
development ports on loopback; verification runs its runner inside the project
network and needs no fixed host ports. A run ID distinguishes artifacts and
concurrent projects. On success or failure, collect artifacts before cleaning up
only that run's containers and volumes. Interrupt handling follows the same rule.
An explicit keep-on-failure option may preserve that isolated run for inspection.

The initial build may need network access to fetch pinned tools/images/model
weights. Once materialized, the test path uses local services and requires no
hosted model API key. Cache misses fail clearly; never silently replace the real
embedding profile with fake vectors. Record image digests, model revision and
tool versions in the report. THE-550 must measure cold preparation separately
from warm startup; this design makes no startup-time or hardware-capacity claim.

## Services, configuration and startup

| Service | Role and startup check |
| --- | --- |
| PostgreSQL | Canonical store; authenticated connection and query succeed |
| Temporal local server | Durable execution; configured namespace is reachable |
| SeaweedFS S3 profile | Immutable bytes; authenticated bucket access succeeds |
| Weaviate | Rebuildable search; server is ready and bootstrap can access the selected collection |
| Local TEI | THE-553 model/tokenizer loaded; bounded inference probe returns the selected vector dimension |
| `quivr migrate` | One-shot numbered migrations and idempotent storage/projection/bootstrap; successful exit gates first startup |
| `quivr api` | Same core image as worker/initializer; configured, expected schema available, canonical DB reachable |
| `quivr worker` | Connected to Temporal and required adapters; processing, indexing, monitoring and delivery loops running |
| Test webhook receiver | Verification fixture; private control interface ready and capture store initialized |

Keep the accepted dependency versions/profiles, pinned by digest when composing
the implementation. Do not use floating image tags. API and worker do not race
to initialize tables or collections. A failed initializer stops first startup
with its error preserved. The harness waits with bounded deadlines and reports
which check failed; no unbounded sleep loop.

Separate process liveness from dependency readiness. Liveness means the process
can serve its probe; dependency loss must not turn it into an automatic restart
loop. API readiness reflects its ability to accept durable commands through
PostgreSQL, not the health of every asynchronous stage. A Temporal/search/model
outage must remain observable without falsely declaring accepted work lost.
Search or upload operations can still fail according to their own dependencies.
Worker readiness reports disconnected pollers/adapters and does not erase pending
work. `make dev` checks the whole initial stack; fault tests do not require every
service to remain ready during the injected outage. Operational probes are private
deployment interfaces, not additions to the public domain API.

Use the existing single typed configuration loader and an `.env.example` with
local endpoints and variable names. Bootstrap disposable Organization/Corpus
credentials for tests before scenarios begin; scenario data then enters via the
public API. Generate API/signing/storage secrets per verification project and
keep them in ignored local files with restricted access. Do not copy personal
Linear, GitHub or model-provider credentials into the stack or its report.
Bind the configured webhook destination to the fixture receiver and Organization;
its control interface is reachable only by the test runner.

Migrations may run against the live evaluation stack. Breaking requests/workflows
and required restarts are accepted and reported. Keep numbered migrations and an
explicit initializer; do not add expand/contract, drain gates or a compatibility
matrix. Verification uses a fresh schema; adapter tests cover migration/bootstrap
idempotence. Safe production upgrades remain separate work.

## Runner and test boundaries

Use Go's test runner for `tests/acceptance`, with a small HTTP client and streaming
SSE helper. The acceptance package imports public transport types where useful,
but no internal application modules, SQL clients or Temporal/Weaviate clients.
Run the same suite against a supplied base URL and scoped credentials. Raw HTTP
assertions complement generated types so serialization helpers cannot hide an
invalid response. The existing Python/TypeScript representation checks remain
contract tests, not an obligation to implement two complete SDKs now.

| Layer | Assertions and permitted access |
| --- | --- |
| Public acceptance | Receipts, versions, availability, authorized search, changes, Matches and Deliveries through HTTP/SSE; exact webhook bytes through the receiver |
| Adapter integration | Real PostgreSQL transactions/guards/outbox, immutable S3 publication, projection convergence, Temporal retry behavior; infrastructure reads allowed |
| Unit | Pure validation, identity rules, segmentation boundaries and state decisions; no substitute for transaction evidence |
| Harness control | Start/stop services, configure receiver failures and collect diagnostics; never mutate canonical tables to manufacture a passing scenario |

Control and diagnostic access does not become the acceptance oracle. A runner
may stop a worker through Compose, but must observe eventual recovery through
public resources. A fault that requires an exact internal transaction pause
belongs to adapter integration unless a real public barrier exists.

Use versioned text fixtures with fixed source identities, expected permissions,
ordinary correction and withdrawal. Include the accepted 24-item FR/EN retrieval
fixture and model-specific judgments separately from the deterministic monitoring
predicate. Isolate tests by Organization/Corpus or fresh stack. Poll named public
conditions with a deadline and retain the last response on timeout; do not assert
random server IDs, precise wall-clock durations or private workflow histories.
Retrieve shared journal events using a captured cursor, deduplicate by event ID
and verify SSE/polling resume against the same committed mutations. `make verify`
starts a second API over the same database with `change_retention: 2s` so
pre-stream cursor expiry (HTTP 410) is proven publicly; in-stream `stream_error`
is covered by transport tests.

The harness configuration provisions one webhook destination per test
Organization (`local-receiver-org-a`, `local-receiver-org-b`) with obvious
local test signing secrets. No receiver listens on those URLs until delivery
lands; monitoring acceptance (`TestMonitoring*`) runs after the timed change-feed
and outage scenarios on its own Corpora.

## Webhook fixture and bounded recovery scenarios

The receiver stores raw body bytes, headers, arrival time and response outcome,
and exposes a private test control/read interface. Support acknowledge, return
503 for a fixed number of attempts, and hold/release one response with a timeout.
Verify the selected signing scheme before parsing, including timestamp freshness,
tampered payload rejection and the stable event ID across retries. Keep signing
keys out of exported captures. Assertions use the configured destination's key,
not an assumption that any correctly shaped payload is authentic.

| Scenario | Observable result |
| --- | --- |
| Ingest and replay a text fixture | Durable Receipt, one canonical version, eventual searchable state, authorized results |
| Positive monitoring fixture | One immutable Match, reference-only signed event, inspectable Delivery and attempts |
| Temporary receiver failure | Match remains unique; eventual successful Delivery with the same event ID and body across attempts |
| Disable before a retry | After the failed attempt completes, disable publicly; no new attempt admitted, prior Match/Delivery remain readable |
| Ordinary correction no longer matches | Linked update references the previous Match; no fabricated positive Match |
| Withdraw an alerted Record | Public search excludes it and a linked withdrawal notice is delivered |
| Restart worker during pending work | Accepted work converges through public state after restart without duplicate canonical effects |
| Temporarily stop search or Temporal | Accepted work remains inspectable and progresses after recovery; outage errors are visible |

For the disable case, configure a bounded retry delay long enough to observe the
first failed attempt and complete disable before its next eligibility time. If a
retry was already admitted, fail the test setup rather than claim suppression of
in-flight work. Observe past that eligibility time with margin and check public
attempt history plus receiver captures. No sleeps that assume disable wins a race.
Other timing values can be shortened for verification through configuration, and
the report records overrides. Do not disable signature checks or transaction guards.

Persistent receiver failure should exercise exhaustion with a shortened configured
delivery window. S3 retry and transaction races belong to targeted adapter tests
when Compose-level interruption cannot isolate the required point. Projection
rebuild/recovery uses available public Operations when implemented; direct store
corruption is an integration fixture, not a new public administration endpoint.
THE-550 reports missing seams as gaps rather than claiming unexecuted scenarios.
There is no successive-corrections policy matrix in this tracer.

## Diagnostics and CI

Emit structured JSON logs with severity, component, operation, outcome, duration
and the available request/Receipt/Record/Match/Delivery identifiers. Preserve
correlation through outbox dispatch and Activities. Do not log content bodies,
API keys, signatures or full dependency credentials. Capture bounded error details.

Start with counters for accepted commands, processing outcomes and delivery
attempt outcomes; gauges for pending work and oldest pending age; and duration
measurements for acceptance-to-searchable and webhook delivery. Use bounded labels
such as component/outcome, never Record IDs as metric labels. A metrics endpoint
is sufficient; a collector, dashboard and distributed tracing backend are not
required. Diagnostics assist explanation; public assertions determine correctness.

`verify` writes a run manifest and machine-readable test results plus a short
Markdown report under ignored artifacts. Include source revision, pins, fixture
profile, timing overrides, commands, durations, scenario outcomes and unresolved
gaps. On failure, attach redacted service logs, readiness status, last public
responses/cursors and receiver captures from synthetic fixtures. Preserve artifacts
even when startup fails. Separate diagnostic infrastructure details from API output.

CI uses the same commands and pins as local verification:

1. Formatting, static checks, unit tests, full OpenAPI validation and the
   [existing transport checks](../contracts/http/v0/README.md). Regeneration must
   match committed generated artifacts once those artifacts exist.
2. Adapter integration and the isolated Compose acceptance journey, including
   the real local embedding service and controlled webhook recovery scenarios.
3. Upload the report and failure diagnostics before cleanup, even after failure.

Start with one supported CI host architecture. Record its actual resource needs
in THE-550; other architectures and performance measurements are explicit runs,
not an implied compatibility claim. Cache artifacts by their pinned identities;
a cold cache must not silently skip model-dependent assertions. Cross-platform
vector checks from THE-553 remain separately reported evidence.

`make measure` is that explicit run for text retrieval (THE-661). It follows the
frozen [workload](../tests/measurement/workload-v1.json): public search in three
modes over the 24-query CC0 fixture, three load conditions, cold preparation and
start recorded separately from warm start and model readiness, resource peaks and
exact pins. Harness or dependency errors fail it; a missed p95 target or relevance
deficit is a reported finding. It stays outside `verify` and runs in CI through
the non-required `Retrieval baseline` workflow, on manual dispatch only.

## Plugin substitution and handoff

The core invokes its accepted processing interface. Initially normalization can
run locally. Later, select a remote adapter and add its process through a Compose
override; keep the public acceptance inputs, assertions and runner unchanged.
The Plugin Contract Runner separately checks plugin input/result schemas and
timeouts. Do not build a plugin registry or force local calls over HTTP for the
harness. Private Agency fixtures are not prerequisites for the public CC0 journey.

THE-550 must implement the commands, run the integrated journey and record actual
results against the table above, including failed attempts and missing behavior.
Until that evidence exists, this proposal establishes a reproducible target only.
