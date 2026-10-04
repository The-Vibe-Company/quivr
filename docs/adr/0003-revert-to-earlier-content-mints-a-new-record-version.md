# A revert to earlier content mints a new Record Version

Status: accepted

A correction whose bytes equal an earlier Record Version's, submitted without a source revision, is a revert: after A, then B, the source says A again. The revert becomes the Record's desired Version and then its Current Record Version, so the Record serves A again. We mint a **new Record Version with identical content**. We do not re-point the Record to the earlier Version.

Without a revision, a Version's slot is the canonical content digest. At acceptance, under the Organization journal lock, the engine reads the latest reservation of this digest for the Record:

- None: reserve the digest slot, as before.
- It is the Record's desired Version: converge on it. A resubmission of the current content is a `duplicate`.
- It is not desired, and the submission would become desired (its Source Position is not older than the desired one): reserve a new slot, the digest qualified by the submission's per-Record acceptance order. Its Version identity derives from that slot.
- It is not desired, and the submission is older than the desired Version: converge on the earlier reservation, which never replaces a newer desired Version. This is unchanged.

Submissions with an explicit source revision keep their revision slot; distinct revisions were already distinct Versions.

## Consequences

- **Receipts.** The revert's Receipt resolves `created` with the new Version. Replaying the same request under the same idempotency key returns that same Receipt, because the key is checked before any slot is chosen. The same content under a new key while the revert is desired resolves `duplicate` of the revert.
- **Idempotency keys** still identify a request, never content. No key changes meaning.
- **Change events.** The revert appends its own `record.accepted`, `record.materialized` and `record.retrieval_ready` events. Their stable identities derive from the new Receipt and Version, so they never collide with the earlier Version's events.
- **Matches and notices.** A Match is unique per Subscription Version and Record Version, so the revert is evaluated like any correction. A Subscription that matched A and not B gets a new Match on the revert, linked to its latest prior Match and announced as `match.corrected`. A Subscription that matched only B gets `match.no_longer_matches` for B's Match. Re-pointing to the earlier Version would have found its existing Match and announced nothing.
- **Lineage.** The revert's predecessor follows the same rule as any correction: the latest earlier-positioned reservation, B in A, B, A′. The earlier Version's order, position and predecessor stay immutable.
- **Purge.** A superseded Version still never becomes current again, so its projection objects can be purged permanently (see `docs/quivr-v2-remaining-limits.md`). The revert is projected afresh like any new Version.
- Stored bytes are shared: blobs are content-addressed per Organization, so the new Version adds rows but no new Blob.

## Considered Options

- **Reuse the earlier Version with a new ordering position.** Version rows carry their order, position and predecessor, so they would need a mutable ordering or a second history table. The earlier Version's Match would make monitoring skip the revert. Its `record.materialized` and `record.retrieval_ready` identities would repeat. The purge would have to un-purge or re-project a Version already declared dead.
