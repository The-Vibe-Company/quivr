# Public acceptance suite

These tests talk to a running Quivr only through its public HTTP API, SSE change
stream and raw webhook bytes. They never read PostgreSQL, Temporal or Weaviate.
Without `QUIVR_TEST_URL` they skip; `make verify` (Linux x86_64) starts an
isolated stack, sets the scoped keys and runs them in the order declared in
`scripts/local.py`.

## Rules for new tests

- **Use your own Corpus**, created with a run-unique idempotency key
  (`monitoringRun()`). Use your own Organization when your test schedules
  background load, as the connector tests do with `org_c`. Test files run in
  alphabetical order inside one `go test` call, and `TestAuthorization` expects
  exactly one Corpus in `org_a` when it starts.
- **Wait for enrichment before any search assertion.** Attaching embeddings
  rewrites the projected object, and the search store briefly hides it from
  lexical results while it does (THE-690). A Version being searchable is not
  enough. Wait for `record.enrichment_available` (`awaitEnriched`, `ingestEnriched`),
  then search.
- **Poll named public conditions with a deadline** (`awaitReceipt`, `awaitReady`,
  `awaitDelivery`, `awaitOperation`). Never assert server IDs, wall-clock durations
  or private workflow state.
- **Schedule ingestion-heavy tests after the timed scenarios** in `scripts/local.py`
  and give them a distinct `-run` pattern. Use an anchored pattern
  (`^TestName$`) when a name is a prefix of another test.
- **Split a test around a harness action** (worker kill, dependency stop) into
  phases that pass identities through a file in `QUIVR_TEST_CAPTURES`. See
  `TestDeliveryRestartBefore`/`After` and the journey phases.

## The assembled journey

`journey_test.go` composes the feature journeys into one run in its own Corpus:

- inline ingestion and replay;
- a batch with an uploaded Blob and a structured Manifest;
- lexical search, then vector search;
- a Match with a signed webhook that is retried, then delivered;
- a correction that no longer matches, and a withdrawal;
- after a real worker outage: recovery, SSE replay and resume, cursor expiry,
  catalog resync and a rebuild.

Each phase writes `journey-<phase>.json` with per-step timings, so a failed run
names the step that failed. Each feature keeps its own, more detailed tests.
