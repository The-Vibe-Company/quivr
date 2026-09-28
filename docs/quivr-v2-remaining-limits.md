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
| Attaching embeddings briefly hides a Record from lexical (BM25) search. Enrichment of a Version that is no longer eligible retries forever | Transient false negatives in lexical search; wasted worker capacity | [THE-690](https://linear.app/thevibecompany/issue/THE-690) |
| Webhook destinations are not filtered against private or internal addresses | Server-side request forgery from the worker's network | [THE-695](https://linear.app/thevibecompany/issue/THE-695) |
| No withdrawal notice for a Subscription disabled when the withdrawal is dispatched | A paused consumer keeps an alert for withdrawn content | [THE-696](https://linear.app/thevibecompany/issue/THE-696) |
| Change events are never physically pruned; expiry is computed from cursor age | Unbounded journal growth | [THE-697](https://linear.app/thevibecompany/issue/THE-697) |
| Concurrent rebuilds of one Corpus can activate out of order; abandoned generations are never purged | Active configuration can lag the latest request; projection store growth | [THE-698](https://linear.app/thevibecompany/issue/THE-698) |
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
- Migrations may break in-flight work. Restarts are reported, not avoided, and
  there is no expand/contract or compatibility matrix.

**Ingestion**

- Batches share the global 5 s request deadline, upload time included.
- A Blob-only Manifest is stored but not extracted, so it is `blocked` for search.
- No orphan-upload sweeper or retention policy exists.

**Search and rebuild**

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

**Observability**

- Metrics are a small Prometheus-text set on the probe listeners. There are no
  dashboards, distributed tracing or alerting.

**Supply chain**

- Container image licences are not inventoried.
- The E5 model's MIT declaration and training-data provenance are as published
  upstream; see [third_party/README.md](../third_party/README.md).
- The denylist does not scan the `multimodal-rag` submodule.
