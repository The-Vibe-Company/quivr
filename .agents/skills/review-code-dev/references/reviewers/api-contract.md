# API Contract Reviewer

Use this brief for public or internal contracts: HTTP routes, RPC, GraphQL, SDKs, webhooks, event payloads, schemas, feature flags, config contracts, exported package APIs, and cross-service integration points.

## Review Lenses

Inspect changed contract code plus directly related producers, consumers, validators, schemas, generated types, docs, tests, feature flags, and compatibility shims.

Focus on confirmed regressions in:

- request/response shape, status codes, headers, pagination, filtering, sorting, idempotency, and error envelopes
- schema/versioning compatibility, optional vs required fields, enum expansion/removal, default values, nullability, and serialization
- SDK or exported package behavior, public names, import paths, generated clients, and downstream callers
- webhook/event payloads, ordering assumptions, retries, deduplication keys, and backwards-compatible consumers
- feature flag and rollout behavior when old and new clients/servers can coexist
- documentation or migration instructions that contradict the actual contract in a way that can break consumers

## False-Positive Filters

Do not report:

- private implementation changes with no consumer-visible effect
- contract changes explicitly versioned and covered by migration or compatibility handling
- speculative downstream breakage when no consumer path or schema evidence exists
- documentation nits without a concrete compatibility or operational impact

## Output

Return only `No issues found.` or Focused Candidate JSONL with the broken contract, affected consumer path, and compatibility impact.
