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
key; explicit cancel and rerun are described below.

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
the rebuild and retrieval configuration commands; future commands define their
result types explicitly.

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
- Each segment's lexical object is its permanent keyword-search anchor. It is
  never updated or deleted (THE-690). The engine re-indexes an updated object
  under a new document id, and a BM25 query does not read its index atomically,
  so updating or deleting the object serving a segment can briefly hide it.
  - Attaching an embedding creates a separate enriched object (same text, plus
    the vector) beside the anchor, create-only, and verifies it.
  - It then deletes the segment's other enriched objects in that generation,
    by filter.
  - Every step is idempotent, so a retry converges on the anchor plus one
    enriched object.
  - An enriched segment therefore has two objects, and briefly three while its
    embedding is replaced. Search deduplicates by segment and fetches three
    times the maximum page size, so a `searchable` Record stays visible and
    duplicates never shorten a page.
  - Cost: every enriched segment's text is indexed twice for BM25 (about twice
    the lexical index). Deduplication keeps one hit per segment, but BM25
    statistics count both objects, so lexical scores can shift slightly, most
    in a Corpus that mixes enriched and not-yet-enriched segments.
  - Superseded and withdrawn Versions keep their objects; hydration hides them.
    They still use candidate slots, so heavy churn in a Corpus can shorten a page
    (see `docs/quivr-v2-remaining-limits.md`).
- Enrichment of a Version that is no longer its Record's current eligible
  Version (withdrawn, superseded or quarantined) ends without touching the
  projection.
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

### Cancel and rerun (THE-659)

`POST /v0/operations/{operation_id}/cancel` and `/rerun` accept the existing
`ActionRequest`, require `operations:write` and the Operation's Corpus in the
key's scope (otherwise 404), and return HTTP 202 with an Operation.
`projection_rebuild` and `retrieval_configuration` Operations are controllable;
a kind whose worker does not honor cancellation is rejected with 422
`unsupported_operation_kind`.

Cancellation:

- A `queued` Operation becomes `canceled` at once. No worker step has begun, so
  no effect can be in flight; a workflow dispatched earlier sees `canceled` and
  ends without work.
- A `running` Operation becomes `cancel_requested`. The worker checks state at
  the start of every step and at every commit, stops, and records `canceled`.
  A cancellation normally settles within one batch. A dependency call that is
  already in flight can delay it by up to one step timeout (2 min) plus one
  retry backoff (30 s maximum); while PostgreSQL itself is unavailable it stays
  `cancel_requested`, and no effect can commit in the meantime.
- Coverage, counters and route activation all commit only while the Operation
  is `running`, under the same Organization journal lock and Operation row lock
  as cancellation. Completion racing cancellation has exactly one winner: if
  activation commits first the Operation is `succeeded` and the cancel request
  returns it; otherwise activation refuses and the Corpus keeps its prior
  generation. A partially covered target is never activated.
- Cancellation does not undo committed effects. Target-generation objects
  already written stay in the shared collection and are never served; no purge
  runs.
- A terminal failure found after a cancel request settles as `canceled`.
- Idempotency follows Operation state. The key is validated but not stored:
  repeating a cancel with any key returns the current state and appends no
  event, and a terminal Operation keeps its outcome.

Rerun:

- Only a terminal (`succeeded`, `failed`, `canceled`) Operation can be rerun;
  otherwise 409 `operation_not_terminal`.
- The caller's current permissions are revalidated: `operations:write` plus the
  originating command's permission (`projections:rebuild` for a rebuild,
  `corpora:write` for a retrieval configuration), and Corpus scope.
- The rerun is a new Operation with a new ID, a new target generation and
  `previous_operation_id` naming the source, committed with its dispatch intent
  and `operation.updated` before HTTP 202 and a `Location` for the new
  Operation. Reruns chain: a rerun can itself be rerun once terminal.
- Idempotency is Organization + source Operation + key; the same key returns the
  same rerun, a changed canonical request conflicts with 409
  `idempotency_conflict`. Rerun keys never collide with keys sent to the rebuild
  route. Technical retries of the rerun keep its own identity.
