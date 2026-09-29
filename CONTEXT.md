# Quivr Ingestion and Retrieval

This context defines the domain-neutral language of the Quivr ingestion and retrieval engine. Vertical concepts such as a news-agency dispatch or monitoring signal belong to plugins rather than the engine vocabulary.

## Content model

**Corpus**:
A logical collection of records that share an access and retrieval boundary.
_Avoid_: Index, database

**Record**:
A stable, typed logical item owned by exactly one corpus, independent of any particular representation or revision.
_Avoid_: Document as a universal term, publication, signal

**Record Version**:
An immutable representation of a record observed at a particular point in its history. A source correction creates a new version, while progressive enrichment does not; distinct ingestion receipts may converge on the same version when source revision and content agree. A correction back to an earlier version's content is a new version, never a return to the old one.
_Avoid_: Mutable record, overwrite

**Record Version Manifest**:
The atomically published, immutable description of a record version's verified parts, blobs, checksums, provenance, and plugin-produced structure.
_Avoid_: Database row set, search projection

**Part**:
A typed, potentially hierarchical component owned by exactly one record version, with at most one parent and a key unique within its manifest.
_Avoid_: Attachment as a universal term

**Normalized Content**:
A source-faithful representation of content independent of retrieval-specific segmentation, embedding, or ranking choices.
_Avoid_: Chunked content, search projection

**Segmentation**:
A versioned derivation that divides normalized content into retrieval or processing units without changing the record version; multiple segmentations may coexist.
_Avoid_: Record version, source structure

**Derivation**:
A reproducible output whose identity is determined by its inputs, capability, producer digest, model, parameters, and output role. Identical executions converge; divergent output for the same identity is a conflict rather than an overwrite.
_Avoid_: Untracked generated file

**Blob**:
Immutable stored bytes whose identity and deduplication scope never cross an organization boundary; one blob may be referenced by multiple parts without sharing their provenance or business context.
_Avoid_: File when referring to stored content

**Upload Session**:
A short-lived, organization-scoped grant to transfer exact bytes to storage, carrying the expected size, checksum and media type. It yields a reusable Blob identity only after read-after-write verification; a session or its transfer URL is not itself content or a durable Blob identity.
_Avoid_: Transfer URL as Blob identity

**Relation**:
A typed link between records or parts.
_Avoid_: Dependency, association

**Annotation**:
A versioned derived fact about a record, record version, or part, produced without changing its identity.
_Avoid_: Metadata for derived results

**Projection**:
A rebuildable representation optimized for retrieval or presentation and derived from durable records and annotations.
_Avoid_: Source of truth, primary record

**Vector Space**:
A named and versioned embedding representation whose dimensions, distance metric, and generating model remain consistent within that space.
_Avoid_: Universal embedding, vector column

**Embedding Artifact**:
A durable derived representation of a part in a vector space, retained independently of any particular search projection.
_Avoid_: Vector index entry

**Projection Generation**:
An internally consistent, rebuildable search representation that pins its derivations and vector spaces and can coexist with another generation during validation and cutover.
_Avoid_: In-place index migration

**Archive Projection**:
A retrieval representation of historical records with its own availability, latency, and storage policy.
_Avoid_: Backup, source of truth

**Cross-modal Vector Space**:
A vector space in which queries and parts of different modalities can be compared directly.
_Avoid_: Modality-specific embedding

**Retrieval Candidate**:
A permitted record or part proposed by a search projection before optional fusion, diversification, or reranking.
_Avoid_: Final search result

**Retrieval Profile**:
A named, versioned latency and quality policy that selects the allowed retrieval and ranking stages for a query.
_Avoid_: Search engine configuration

## Continuous retrieval

**Saved Query**:
A stable, organization-owned identity for a retrieval request retained for repeated or automated evaluation.
_Avoid_: Subscription, alert

**Saved Query Version**:
An immutable definition of a saved query that fixes its query expression, corpus scope, retrieval profile, and temporal policy.
_Avoid_: Mutable saved query, subscription version

**Subscription**:
A stable, organization-owned instruction to evaluate a saved query continuously.
_Avoid_: Saved query, notification channel

**Subscription Version**:
An immutable subscription configuration that pins one saved query version and its evaluation and delivery policy.
_Avoid_: Match, delivery attempt

