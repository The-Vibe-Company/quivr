# Quivr V2 — Remaining limits

Status: consolidated for the text-monitoring vertical slice (Spec 1) by
[THE-662](https://linear.app/thevibecompany/issue/THE-662), 2026-09-28.
Quivr V2 is at evaluation stage. Nothing here, and no green `make verify`,
certifies production readiness, capacity, security or relevance.

Each limit was declared by the slice that shipped the behaviour. A limit with a
correctness or security impact has its own ticket; the rest are listed so they
stay visible. The Spec 1 obligation map is in
[docs/evidence/the-662-spec1-closure.md](evidence/the-662-spec1-closure.md).

## Open, tracked

| Limit | Impact | Ticket |
| --- | --- | --- |
| PostgreSQL adapter tests need the whole Linux stack | Slow feedback off Linux | [THE-699](https://linear.app/thevibecompany/issue/THE-699) |
| Hybrid search ranks below semantic search on the FR/EN fixture | Relevance, kept separate from this list | [THE-641](https://linear.app/thevibecompany/issue/THE-641) |

## Accepted or untracked

**Platform and harness**

- Only linux/amd64 is supported: tokenizer wheel, TEI digest and `/proc`
  process checks. macOS and linux/arm64 are not claimed.
- The acceptance suite is order- and load-sensitive, so tests use their own
  Corpus and are scheduled explicitly (`tests/acceptance/README.md`).
- Delivery retries run on a shortened policy under verification (initial 2 s,
  cap 5 s, window 60 s), reported under `timing_overrides`.
- SeaweedFS 4.45 can crash with a raft map race when it restarts on existing
  data. The harness gives a failed dependency start one more bounded attempt and
  records it in `readiness.json` (`dependency_start_retries`).
- Change-cursor expiry is proven with a second API at 2 s retention.
  In-stream expiry is proven only by handler tests.
- Physical pruning of the change journal (THE-697) is proven on org_r only:
  the harness worker prunes it after 2 s through an explicit
  `allow_short_retention` override, reported under `timing_overrides`.

**Change feed**

- The prune waits for evaluation dispatch. A stalled dispatch checkpoint holds
  back pruning of its Organization and the journal grows until dispatch resumes.
- Two emission probes (`record.enrichment_available`, `operation.updated`)
  detect an earlier emission by looking up its event. If the same mutation
  commits again more than `change_retention` after the first one, that event is
  gone and the same `event_id` is published again at a new position. Clients
  already deduplicate by `event_id`. A repeated evaluation cannot create a
  second Match.
- Expiry uses both the pruned watermark and the age rule, so a cursor can
  get 410 while its next events are still stored (the prune lags or dispatch
  holds it back). It never resumes over a gap.
- Migrations may break in-flight work. Restarts are reported, not avoided, and
  there is no expand/contract or compatibility matrix.

**Ingestion**

- Batches share the global 5 s request deadline, upload time included.
- A Blob-only Manifest is stored but not extracted, so it is `blocked` for search.
- No orphan-upload sweeper or retention policy exists.

**Search and rebuild**

- Each enriched segment is projected twice, as a permanent lexical object and
  an enriched object, so BM25 postings are duplicated and the lexical index is
  about twice as large (THE-690). Search deduplicates by segment, but BM25
  document counts include both objects: an enriched segment's terms count twice,
  which slightly lowers their IDF against not-yet-enriched segments until
  enrichment catches up. Revisit for scale in Spec 8.
- Objects of superseded Versions, withdrawn Records and abandoned generations
  are purged only after `projection_purge_grace` (default 1 h, THE-698).
  Until then hydration hides them, but they still take some of the 150
  candidates a search fetches, so a Corpus with heavy churn inside that window
  can return a shorter page than exists.
- A projection write that lands after the grace period and the purge (a
  promotion still aimed at a replaced generation, or ingestion of an
  already-superseded Version delayed by more than the grace period) leaves an
  orphan object that no later sweep selects. Hydration hides it, and it holds
  at most the lexical object: an enriched object is never created without its
  anchor. No reconciliation sweep compares the store with coverage.
- The purge keeps projection and embedding coverage rows as history. It relies
  on a dead Version never becoming current again. A correction back to an
  earlier Version's exact bytes mints a new Version with that content
  (ADR 0003, THE-712) instead of re-pointing the Record to the purged one. An
  adapter test fails if that ever changes.
- Physical collections left by evaluation cutover migrations (earlier default
  generations) are not purged.
- Each worker's purge sweep scans every Record Version once a minute to find
  newly dead ones, and `projection_purges` rows are kept as history. Both grow
  with the total number of Versions; revisit for scale in Spec 8.
- Retrieval measurement ran on one 2-CPU runner with 24 documents.
  Lexical and hybrid ranks vary slightly between runs with identical pins.
- Mapping values that cannot be projected are skipped silently. Pending
  retrieval configuration is not readable, and semantic vectors ignore mappings.
- Some branches are proven only at adapter or unit level:
  - mutation during a rebuild;
  - cancelling a running rebuild;
  - rerun and rebuild conflicts (409);
  - catalog removal on 404.

**Monitoring**

- Evaluation and delivery run as PostgreSQL-leased loops rather than Temporal
  workflows. This is a documented deviation from the blueprint.
- Matching uses the deterministic fixture evaluator. Production match criteria
  belong to a future plugin.
- Delivery is at least once: a crash after sending can resend the same
  event id and bytes.
- Deliveries exhausted by the retry migration emitted no `delivery.updated`.
- Re-enabling a Subscription resumes delivery of its parked notices under their
  original window, so an ordinary notice parked for longer than the window ends
  `exhausted` (`window_elapsed`) without being sent. Only a `match.withdrawn`
  notice committed while the Subscription was disabled gets a window that starts
  at the re-enable (THE-696). Changes made during the pause are never
  evaluated; there is no backfill command.
- Delivery refuses private and internal receiver addresses after DNS
  resolution ([THE-695](https://linear.app/thevibecompany/issue/THE-695)), but
  a public hostname the operator configures is trusted: there is no egress
  proxy or per-destination allowlist. The harness lifts the refusal
  (`delivery.allow_private_destinations`, reported under `timing_overrides`)
  because its receivers listen on loopback.

**Observability**

- Metrics are a small Prometheus-text set on the probe listeners. There are no
  dashboards, distributed tracing or alerting.
- Counters and histograms reset when a process restarts.
- The ingestion backlog counts Receipts that are not yet materialized, not later
  processing stages. Receipts accepted before the `accepted_at` migration are
  dated at migration time.

**Supply chain**

- Container image licences are not inventoried.
- The E5 model's MIT declaration and training-data provenance are as published
  upstream; see [third_party/README.md](../third_party/README.md).
- The denylist does not scan the `multimodal-rag` submodule.
