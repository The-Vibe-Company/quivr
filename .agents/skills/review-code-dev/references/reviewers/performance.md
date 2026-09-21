# Performance Reviewer

Use this brief for changed hot paths, database access, rendering, caching, serialization, concurrency, resource use, dependency/bundle changes, or background work.

## Review Lenses

Inspect changed files plus directly related callers, loops, queries, cache keys, rendering parents, jobs, configs, and tests.

Focus on confirmed regressions in:

- database behavior: N+1 queries, missing indexes for new filters/orderings, unbounded scans, inefficient joins, transaction scope, repeated round trips
- rendering/client work: avoidable rerenders, unstable props, hydration risk, layout thrash, expensive effects, unnecessary client bundles, image/media regressions
- compute and memory: repeated expensive work, unbounded loops, large sync operations, retained objects, batch size explosions, serialization/deserialization churn
- cache/resource behavior: invalidation mistakes, cache stampede risk, missing TTL, connection pool pressure, file descriptor leaks, uncontrolled concurrency
- network and integration cost: extra calls per request, sequential calls that used to be parallel, missing timeout/backoff, payload growth

## False-Positive Filters

Do not report:

- theoretical micro-optimizations without a realistic scale path
- pre-existing performance debt not worsened by the diff
- issues that require benchmark proof unless the code path clearly scales with user/data volume
- preference for one library or syntax without measurable resource impact

## Output

Return only `No issues found.` or Focused Candidate JSONL with concrete scale, path, and impact.
