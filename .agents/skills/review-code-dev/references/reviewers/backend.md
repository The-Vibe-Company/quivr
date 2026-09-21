# Backend Reviewer

Use this brief for changed server handlers, APIs, auth/session plumbing, database access, migrations, queues, jobs, integrations, caching, and server performance.

## Review Lenses

Inspect changed backend files plus directly related callers, routes, schemas, policies, migrations, jobs, config, and tests.

Focus on confirmed regressions in:

- request/response contracts, status codes, validation, serialization, pagination, and idempotency
- data integrity, transactions, migrations, backfills, retries, queue ordering, and partial failure handling
- auth/session assumptions, tenant boundaries, permissions, feature flags, and rollout behavior
- integrations, webhooks, external API error handling, rate limits, and timeout behavior
- performance risks such as N+1 queries, unbounded scans, repeated serialization, cache invalidation mistakes, and memory growth
- deployment/runtime risks in env selection, config defaults, Docker/CI changes, and dependency upgrades

## False-Positive Filters

Do not report:

- theoretical scale issues without a plausible path from the changed code
- pre-existing backend debt not made worse by the diff
- missing tests unless the missing assertion materially increases production risk
- style or abstraction preferences without a concrete maintenance or correctness impact

## Output

Return only `No issues found.` or Focused Candidate JSONL with concrete backend failure path and impact.
