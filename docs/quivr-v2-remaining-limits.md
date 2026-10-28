# Quivr — Engine limits for contributors

The technical limits of the engine and its verification harness, for people
changing this repository. Users and operators read the shorter
[Known limits](https://docs.quivr.thevibecompany.co/reference/known-limits) page
instead. A green `make verify` does not certify capacity, security or relevance.

This register was first consolidated for Spec 1 on 2026-09-28
([THE-662](https://linear.app/thevibecompany/issue/THE-662); obligation map in
[docs/dated/evidence/the-662-spec1-closure.md](dated/evidence/the-662-spec1-closure.md))
and checked against the code again for the 2.0 release
([THE-1940](https://linear.app/thevibecompany/issue/THE-1940)). A limit with a
correctness or security impact has its own ticket; the rest are listed so they
stay visible. Remove a limit in the pull request that lifts it.

**Platform and harness**

- `make dev` runs on linux/amd64 and macOS arm64; `make verify`, `make measure`
  and `make eval` run on linux/amd64 only. On macOS the pinned TEI image, which
  is published for amd64 only, runs under Docker Desktop's x86_64 emulation.
  linux/arm64 and Intel Macs are not claimed.
- The acceptance suite is order- and load-sensitive, so tests use their own
  Corpus and are scheduled explicitly (`tests/acceptance/README.md`).
- Delivery retries run on a shortened policy under verification (initial 2 s,
  cap 2 s, window 20 s), reported under `timing_overrides`.
- SeaweedFS 4.45 can crash with a raft map race when it restarts on existing
  data. The harness gives a failed dependency start one more bounded attempt and
  records it in `readiness.json` (`dependency_start_retries`).
- Change-cursor expiry is proven with a second API at 1 s retention.
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
- Legacy-risk migrations may break in-flight work. New migrations use
  expand/contract; there is no full released-version compatibility matrix yet.

**Ingestion**

- Batches share one request deadline (8 s), upload time included.
- Only text Parts are indexed: a Blob-only Manifest is stored but ends
  `ingestion_refused` for search.
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
- `projection_purges` rows are kept as history and grow with the total number
  of purged Versions.
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
- Match criteria come from `subscription` plugins. The first-party
  `plugins/alerts` has no stemming or wildcards in keyword alerts.
- Jev meaning checks send article text to TypeSafe. A TypeSafe outage delays the
  keyword alerts of the same articles too, because the core retries a plugin's
  batch as one.
- One article's due alerts are decided in one plugin call: one worker at a time
  claims a Record Version's intents. After a failed call, each intent retries on
  its own jittered schedule, so the retries of one article can cost more than one
  call until they succeed.
- Subscription Versions pinned to an alert-rule version no longer installed stay
  pending with `evaluator_unavailable` until an operator restores it or retires
  them (ADR 0006).
- Delivery is at least once: a crash after sending can resend the same
  event id and bytes.
- Re-enabling a Subscription resumes delivery of its parked notices under their
  original window, so an ordinary notice parked for longer than the window ends
  `exhausted` (`window_elapsed`) without being sent. Only a `match.withdrawn`
  notice committed while the Subscription was disabled gets a window that starts
  at the re-enable (THE-696). Changes made during the pause are never
  evaluated; there is no backfill command.
- Editing a Saved Query or Subscription applies from its commit on; there is
  no re-evaluation of earlier content under the new Version. A Subscription
  keeps its Saved Query, and deletion is permanent
  (no undelete). Pending notices of a deleted Subscription, including a
  `match.withdrawn` committed after the deletion, are never sent (THE-724).
- Delivery refuses private and internal receiver addresses after DNS
  resolution ([THE-695](https://linear.app/thevibecompany/issue/THE-695)), but
  a public hostname the operator configures is trusted: there is no egress
  proxy or per-destination allowlist. The harness lifts the refusal
  (`delivery.allow_private_destinations`, reported under `timing_overrides`)
  because its receivers listen on loopback.

**Observability**

- Prometheus metrics and OpenTelemetry export exist, but no dashboards or alert
  rules ship in `deploy/`.
- Counters and histograms reset when a process restarts.

**Supply chain**

- Release images carry SPDX SBOMs; the licences of dependency images
  (PostgreSQL, Weaviate, Temporal, SeaweedFS, TEI) are not inventoried.
- The E5 model's MIT declaration and training-data provenance are as published
  upstream; see [third_party/README.md](../third_party/README.md).
- The denylist does not scan the `multimodal-rag` submodule.

**Application upgrades**

- Expand/contract migration lint and a merge-base binary compatibility job cover
  inline ingestion, authorization and lexical search. There is no full release
  compatibility matrix yet. Historical migrations flagged in `migrations/legacy.json`
  retain their original risks. Additive DDL can briefly delay writes while locks
  are held. Contract execution closes the application rollback window; there are
  no down migrations. See the [upgrade guide](https://docs.quivr.thevibecompany.co/run-quivr/upgrade-quivr).
