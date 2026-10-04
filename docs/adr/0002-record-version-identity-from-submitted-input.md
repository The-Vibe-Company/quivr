# Record Version identity derives from submitted input, not normalizer output

Status: accepted

When a normalizer Contribution turns a submitted Blob into a Manifest, the Record Version's identity (its digest) is still computed at acceptance from what was submitted: the source Blob, its media type and the submitted extensions, exactly as for built-in content. The normalizer's Manifest is published once for that Record Version. A retry with the same idempotency key must converge. Divergent output for the same key is a `normalizer_conflict`, following Derivation semantics, and never an overwrite. Re-normalizing with a newer plugin version is a Backfill, not a new Record Version.

## Considered Options

- **Identity from normalizer output.** A plugin upgrade, a non-deterministic extractor or a changed configuration would then mint new Record Versions for unchanged sources. That would break correction semantics, duplicate Matches and make acceptance depend on a remote process being available.

## Consequences

- Acceptance never waits for a plugin: normalization runs after acceptance, in processing, and the engine can quarantine a Record Version whose normalization cannot complete.
- Plugins must be deterministic per idempotency key within one plugin version. The Contract Runner checks this through deterministic replay.
