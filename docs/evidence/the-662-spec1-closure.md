# Spec 1 — obligation map (THE-662)

Status: comparison written 2026-09-28 for
[THE-532](https://linear.app/thevibecompany/issue/THE-532) (text-monitoring
vertical slice) by its closing slice,
[THE-662](https://linear.app/thevibecompany/issue/THE-662).

It maps each obligation of the parent spec to the feature evidence that satisfies
it, and reports what is unresolved. **It does not close the spec.** Closing is a
human decision in Linear. The evidence named here is the public acceptance tests,
harness scenarios and adapter tests on `main`, run by every `make verify`.
Iteration records live on each ticket.

Verdicts: **met**; **met, caveat** (met, with an open limit that has a ticket);
**partial**; **unresolved**.

## User stories

| # | Obligation | Evidence | Verdict |
| --- | --- | --- | --- |
| 1 | Create, list and read authorized Corpora | THE-642: `TestCorpusPersistsAndReplays`, `TestAuthorization`, `TestPagination`, `TestConcurrentCreation` | met |
| 2 | Organization-bound, scoped API keys | THE-642 and every slice's 403/404 cases | met (key-management API out of scope) |
| 3 | Inline UTF-8 ingestion | THE-643: `TestInlineMaterialization`, `TestInlineRejections` | met |
| 4 | Namespaced structured source data | THE-648: `TestManifestExtensionsAndStructureRejections` | met |
| 5 | Explicit Manifest, Parts, Relations | THE-648: `TestStructuredManifestRelationsAndSearch` | met (Blob Parts stored, not extracted) |
| 6 | Verified upload before a Blob is used | THE-647: `TestUploadedBlobIngestion`; adapter upload tests | met |
| 7 | Source identity is Organization + Corpus + Namespace + key | THE-643, THE-650 | met |
| 8 | Durable acceptance without indexing or Temporal | THE-643: Temporal/S3 outages with SIGKILL and replay (`outages.json`) | met |
| 9 | Replay returns the same Receipt; a changed replay is a conflict | THE-643, THE-649, THE-650 | met |
| 10 | Independent per-entry batch outcomes | THE-649: `TestBatch*`; adapter `TestBatchEntryFailureLeavesNoTraceAndKeepsPeers` | met (5 s batch deadline) |
| 11 | Receipt state separate from processing and availability | THE-643, THE-644 | met |
| 12 | Immutable Version and Manifest reads | THE-643, THE-648 | met |
| 13 | Relation resolution respects access and withdrawal | THE-648: public test and adapter guard | met |
| 14 | Lexical search while embeddings are unavailable | THE-646: TEI outage (`embedding-outage.json`) | met, caveat: THE-690 |
| 15 | Lexical, semantic and hybrid modes with a versioned profile | THE-644, THE-646 | met |
| 16 | The whole requested scope is authorized | THE-644: scope rejections | met |
| 17 | Only eligible current Versions are returned | THE-644, THE-650 | met, caveat: THE-690 |
| 18 | Canonical excerpts with code-point bounds and provenance | THE-644, THE-645 | met |
| 19 | Outages are explicit errors, never empty successes | THE-644 (Weaviate 503), THE-646 (TEI 503) | met |
| 20 | A mapping change takes effect only after a validated generation | THE-660: `TestRetrievalConfigurationChangesSearchOnlyAfterCutover` | met |
| 21 | Immutable Saved Query and Subscription Versions | THE-653 | met |
| 22 | Activation from a committed boundary, no backfill | THE-653, THE-654 | met |
| 23 | Repeated evaluation yields one Match | THE-654: `TestMonitoringMatchesEvaluateLaterEligibleVersions` | met |
| 24 | Signed notices with a stable id and bytes across retries | THE-655, THE-656 | met |
| 25 | Delivery and attempts observable independently | THE-655: `TestMonitoringDeliverySignedNotifications` | met |
| 26 | Disable stops evaluations and retries | THE-653, THE-656 | met |
| 27 | A correction keeps the prior Version current until its successor is ready | THE-650: `TestCorrectionKeepsPriorCurrentUntilSuccessorReady` | met |
| 28 | A negative correction sends a linked update and no false Match | THE-657: `TestMonitoringCorrectionNotices`; THE-694 (#57): a stale `match.no_longer_matches` is superseded by a later `match.corrected` | met |
| 29 | Withdrawal fences pending materialization | THE-650: withdrawal race and no-resurrection tests | met |
| 30 | Linked withdrawal notice | THE-657: `TestMonitoringWithdrawalNotices` | met, caveat: THE-696 |
| 31 | Polling and SSE share identity and cursor | THE-651 | met |
| 32 | Cursor expiry and convergent resync | THE-651, THE-652 | met, caveat: THE-697 |
| 33 | Durable, scoped rebuild without re-embedding | THE-658: `rebuild-recovery.json` (TEI stopped throughout) | met |
| 34 | Replay and worker recovery keep the Operation and its target | THE-658 | met |
| 35 | Cancel does not undo; rerun gets a new, linked identity | THE-659 | met, caveat: THE-698 |
| 36 | One command each to start, verify, reset and migrate | Harness, THE-673, THE-662: `lifecycle.json`, `scripts/test_local.py` | met by THE-662 |
| 37 | Isolated acceptance, diagnostics and scoped cleanup | THE-662: per-run project, secrets and ports; step report; capture before cleanup on failure or interrupt | met by THE-662 |
| 38 | Correlated logs, bounded errors and a few metrics | THE-656 delivery metrics; THE-662 command, processing, backlog and duration metrics with the failure drill | met by THE-662, second pull request |
| 39 | Language-neutral contracts and generated checks | Every `make verify`: original 24 examples and 31 boundaries guarded, Go/Python/TypeScript round trips, captured responses validated | met |
| 40 | Honest limitation reporting | [remaining limits](../quivr-v2-remaining-limits.md), [dependency notices](../../third_party/README.md), `report.md` | met by THE-662 |

## Testing decisions

| # | Obligation | Evidence | Verdict |
| --- | --- | --- | --- |
| T1 | Public HTTP/SSE/raw-webhook seam; no SQL or workflow oracles | All acceptance tests (`tests/acceptance/README.md`) | met |
| T2 | 24 fixtures, 31 boundaries, full OpenAPI validation | `contracts/http/v0/checks/validate.py` with the original set as a floor | met |
| T3 | The main journey in one run | THE-662: `TestJourneyBeforeRestart`, `TestJourneyWorkerStopped`, `TestJourneyAfterRestart` | met by THE-662 |
| T4 | Isolation, mixed scope, history, relations, Unicode, long text | THE-642, THE-644 to THE-648 | met |
| T5 | Temporal, search and model outages; worker interruption; adapter tests | THE-643, THE-644, THE-646, THE-656, THE-658, THE-688 | met |
| T6 | Corrections, withdrawal race, retry and exhaustion, tamper, disable before retry | THE-650, THE-655 to THE-657, THE-694 | met |
| T7 | Polling and SSE resume; start-now; expiry and scope change; resync under mutation | THE-651, THE-652 | met |
| T8 | Rebuild, replay, recovery, vector reuse, cancel and rerun, other Corpora preserved | THE-658, THE-659 | met (some races adapter-only) |
| T9 | Per-mode relevance on FR/EN judgments; deficit reported | THE-646, THE-661 | met; conclusions below, kept separate |
| T10 | p95 < 1 s with a reproducible protocol | THE-661, THE-675 (all 9 cells met on one 2-CPU runner) | met |
| T11 | CI runs pinned commands from a fresh schema on one declared architecture | `verify.yml` (ubuntu-24.04, linux/amd64), `report.json` pins, `dependency-inventory.json` | met by THE-662 |

A deliberate deviation is documented in the monitoring tracer: evaluation and
delivery run as PostgreSQL-leased loops, not as Temporal workflows.

## Unresolved at hand-back

- [THE-690](https://linear.app/thevibecompany/issue/THE-690): a Record briefly
  disappears from lexical search while its embeddings attach, and enrichment of an
  ineligible Version retries forever. In Progress; no option chosen yet.
- Follow-ups filed by THE-662:
  - [THE-695](https://linear.app/thevibecompany/issue/THE-695): SSRF filter, High priority;
  - [THE-696](https://linear.app/thevibecompany/issue/THE-696): withdrawal notice when disabled;
  - [THE-697](https://linear.app/thevibecompany/issue/THE-697): change-event pruning;
  - [THE-698](https://linear.app/thevibecompany/issue/THE-698): rebuild ordering and purge;
  - [THE-699](https://linear.app/thevibecompany/issue/THE-699): `make adapter-postgres`.
- Untracked, accepted limits: [remaining limits](../quivr-v2-remaining-limits.md).

## Relevance (THE-641), kept separate

[THE-641](https://linear.app/thevibecompany/issue/THE-641) is in Triage, and no
investigation has started. The hybrid deficit reproduces consistently without
tuning on the 24-query CC0 FR/EN fixture.

| Source | Lexical (MRR@10 / Recall@3) | Semantic | Hybrid |
| --- | --- | --- | --- |
| THE-646 (isolated fixture collection) | 0.6993 / 0.8333 | 0.9583 / 1.0 | 0.7969 / 0.9583 |
| THE-661 run 1 (public path) | 0.7201 / 0.8333 | 0.9583 / 1.0 | 0.7760 / 0.9583 |
| THE-661 run 2 (public path) | 0.6993 / 0.8333 | 0.9583 / 1.0 | 0.7969 / 0.9583 |

The profile is unchanged (alpha 0.5, relative-score fusion, title^2 + body), and
no default change is approved. Nothing in this map is a relevance claim.