- The source Operation and its outcome are unchanged.

State changes (`queued`, `running`, `cancel_requested`, `canceled`,
`succeeded`, `failed`) each append one `operation.updated` to the shared journal.

## Retrieval configuration (THE-660)

`PUT /v0/corpora/{corpus_id}/retrieval` (`ConfigUpdate`) changes the logical
source-field mappings of one Corpus. It requires both `corpora:write` and
`operations:write` (otherwise 403) and the Corpus in the key's scope (otherwise
404). Invalid input is 422 without an Operation: `invalid_mapping` for a name
that is not logical (`^[a-z][a-z0-9_]{0,63}$`, so engine names such as
`generationId` or `title^2` are rejected), a pointer outside `/manifest`,
`/provenance` or `/extensions/{declared namespace}`, a search role on a
non-text type, or duplicate names and roles; `unsupported_profile` for an
uninstalled `plugin_profile`. Corpus creation applies the same validation.

- The configuration is resolved first: a pinned profile's default fields, then
  explicit fields overriding them by logical name. The only built-in profile,
  `example.editorial`, is an illustrative profile paired with the example
  extension namespace so resolution and overrides can be exercised without a
  plugin platform. It is not a product default and nothing selects it
  implicitly.
- Acceptance commits, before HTTP 202 and `Location`, a `retrieval_configuration`
  Operation whose new target generation pins the resolved configuration and
  the Corpus's next configuration version. Idempotency is Organization + Corpus
  + route + key over the resolved request.
- The same rebuild worker as `projection_rebuild` builds the target from
  canonical text and stored vectors, without inference, and activates it by
  the same atomic route switch.
- The effective configuration returned by `GET /v0/corpora/{corpus_id}` is the
  one pinned by the Corpus's routed generation. It therefore changes exactly at
  validated cutover. Failure, cancellation or an unfinished build leave the
  prior configuration effective and queryable. Recovery after activation
  recognizes the same target, so configuration and generation can never
  diverge. v0 does not expose a pending configuration's content: the
  configuration Operation reports its progress and outcome, and the Corpus read
  reports the configuration it actually serves. A client that needs the
  requested fields keeps its own request.
- The latest accepted configuration wins. Accepting one cancels older pending
  configuration Operations of the Corpus (queued at once, running by request).
  A plain rebuild or a rerun pinned to an older configuration than the one the
  Corpus serves fails with `retrieval_configuration_superseded` instead of
  reverting it. A plain rebuild pins the effective configuration. A rerun of a
  configuration Operation re-targets that Operation's configuration.

Effect on the projection:

- A search field named `title` replaces the projected title (weight 2 in the
  pinned lexical/hybrid profile).
- Every other search field adds its text once per Record Version, after the
  canonical text of the first (title-bearing) segment. Placing it once keeps a
  long Record from multiplying the mapped terms' frequency by its segment count.
- `string_array` values are joined. A missing or mistyped value contributes
  nothing and never fails the build.
- Corpora without search mappings produce exactly the unmapped projection, so
  the ranking defaults are unchanged.
- **Provenance consequence.** Excerpts, offsets and segment provenance stay
  canonical. A lexical hit, or the lexical branch of a hybrid hit, can therefore
  match on mapped text that its excerpt does not contain: the title for any
  segment of the Version, other mapped fields for the first segment. Clients
  must not assume that the excerpt contains the query terms.
- Stored vectors are not recomputed, so semantic retrieval ignores mapped
  fields.
- Filter roles are validated, typed, versioned and returned in the effective
  configuration. They are not materialized in the engine, and v0 exposes no
  public filter parameter.
- Unmapped source data stays readable through the Version read.

Upgrade note (migration `015_retrieval_configs.sql`): creation configurations
stored before this slice were never applied to any projection. The migration
therefore resets them to the configuration those Corpora actually serve, and
`effective_retrieval.fields` reads empty afterwards. Owners reapply mappings
through `PUT /v0/corpora/{corpus_id}/retrieval`.

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
