# Public text search and projection rebuild

Decision ticket: [THE-640](https://linear.app/thevibecompany/issue/THE-640).
This closes the missing transport seams found by the THE-550 proof. The
[OpenAPI](../contracts/http/v0/openapi.yaml) remains authoritative. It does not
change THE-545's ranking seed or resolve the hybrid relevance deficit measured
in THE-550.

## Search

`POST /v0/search` accepts `query`, an explicit nonempty `corpus_ids` array, optional
`mode` (`lexical`, `semantic`, `hybrid`), `profile` (`fast`, `balanced`, `deep`) and
`limit`. Defaults are hybrid, balanced and 10 results; the initial limit is 50.
All requested Corpora must be authorized within the caller's Organization.
Reject an unsupported profile, mode or query length; never silently truncate,
drop unauthorized scopes or substitute an empty success for a dependency error.
This first route has no arbitrary metadata filter language or pagination.

Resolve an immutable profile version before searching. Compile mandatory
Organization/Corpus filters, obtain candidates, then hydrate and recheck current
version, access, quarantine and Tombstone against canonical state. Return up to
the requested number of eligible segment hits, assigning contiguous ranks after
filtering. Fewer hits do not assert an exhaustive count or a stable snapshot.
Lexical-only coverage remains usable; semantic-only retrieval requires vectors.

Each hit names the Record, Version, Part key, segment, Segmentation and logical
Projection Generation. If the segment has an Embedding Artifact, return its ID
and Vector Space together. Their presence describes coverage, not which branch
caused the hit's rank. All hits inherit the response's resolved profile identity.
An empty response still identifies that profile.

The excerpt is an exact slice of the referenced canonical normalized Part,
bounded by `[start,end)` Unicode code-point offsets. These are neither UTF-8 byte
offsets nor JavaScript UTF-16 indices. Verify its bounds and text during hydration;
do not rewrite or inject highlights into it. The returned Availability describes
the same canonical eligibility check. Raw engine scores, explanations, physical
collection names and workflow identities stay internal.

The initial contract deliberately exposes semantic identities rather than a
search-engine query dialect. Rich filters, other modalities and pagination can
extend it when those slices have their own verified behavior.

## Rebuild initiation and observation

`POST /v0/corpora/{corpus_id}/rebuilds` accepts the existing `ActionRequest`
(`idempotency_key`) and requires `projections:rebuild` permission on that Corpus.
It commits a `projection_rebuild` Operation plus durable dispatch intent and
returns HTTP 202 with a `Location` pointing to its Operation read route. It does
not synchronously wait for reconstruction.

Idempotency is scoped to Organization, Corpus and the rebuild route. Replaying
the same key/request returns the same Operation even after completion; changing
the canonical request conflicts. Retries and worker restarts keep both Operation
identity and the target generation. A new independent rebuild command uses a new
key; explicit rerun/cancel retain the existing Operation semantics.

The worker rebuilds from canonical content and durable artifacts, validates the
target generation, then activates it under the existing currentness/withdrawal
rules. If physical collections cover multiple Corpora, rebuilding one Corpus
must preserve the others. The transport does not mandate a collection layout or
permit a caller to operate on an unauthorized Corpus.

`GET /v0/operations/{operation_id}` exposes the durable state, target `corpus_id`,
bounded counters and errors. A succeeded rebuild requires
`result.projection_generation_id`, an opaque logical ID. Progress is optional;
server retry attempts are not new Operations or public workflow IDs. Operation
creation/state changes enter the shared public journal. The result shape covers
this initial rebuild command; future commands define their result types explicitly.

An infrastructure outage can leave work running/retrying. It must not erase an
accepted Operation or invent successful activation. If a worker dies after
activation but before recording success, recovery recognizes the same target
generation and completes the existing Operation. Live potentially breaking
evaluation schema migrations are separate from rebuilding search projections.

### Implemented behavior (THE-658)

- PostgreSQL routes each Corpus to one logical generation. A Corpus without a
  route uses the default generation. All generations share one physical
  collection, and each projected object carries its logical generation.
- Search sends one query covering every requested (Corpus, routed generation)
  pair. Each hit's `projection_generation_id` is its Corpus's routed generation.
- Promotion, enrichment and hydration all check routing. They serialize with
  activation on the Organization journal lock. Work still aimed at a replaced
  generation fails its guard and retries on the new route.
- The worker handles bounded batches of current eligible Versions:
  - It re-derives each segmentation locally from canonical text and requires the
    stored segmentation digest to match.
  - It publishes into the target generation.
  - It reuses verified stored vectors for Versions that the routed generation
    serves with vectors.
  - It never calls inference. A missing, corrupt or mismatched artifact fails
    the Operation with a bounded error (`embedding_artifact_unavailable`,
    `embedding_artifact_corrupt`, `segmentation_mismatch` or, when the
    canonical text objects themselves are missing or fail their checksum,
    `canonical_content_unavailable`). Object-storage unavailability is
    transient and retried.
  - Versions not yet enriched go into the target lexical-only and are enriched
    later on whichever generation is routed.
- Activation runs in one transaction. It rechecks coverage gaps left by
  concurrent promotions or enrichment, and if any remain it reconciles and tries
  again. Otherwise it installs the route, records `succeeded` with the result,
  and appends `operation.updated`.
- Transient failures leave the Operation `running` with no errors. Retries use
  capped backoff (30 s maximum) and are logged with Operation ID and attempt.
- `operation.updated` is emitted only on state transitions. Counters (`indexed`,
  `vectors_reused`) are only visible through the Operation read.

## Verification boundary

Contract fixtures cover semantic and lexical-only hit representations, non-ASCII
excerpts, queued/succeeded rebuilds, forbidden raw score fields, required Corpus
scope and paired embedding provenance. Generated Go/Python/TypeScript transport
checks follow the [existing reproduction guide](../contracts/http/v0/README.md).
Cross-field excerpt bounds, authorization and contiguous ranking require runtime
checks in addition to JSON Schema validation.

The [disposable proof and report](https://github.com/The-Vibe-Company/quivr-v2/blob/1e4446a/prototype/end-to-end/evidence/public-search-report.md)
replace both `/prototype/` routes, persist segment/embedding references, dispatch
rebuilds asynchronously and poll the public Operation endpoint. The rerun validates
515 engine response captures, including search/rebuild and structured rejection
responses. Same-key replay and queued work surviving a worker stop pass, alongside
the existing text/monitoring/recovery journey. Only the receiver's private control
interface remains outside OpenAPI validation.

The contract checks preserve 24 fixtures in Go, Python and TypeScript and pass
31 boundary checks. The semantic/hybrid relevance measurements are unchanged;
the hybrid deficit remains open. This does not expand the proof's single-scope,
short-text or quiescent-rebuild evidence into production ACL, long-document or
concurrent catch-up guarantees. Runtime source stays on its prototype branch.