**Match**:
A durable, idempotent determination that one record version satisfies one version of a subscription. Reevaluation after progressive enrichment converges on the same match; a materially relevant source correction may create a linked match for the new record version.
_Avoid_: Delivery, search result

**Delivery**:
A stable logical external notification for one match, destination, and event kind; transport attempts may repeat without creating another delivery.
_Avoid_: Match, delivery attempt

**Delivery Attempt**:
One transport attempt for a delivery, recorded separately because external effects are at-least-once rather than transactional with Quivr.
_Avoid_: Logical delivery, match

**Record Key**:
The source-provided stable identity of a record within an organization, corpus, and source namespace, preserved across all of its versions.
_Avoid_: Version identifier, blob hash

**Source Namespace**:
A durable identity partition for record keys that survives connector replacement or reconfiguration.
_Avoid_: Connector instance, transport endpoint

**Source Position**:
An optional monotonic position supplied within a source namespace to order record revisions independently of delivery time; durable acceptance order is the fallback when none exists.
_Avoid_: Arbitrary source timestamp, ingestion receipt identifier

**Current Record Version**:
The eligible version selected for a record, replaced atomically only after a successor becomes searchable.
_Avoid_: Latest submitted version, latest created row

**Version Availability**:
The canonical readiness of a record version for retrieval, tracked independently from whether it is the record's current version and from optional enrichment progress.
_Avoid_: Receipt state, workflow status

**Ingestion Receipt**:
The immutable acknowledgement that the engine has durably accepted responsibility for one submission; it resolves exactly once as created, duplicate, withdrawal applied, or conflict. Processing and search availability belong to the associated record version rather than being copied onto the receipt.
_Avoid_: Processing completion, search availability

**Operation**:
A durable, trackable execution of a long-running administrative command such as a backfill, rebuild, cold-data restoration, or purge. Technical retries and restarts preserve its identity; rerunning a terminal operation creates a new linked operation.
_Avoid_: Workflow, ingestion receipt

**Change Event**:
An immutable, uniquely identified public fact describing a committed domain change, ordered within its organization for clients that consume the resumable change feed.
_Avoid_: Temporal history event, internal task

**Change Cursor**:
An opaque organization-scoped position from which a client can resume at-least-once consumption of the public change feed within its retention window; an expired cursor requires explicit resynchronization.
_Avoid_: Database offset, page number

**Tombstone**:
An explicit durable marker that immediately and permanently withdraws a record from retrieval, monitoring, and new delivery without erasing its identity or history; physical deletion remains a separate retention operation.
_Avoid_: Hard delete, missing record

## Governance

**Organization**:
The ownership and security boundary that contains one or more corpora.
_Avoid_: Corpus, deployment, user account

**Retention Policy**:
A versioned set of lifecycle rules governing the availability, tiering, and eventual deletion of records and their derivatives.
_Avoid_: Garbage collection policy

**Legal Hold**:
An explicit override that prevents destructive retention actions on protected content and the canonical artifacts it still references.
_Avoid_: Permanent retention policy

**Lifecycle State**:
The current logical availability of content or an artifact, independent of the storage provider implementing it.
_Avoid_: S3 storage class

**Purge Candidate**:
An item with no canonical reference, active use, or legal hold that has satisfied its retention policy and is awaiting verification and a recovery grace period before physical deletion.
_Avoid_: Deleted item

**Searchable Record**:
A materialized record version for which the engine has published the mandatory retrieval baseline, independently of optional multimodal enrichments that may arrive later.
_Avoid_: Fully processed record

**Progressive Enrichment**:
The availability model in which a record version becomes searchable as soon as its mandatory retrieval baseline is ready, then gains optional text, image, audio, or video derivations without changing its identity.
_Avoid_: Waiting for full processing, creating a new record version for derived output

**Backfill**:
The controlled processing or reprocessing of an existing historical range without changing the identity of its records.
_Avoid_: Real-time ingestion, replay when referring only to transport redelivery

## Extensibility

**Connector Instance**:
A configured acquisition endpoint bound to exactly one corpus and one source namespace that introduces external content into that corpus through the same ingestion commands as any client.
_Avoid_: Source when referring to collection mechanics

