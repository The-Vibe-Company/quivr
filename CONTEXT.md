# Quivr Ingestion and Retrieval

This context defines the domain-neutral language of the Quivr ingestion and retrieval engine. Vertical concepts such as an Agency dispatch or monitoring signal belong to plugins rather than the engine vocabulary.

## Content model

**Corpus**:
A logical collection of records that share an access and retrieval boundary.
_Avoid_: Index, database

**Record**:
A stable, typed logical item known to the engine, independent of any particular representation or revision.
_Avoid_: Document as a universal term, publication, signal

**Record Version**:
An immutable representation of a record observed at a particular point in its history.
_Avoid_: Mutable record, overwrite

**Record Version Manifest**:
The immutable description of a record version's parts, artifacts, checksums, provenance, and plugin-produced structure.
_Avoid_: Database row set, search projection

**Part**:
A typed, potentially hierarchical component of a record version, such as text, structured data, an image, audio, or video.
_Avoid_: Attachment as a universal term

**Normalized Content**:
A source-faithful representation of content independent of retrieval-specific segmentation, embedding, or ranking choices.
_Avoid_: Chunked content, search projection

**Segmentation**:
A versioned derivation that divides normalized content into retrieval or processing units without changing the record version.
_Avoid_: Record version, source structure

**Derivation**:
A reproducible output whose plugin, model, parameters, inputs, checksums, and supersession lineage are recorded.
_Avoid_: Untracked generated file

**Blob**:
Immutable stored bytes that may be referenced by one or more parts without sharing their provenance or business context.
_Avoid_: File when referring to stored content

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
An internally consistent, rebuildable search representation that can coexist with another generation during validation and cutover.
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
A versioned retrieval request retained for repeated or automated evaluation.
_Avoid_: Subscription, alert

**Subscription**:
A durable instruction to evaluate a saved query against eligible new record versions.
_Avoid_: Saved query, notification channel

**Match**:
A durable determination that one record version satisfies one version of a subscription.
_Avoid_: Delivery, search result

**Delivery**:
An external effect attempted for a match through a configured channel.
_Avoid_: Match, subscription

**Record Key**:
The stable identity of a record within a corpus and connector namespace, preserved across all of its versions.
_Avoid_: Version identifier, blob hash

**Ingestion Receipt**:
The durable acknowledgement that the engine has accepted responsibility for a submitted record or blob reference.
_Avoid_: Processing completion, search availability

**Operation**:
A durable, trackable execution of a long-running administrative command such as a backfill, rebuild, restoration, or purge.
_Avoid_: Workflow, ingestion receipt

**Change Event**:
A compact public fact describing a committed domain change for clients that consume the resumable change feed.
_Avoid_: Temporal history event, internal task

**Change Cursor**:
An opaque position from which a client can resume consumption of the public change feed within its retention window.
_Avoid_: Database offset, page number

**Tombstone**:
An explicit durable marker that a record is withdrawn from active use without erasing its identity or history.
_Avoid_: Hard delete, missing record

## Governance

**Organization**:
The ownership and security boundary that contains one or more corpora.
_Avoid_: Corpus, deployment, user account

**Retention Policy**:
A versioned set of lifecycle rules governing the availability, tiering, and eventual deletion of records and their derivatives.
_Avoid_: Garbage collection policy

**Legal Hold**:
An explicit override that prevents destructive retention actions on protected content.
_Avoid_: Permanent retention policy

**Lifecycle State**:
The current logical availability of content or an artifact, independent of the storage provider implementing it.
_Avoid_: S3 storage class

**Purge Candidate**:
An unreferenced, unprotected item awaiting verification and a recovery grace period before physical deletion.
_Avoid_: Deleted item

**Searchable Record**:
A materialized record version for which the engine has published the minimum retrieval projection, independently of optional enrichments.
_Avoid_: Fully processed record

**Backfill**:
The controlled processing or reprocessing of an existing historical range without changing the identity of its records.
_Avoid_: Real-time ingestion, replay when referring only to transport redelivery

## Extensibility

**Connector Instance**:
A configured acquisition endpoint that introduces external content into a corpus.
_Avoid_: Source when referring to collection mechanics

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
A durable state that withholds a record from normal availability because a mandatory contribution could not safely complete.
_Avoid_: Retry queue, deletion

**Plugin Worker**:
An independently operated process that executes remote contributions through public engine contracts.
_Avoid_: In-process plugin, engine instance
