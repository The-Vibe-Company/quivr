# Quivr V2 — Local harness and operational baseline

Decision ticket: [THE-548](https://linear.app/thevibecompany/issue/THE-548).

Status: implemented. [THE-662](https://linear.app/thevibecompany/issue/THE-662)
assembled the end-to-end journey and hardened the harness; see "Implemented
verification (THE-662)" below. Stan accepted correlated logs, a few metrics and
an error report as the initial diagnostics; distributed tracing and dashboards
are deferred. The rest of this document is the original design (THE-548); where
it says "must", the implemented section records what was built.

## Scope and inherited decisions

Start the reference stack with one command and verify behavior through public
HTTP contracts and captured webhooks. Reuse the accepted
[module boundaries](dated/design/quivr-v2-module-boundaries.md),
[ingestion contract](dated/design/quivr-v2-ingestion-contracts.md) and
[monitoring tracer](dated/design/quivr-v2-monitoring-tracer.md).

The [THE-549 spike](https://github.com/The-Vibe-Company/quivr-v2/tree/4196f51/prototype/runtime-spike)
provides recovery scenarios and operational evidence. Its tests also inspect
PostgreSQL, Temporal and Weaviate, so they are not the public acceptance suite.
Its fake embeddings and external fake normalizer are not the reference baseline.
Use THE-553's [pinned local E5/TEI profile and CC0 fixture](https://github.com/The-Vibe-Company/quivr-v2/blob/9e59d3bf12afe5d20ce1b0afd5775e464b2ebddf/research/text-segmentation-embedding-profile.md).
Monitoring matches through a pinned alert-rule plugin (the `subscription`
template, see below); the deterministic fixture evaluator stays installed for
the notification-mechanics tests.

## Commands and isolation

| Target | Required behavior |
| --- | --- |
| `make dev` | Build or obtain pinned artifacts, start dependencies, run initializer, then API/worker; return only when startup checks succeed |
| `make verify` | Run static/contract checks and tests in an isolated Compose project; collect a report and return nonzero on failure |
| `make down` | Stop the development project's processes and containers and preserve its data volumes |
| `make reset` | Stop the project and delete only its volumes and the state bound to that data. Generated credentials and ports are kept, and nothing is restarted: the next `make dev` initializes a fresh schema |
| `make migrate` | Run the versioned initializer against the running project; report failures and required restarts. On a stopped project it fails and points to `make dev` |
| `make adapter-postgres` | Run `go test ./internal/adapters/postgres/...` against a bare, migrated PostgreSQL in its own Compose project; works on macOS arm64 and Linux (see below) |

`QUIVR_PROJECT=<name>` selects another project for `down`, `reset` and `migrate`,
for example a verification run kept with `QUIVR_KEEP_ON_FAILURE=1`.

`verify` uses its own project name, volumes, network, fixture identities and
temporary credentials. It must not reuse or reset the developer's stack. Publish
development ports on loopback; verification runs its runner inside the project
network and needs no fixed host ports. A run ID distinguishes artifacts and
concurrent projects. On success or failure, collect artifacts before cleaning up
only that run's containers and volumes. Interrupt handling follows the same rule.
An explicit keep-on-failure option may preserve that isolated run for inspection.

Every loopback port a host service uses (API, probes, worker probe, fake
servers, receivers, normalizer plugins, demo server) comes from one allocator,
`scripts/ports.py`. It never hands out a port twice in a harness process and
never re-hands a port a reloaded stack already owns. It picks from a band below
the kernel's ephemeral range and Docker's published ports (from 15000 up to
32768, or to where the kernel range starts if lower), so `bind(0)`, outgoing
connections and Docker cannot take a chosen port before its service binds it.
Harness processes running side by side on one host lease their ports through
files in a private per-user directory under the system temporary directory
(`quivr-harness-ports-<uid>/`), so they
skip each other's ports; a lease whose process has exited is reclaimed. Asking
the kernel for port 0 and releasing it was racy and occasionally gave two
services one port (THE-728).

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
| `quivr migrate` | One-shot ordered migrations (legacy `0xx_`, then UTC-stamped) and idempotent storage/projection/bootstrap; successful exit gates first startup |
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
and required restarts are accepted and reported. Keep ordered migrations (UTC-stamped
names; the numbered `0xx_` set is closed) and an explicit initializer; do not add expand/contract, drain gates or a compatibility
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
is covered by transport tests. The worker also physically prunes the change
journal of `org_r` (key `QUIVR_TEST_RETENTION`) after 2 s, every second
(`change_prune` with `allow_short_retention`). The short retention is confined
to `org_r`, so org_a/org_b cursors keep the default seven days.
`TestChangePruneExpiresCursorsAndResyncConverges` proves the sequence: prune,
then 410 on the seven-day API, then catalog resync converging. Verification
records these settings under `timing_overrides.change_prune`.

The harness configuration provisions one webhook destination per test
Organization (`local-receiver-org-a`, `local-receiver-org-b`) with obvious
local test signing secrets. Nothing listens on those URLs, so their Deliveries
retry and end exhausted. A third org_a destination, `local-receiver-capture`,
points at a harness-allocated port where the delivery acceptance tests run
their own verifying receiver (`QUIVR_TEST_RECEIVER_ADDR`,
`QUIVR_TEST_RECEIVER_SECRET`) with scripted per-Subscription replies. Monitoring
acceptance (`TestMonitoring*`) runs after the timed change-feed and outage
scenarios on its own Corpora; then `TestDeliveryRestartBefore` records a failed
attempt, the harness kills and restarts the worker, and
`TestDeliveryRestartAfter` proves the same Delivery converges to delivered.

The local harness (`make dev` and `make verify`) shortens the webhook retry
policy through the worker's `delivery` block (initial 2 s, cap 2 s, window 20 s
instead of 1 s / 5 min / 24 h); verification records it under
`timing_overrides` in `report.json`. Because the test receivers listen on
loopback, the same block sets `allow_private_destinations: true`, which is also
recorded there; deployments keep the default refusal of private destinations. The worker probe's
`/metrics` (`QUIVR_TEST_WORKER_PROBE_URL`) is scraped by the exhaustion test and
saved as `delivery-metrics.txt`.

### PostgreSQL adapter suite on a bare database

`make adapter-postgres` gives fast feedback on transactions and guards without
the full stack, and runs on macOS arm64 as well as Linux (only Docker, Go and
Python are needed). It starts only the pinned `postgres` service of
`deploy/compose/compose.yaml` in a project named `quivr-adapter-pg-<run id>`
with its own random password, writes a `config.json` holding only
`database_url`, and runs the suite with `QUIVR_ADAPTER_CONFIG` pointing at it.
Extra `go test` arguments go in `args`, for example
`make adapter-postgres args='-v -run TestDelivery'`.

The suite's `TestMain` prepares the database with `app.BootstrapDatabase`, the
PostgreSQL part of `quivr migrate`: the embedded migrations, then the default
projection generation. It needs no S3, Weaviate or tokenizer and adds no flag
to any production command. Both steps are idempotent, so inside `make verify`,
where `quivr migrate` has already run, the same `TestMain` changes nothing. Two
tests need more than PostgreSQL (the tokenizer, TEI and S3). They skip only
when `QUIVR_ADAPTER_POSTGRES_ONLY=1`, which this target sets, and still run in
`make verify`.

The test output is streamed and saved to `.scratch/<project>/adapter-postgres.log`,
with `postgres.log` and `services.json`. Secrets are redacted from them, and `config.json` is deleted with the database. On
success, failure, Ctrl+C or SIGTERM the run removes only its own project
(`docker compose -p <project> down --volumes`). With `QUIVR_KEEP_ON_FAILURE=1`
a failed run is kept, and the command to remove it is printed. CI runs it as the
separate `adapter-postgres` job. That job gives a signal within minutes, proves
the Linux path, and fails if an adapter test silently depends on state that
acceptance scenarios leave behind in `make verify`.

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

## Implemented verification (THE-662)

`make verify` runs on linux/amd64 only (ubuntu-24.04 in CI); no other platform is
claimed. It runs the same commands locally and in CI, in this order:
1. `denylist`, `migrations` and `contracts`, which regenerates the transport and
   compares it with the committed code. It also validates the full OpenAPI
   document, every example (the original 24 are a guarded floor) and at least
   31 boundary checks.
2. `test`.
3. `scripts/local.py verify`.
4. The demo UI check.

**Isolation.** Each run gets:
- a unique Compose project `quivr-verify-<run id>`, with its own network and volumes;
- ports on 127.0.0.1 only;
- a private directory under `.scratch/` (mode 0700);
- freshly generated keys, database password, S3 and cursor secrets (state file mode 0600).

The harness reads no personal credential from the environment. The only variables
it reads are `GO`, `CONTRACT_PYTHON`, `QUIVR_PROJECT` and `QUIVR_KEEP_ON_FAILURE`;
`scripts/test_local.py` enforces this.

**Steps and report.**
- Every scenario is a named step:
  - persistence, core acceptance, adapters and outages;
  - changes/catalog/rebuild, monitoring, the `quivr` CLI (`TestCLI*`, run with the
    stack's built binary as `QUIVR_TEST_BINARY`) and delivery restart;
  - the three journey phases, connectors, keyless, capture validation and lifecycle.
- On success, failure or interrupt (SIGINT or SIGTERM), the run captures service
  logs and `services.json`, writes `dependency-inventory.json`, then removes only
  its own project.
- Before the report is written, every generated secret is replaced by `[REDACTED]`
  in exportable artifacts. Private configuration files are never uploaded.
- `report.json` and `report.md` record:
  - status, the failed or interrupted step and its bounded error;
  - source revision and dirty flag, per-step durations;
  - `timing_overrides`;
  - pins: image digests, model revision, tokenizer, toolchain;
  - the unsupported platforms;
  - a link to [the remaining-limit report](quivr-v2-remaining-limits.md).
- CI uploads the artifacts and appends `report.md` to the job summary.
- With `QUIVR_KEEP_ON_FAILURE=1`, a failed run keeps its project for inspection.
  Remove it with `QUIVR_PROJECT=<name> make reset`.

**Readiness.** Every wait is bounded:
- Compose `--wait` up to 180 s, with one more bounded attempt when a dependency
  crashes while starting (recorded as `dependency_start_retries`);
- each process `/readyz` up to 20 s.

A timeout names the probe (`api`, `worker`, `short-api`), its last answer and
the startup log to read, and records them in `readiness.json`.

Readiness separates durable acceptance from downstream outages. During the
Weaviate outage scenario the harness asserts that:
- the API `/readyz` stays 204, so commands are still accepted durably;
- the worker `/readyz` turns 503, but the worker is live and is not restarted.

It records both under `during_search_outage`. The report records cold model
preparation apart from the whole stack start (`preparation`); it makes no
startup-time claim.

The lifecycle step recreates containers. Before it runs, the service logs of
the whole run are kept as `<service>-before-lifecycle.log`.

**Assembled journey.** `tests/acceptance/journey_test.go` composes the feature
journeys in its own Corpus, over HTTP, SSE and raw signed webhook bytes. The
harness stops the worker between the first and second phases and restarts it
before the third:

1. **Before restart.**
   - Inline ingestion and replay.
   - Lexical search, then vector search: `record.retrieval_ready` precedes
     `record.enrichment_available`.
   - A Match whose signed webhook fails once and is then delivered with
     identical bytes.
   - A batch with a verified upload and a structured Manifest.
   - A correction that no longer matches, and a withdrawal notice.
2. **Worker stopped.** New work is accepted and stays pending; reads and search
   keep working.
3. **After restart.**
   - The pending work converges, with exactly one Match and its Delivery.
   - SSE replay and `Last-Event-ID` resume agree with polling.
   - The saved cursor expires with 410 on the short-retention API, and catalog
     resync converges.
   - A rebuild activates a new generation without changing results or
     resurrecting the withdrawn Record.

Each phase writes `journey-<phase>.json` with per-step timings. Each feature keeps
its own detailed tests. See [tests/acceptance/README.md](../tests/acceptance/README.md)
for ordering rules, including waiting for enrichment before any search assertion.

**Lifecycle.** The last step, `scripts/lifecycle.py`, proves on the verification
project that:
- `migrate` is idempotent while the project runs, and refused with guidance once
  it is stopped;
- `down` then `dev` keeps a Corpus;
- `reset` then `dev` starts with no Corpora.

It writes `lifecycle.json`.

**Metrics and the failure drill.** Each process serves Prometheus text on its
private probe listener at `GET /metrics`. There is no client library, and labels
come only from fixed sets: no identifiers or secrets.

| Process | Metric | Kind |
| --- | --- | --- |
| API | `quivr_commands_accepted_total{command=record\|batch_entry\|withdrawal\|upload_confirm}` (replays included) | counter |
| API | `quivr_ingestion_pending`, `quivr_ingestion_oldest_pending_age_seconds` (Receipts accepted but not yet materialized) | gauges |
| Worker | `quivr_processing_outcomes_total{stage=baseline\|enrichment,outcome=succeeded\|retrying\|blocked}` | counter |
| Worker | `quivr_acceptance_to_searchable_seconds` (from the Receipt's durable `accepted_at`) | histogram |
| Worker | `quivr_delivery_attempts_total{outcome}` (THE-656), `quivr_delivery_request_duration_seconds` | counter, histogram |
| Worker | `quivr_delivery_pending`, `quivr_delivery_oldest_pending_age_seconds` (THE-656) | gauges |

Logs are structured JSON with bounded fields:
- `command accepted`: `request_id`, `receipt_id`, `record_id`.
- `processing outcome`: `stage`, `outcome`, `code`, `receipt_id`, `record_id`,
  `version_id`, `duration_ms`.
- `delivery attempt`: now carries `duration_ms`.

**Failure drill** (`scripts/failure_drill.py`). This drill checks diagnostics only.
It runs around the journey's real worker outage and asserts:
- during the outage:
  - the API `/readyz` answers 204, so durable acceptance is unaffected;
  - the worker probe does not answer;
  - the ingestion backlog gauges show the Receipt pending for at least 1 s;
  - the API log correlates its `request_id` with the Receipt.
- after the restart:
  - the API backlog gauge drops, so the Receipt drained;
  - the worker's processing counter records the outcome;
  - its acceptance-to-searchable histogram records an observation over 1 s;
  - its log names the same Receipt with its Record and Version.

It writes `failure-drill.json` and the metric snapshots it read. The final
`metrics-api.txt` and `metrics-worker.txt` are captured before cleanup.

**Dependencies.** [third_party/README.md](../third_party/README.md) indexes the
notices. `scripts/inventory.py` lists the Go modules linked into the built
binary, the pinned images, the model and tokenizer, and the demo UI's npm
packages. A licence it cannot identify is written as `unclassified`.

## Plugin substitution and handoff

The core invokes its accepted processing interface. Initially normalization can
run locally. Later, select a remote adapter and add its process through a Compose
override; keep the public acceptance inputs, assertions and runner unchanged.
The Plugin Contract Runner separately checks plugin input/result schemas and
timeouts. Do not build a plugin registry or force local calls over HTTP for the
harness. Private customer fixtures are not prerequisites for the public CC0 journey.

The harness pins one external normalizer and two alert-rule plugins together.
`scripts/normalizer_plugin.py` pins the normalizer in the stack configuration's
single `plugin` entry and,
for pdf-text and the template, runs it as its own process with the repository
SDK. `QUIVR_NORMALIZER` chooses it for `make dev`:

| `QUIVR_NORMALIZER` | Plugin | Routed media type |
| --- | --- | --- |
| `pdf-text` (default) | The reference plugin [`plugins/pdf-text`](../plugins/pdf-text/README.md), installed into `.scratch/plugin-sdk/venv` | `application/pdf` |
| `template` | The `quivr plugin init` template, scaffolded once per stack | `text/markdown` |
| `none` | No external normalizer; only `text/*` Blobs are accepted | none |
| a plugin directory | Your own plugin, pinned at `http://127.0.0.1:$QUIVR_NORMALIZER_PORT` (default 9900) with the JSON configuration in `QUIVR_NORMALIZER_CONFIG` (default `{}`). The harness does not run it; start it with `quivr plugin dev --port 9900 <dir>` ([guide](plugins/write-a-normalizer.md)) | every media type its normalizer declares |

For example, `QUIVR_NORMALIZER=none make dev` disables it. The choice is applied
on every `make dev` and printed with the API address. The plugin's log is
`.scratch/<project>/normalizer-plugin.log`.

Verification starts on the template and runs these steps in order:

1. It ingests a Markdown Blob through the public API.
2. It checks that `quivr api` and `quivr worker` refuse invalid pins: an
   incompatible Plugin API or engine range, and a configuration that fails the
   plugin's schema.
3. It stops the plugin, restarts the API and worker, and checks that both stay
   healthy.
4. It rebuilds the Corpus while the plugin is still down.
5. With the plugin still down, it ingests another routed Blob and checks for 40
   seconds that the Receipt stays `pending` with `plugin_unavailable`, while both
   `/healthz` probes, text ingestion and search keep working. It then restarts the
   plugin and checks that the pending Version becomes searchable.
6. It pins the controllable Go test plugin (`internal/plugins/devhost/fakeplugin`,
   mode `by-record-key`) on a required and an optional `text/*` route, and restarts
   the API and worker. It then checks each failure class (terminal error, malformed
   Part, bad checksum, undeclared namespace, oversized response, timeout, exhausted
   retries): the Version is quarantined with the right diagnostic on the Receipt and
   the Version read, and a `record.quarantined` change event is published. It also
   checks that the optional route stays searchable through the built-in text path.
   Finally it restores the stack's pin.
7. It switches the pin to pdf-text, restarting the plugin, the API, the worker
   and the short-retention API.
8. It uploads a three-page PDF and finds a phrase from page 2 on Part `page-2`,
   with `normalization` provenance naming `pdf-text` 0.1.0.
9. It ingests damaged PDF bytes. The Version is quarantined with a structured
   `normalizer_failed` diagnostic naming pdf-text and a `record.quarantined`
   change event, and a Record ingested afterwards is still searchable.

10. Next to pdf-text, the alert-rule template (`scripts/subscription_plugin.py`)
    decides alerts: a matching article gives exactly one signed webhook with the
    plugin's evidence, a non-matching one gives none, an invalid expression or
    configuration is 422 at Subscription creation, and a rule on the
    provenance producer matches only that producer's article.
11. The keyword alerts plugin `plugins/alerts` decides a saved
    `<word> AND (grève OR strike) NOT sport` query:
    - only the article that satisfies it alerts, although it writes `GREVE`;
    - it gives exactly one signed webhook;
    - the Match evidence names the matched terms and their Parts;
    - a malformed query tree is 422;
    - a `source` filter alone matches only that source's article.
12. With the alert-rule plugins stopped, a matching article becomes searchable
    and no Match appears; after the restart the delayed evaluation completes
    with one Match and one acknowledged webhook.

Later steps keep the pdf-text pin.

Every stack also pins two alert-rule plugins through the configuration's
`plugins` list (`scripts/subscription_plugin.py`):

| Plugin | Subscriptions pin | Log |
| --- | --- | --- |
| The keyword alerts plugin [`plugins/alerts`](../plugins/alerts/README.md), installed into `.scratch/plugin-sdk/venv` ([guide](keyword-alerts.md)) | `{"plugin_id": "alerts", "version": "0.1.0"}` | `.scratch/<project>/alerts-plugin.log` |
| The `quivr plugin init --kind subscription` template, scaffolded once as `alert-rules` in `.scratch/<project>/subscription-plugin` | `{"plugin_id": "alert-rules", "version": "0.1.0"}` | `.scratch/<project>/subscription-plugin.log` |

`QUIVR_ALERTS=off make dev` leaves the keyword alerts plugin unpinned, and
Subscriptions pinned to it are then refused with `422 unsupported_evaluator`.
`make verify` always pins it. `make dev` prints the pinned alert-rule plugins
under the API address.

The PostgreSQL adapter suite also kills the test plugin process in the middle of an
invocation, restarts it, and checks that the Version ends with exactly one published
Manifest.

THE-550 must implement the commands, run the integrated journey and record actual
results against the table above, including failed attempts and missing behavior.
Until that evidence exists, this proposal establishes a reproducible target only.