**Acquisition Checkpoint**:
The durable, connector-defined position from which a connector instance resumes acquisition, advanced only after the items fetched before it were durably accepted; re-fetching after a crash converges on the same receipts.
_Avoid_: Source position, change cursor

**Deposited Credential**:
A write-only secret supplied for a connector instance, encrypted at rest, versioned by replacement and optionally carrying an expiry; it is never returned or logged.
_Avoid_: API key, stored password

**Connector Health**:
The committed, evaluated condition of a connector instance's collection (active, silent, access error, credential expiring or disabled) that distinguishes a source refusing access from a source that simply published nothing new.
_Avoid_: Uptime, workflow status

**Connector Usage**:
The per-UTC-day count of source resources a connector instance read (current and previous day), reported by kinds whose source bills or rate-limits per resource; an estimate of what the source bills.
_Avoid_: Quota, cost

**Plugin**:
A versioned installation unit that contributes one or more extensions to the engine through public contracts.
_Avoid_: One plugin type per extension point

**Plugin Package**:
An immutable distribution unit containing a plugin manifest, schemas, and references to its executable artifacts.
_Avoid_: Mutable image tag, contribution

**Contribution**:
A named, typed extension supplied by a plugin, such as a connector, normalizer, enricher, projector, retriever, or subscription.
_Avoid_: Plugin when referring to one capability inside a plugin

**Capability**:
A named and versioned public contract that a contribution provides or consumes without depending on another plugin's identity.
_Avoid_: Plugin dependency, internal service

**Plugin Requirements**:
The data access, secrets, network dependencies, and resources a plugin declares so an installer can configure and assess it.
_Avoid_: Enforced sandbox policy, plugin configuration

**Plugin Trust Level**:
The support and provenance classification assigned by an installer to a plugin it has chosen to trust.
_Avoid_: Runtime permission system, sandbox profile

**Plugin Admission Policy**:
The deployment-specific rules that determine whether a plugin package may be installed or activated based on provenance and integrity evidence.
_Avoid_: Permission grant, runtime sandbox

**Plugin Generation**:
An activated, internally consistent set of plugin versions and configuration that owns a bounded set of work while newer generations may coexist.
_Avoid_: Deployment, plugin version

**Pipeline Plan**:
The immutable set and ordering of contributions resolved for one bounded processing execution.
_Avoid_: Live plugin registry, mutable workflow configuration

**Quarantine**:
A durable hold that withholds an accepted submission or record version from normal availability because a mandatory contribution could not safely complete.
_Avoid_: Retry queue, deletion

**Plugin Worker**:
An independently operated process that executes remote contributions through public engine contracts.
_Avoid_: In-process plugin, engine instance

**Plugin Manifest**:
The declaration a plugin ships to describe itself: its identity and version, compatibility ranges, contributions, configuration schema, required secrets, owned extension namespaces and limits.
_Avoid_: Record Version Manifest, package metadata

**Plugin Protocol**:
The versioned, language-neutral contract through which the engine discovers, checks and invokes a plugin's contributions.
_Avoid_: SDK API, internal plugin interface

**Plugin API Version**:
The semantic version of the plugin protocol, versioned independently of the engine; a plugin declares the range it supports and the engine refuses one outside it.
_Avoid_: Engine version, plugin version

**Normalizer**:
A contribution that turns one accepted blob of a routed media type into the parts, relations and extensions of that record version's manifest, without changing the record version's identity.
_Avoid_: Parser, converter, enricher

**Normalizer Route**:
The installation's mapping from an accepted blob media type to the normalizer that handles it, marked required or optional.
_Avoid_: Plugin registration, media type support

**Plugin Invocation**:
One uniquely identified call of a contribution for a specific input; retries of the same logical call share an idempotency key and must converge on the same output.
_Avoid_: Plugin request, job

**Invocation Fixture**:
A language-neutral local test input for a contribution: an input file, its media type and optional configuration, which tools turn into a plugin invocation through a local file reference.
_Avoid_: Test case, sample request

**Plugin Contract Runner**:
The tool that checks a plugin against the plugin protocol and normative fixtures using the engine's own validation, so passing it means the engine accepts the plugin.
_Avoid_: SDK test suite, integration test
