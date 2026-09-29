# Testing standard

A test earns its place by catching a credible regression that no cheaper test
catches, fast enough that people wait for it. This page is the rule for every
test added or changed in this repository, by a person or an agent. Reviews check
it.

## Before adding a test

Answer four questions. A missing answer means the test is not ready.

1. What observable behaviour or contract does it protect?
2. What realistic change would make it fail?
3. Why does no existing test already catch that? Each contract has one owner
   test, at the strongest boundary that is still cheap. Extend a table or a
   fixture before writing a near-copy.
4. Does it need a hook that production code does not need (an export, a flag, an
   injection point)? Then test at the real boundary instead.

Reject a test that compares a value with itself, restates the source, copies a
fixture or list it then checks, or asserts behaviour that a fake produces. Fake
the network or the dependency, never the behaviour under test. A bug fix comes
with a test that fails on the old code for the reason of the bug.

## Where a test goes

| Level | Tool | Use it for | Budget |
| --- | --- | --- | --- |
| Unit | `go test`, `scripts/test_*.py`, `node --test` | Parsing, validation, mapping, pure rules | Seconds per package; no network, Docker or sleep |
| Adapter | `make adapter-postgres` | SQL and storage behaviour on real PostgreSQL | The whole suite in about a minute |
| Acceptance | `tests/acceptance` on the local stack | User journeys, cross-component contracts, outage and recovery | 10 s per test |
| Browser | Playwright in `quivr-search/tests` | The demo's critical paths | 15 s per spec |
| Measurement | `make measure`, nightly | Relevance, latency and cost numbers | Outside the pull-request path |

Choose the lowest level that can see the behaviour. Acceptance tests talk to the
public API only; their rules are in `tests/acceptance/README.md`. A measurement
belongs on a pull request only when it is a regression gate with a fixed
threshold.

## Time

- Never wait for the wall clock. Retry delays, delivery windows, retention and
  polling intervals come from the harness timing overrides, set as short as the
  behaviour allows.
- Wait on an observable condition with a deadline (`awaitReceipt`,
  `awaitDelivery`, `expect(...).toHaveText`). A bare sleep is a bug.
- A test over its budget carries a comment that names why, and a ticket to
  shorten it or move it to its lane. The CI summary lists the slowest tests;
  keep that list short.

## Flaky tests

A test that fails without a code change is a bug, and it is fixed at its cause.

- On the first sighting, open a ticket with the run link, the test name and the
  error. Rerunning only the failed job is allowed to unblock an unrelated pull
  request, and the ticket records it.
- Do not add retries to reach green. Find the race: most flakes so far read a
  value once instead of waiting for it (THE-738) or compared two moments of an
  asynchronous result (THE-754). Also read the dependency logs: a runner disk
  at 90% turned Weaviate read-only and stalled ingestion (THE-758).
- The fix shows the race: force the slow path, show the old test fails and the
  new one passes.

## Failure output

A failing test says what it expected, what it got, and the identifiers needed to
look further, so the cause is readable from the run page. Log bodies and
artifacts are for depth, not for finding the failure.

## In CI

- A pull request runs one quick lane (checks, unit, adapter) and the full
  local-stack verification. Both must pass.
- The full verification runs in parallel parts, each with its own stack.
- Measurements run nightly, and on pull requests that change what they measure.

The method for pruning existing tests area by area is the test-audit campaign
used in THE-737: a per-test ledger, one owner per contract, and one deliberate
mutation per kept contract.
