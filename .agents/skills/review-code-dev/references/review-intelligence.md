# Review Intelligence

Use this reference when running a standard or deep review, or when focused helpers produce conflicting candidates.

## Imported Patterns

The review harness should combine four proven patterns:

- GStack-style review army: specialists return structured candidates with confidence, fingerprints, evidence, and a narrow category.
- Cubic-style discipline: gather git evidence first, inspect every changed file, use direct reads before delegation, and publish parseable findings only.
- Improve-style advisor rigor: subagents are leads, not facts; the lead reviewer verifies every citation and records rejected candidates so the same false positive does not recur.
- Autoreview-style closeout: stop when the reviewed bundle exits cleanly or when the remaining issue is out of scope; do not run extra panels just to get a nicer clean line.

## Intent And Scope Drift

Before judging implementation quality, identify what the branch was supposed to do.

Preferred intent sources:

1. user request or delegation brief
2. PR/MR description
3. plan/spec files
4. issue references
5. commit messages
6. TODO/backlog files

Classify scope:

- `clean`: diff matches stated intent
- `scope_drift`: diff includes unrelated behavior, opportunistic refactors, or unexplained blast-radius expansion
- `missing_requirement`: stated behavior is absent or partial
- `unverifiable`: intent depends on another repo or external system

Scope drift is not automatically a P1/P2 finding. It becomes a finding only when it changes user/system behavior, merge risk, release risk, or owner boundaries. Otherwise record it in `scope-audit.md`.

## Confidence Calibration

Every candidate finding gets `confidence` from 1-10:

- `9-10`: verified by reading the exact code path and directly related caller/consumer
- `7-8`: high-confidence pattern with concrete evidence and plausible impact
- `5-6`: plausible but needs caveat; do not put in `review.md` unless impact is severe and uncertainty is explicit
- `3-4`: appendix only
- `1-2`: suppress unless it would be P0 if true

Promotion rule: `review.md` should contain only candidates with `status` `accepted` or `downgraded` and confidence `>= 7`, except P0/P1 issues where the risk is concrete and the uncertainty is part of the impact.

## Pre-Emit Verification Gate

Before a candidate reaches `review.md`, verify:

1. motivating line: quote or cite the exact source line that triggered the concern in `candidate-findings.json`
2. failure path: identify the caller, input, state, or deployment condition that reaches the line
3. introduced risk: explain why this diff created or worsened the issue
4. impact: say what user, system, data, security, or delivery outcome breaks
5. false-positive check: read the nearby guard, middleware, schema, ADR, tests, or config that could make the concern impossible

If any item is missing, classify the candidate as `unverified` or `rejected`; do not publish it as a final finding.

## Fingerprints And Deduplication

Every candidate gets a stable `fingerprint`:

```text
<file>:<line-or-contract>:<category>:<root-cause-key>
```

When multiple helpers report the same fingerprint:

- keep the candidate with the strongest evidence
- merge source helper names into `confirming_sources`
- boost confidence by at most 1, capped at 10
- record duplicates in `candidate-findings.json`

Do not publish duplicate symptoms. Publish the root cause once.

## Specialist Selection

Select specialists by risk, not by file extension alone:

- frontend: UI, accessibility, responsive layout, CSS, client state, forms, UX copy, visual-system drift
- backend: server behavior, APIs, data access, background jobs, integrations, caching
- tests: missing negative/edge/regression assertions for changed behavior
- security: auth, secrets, injection, data exposure, file/network/process boundaries, prompt injection
- architecture: owner boundaries, refactors, abstractions, duplication, long-term change cost
- performance: N+1, unbounded work, rendering churn, bundle growth, cache/resource issues
- data-migration: rollback, locks, backfills, index safety, mixed-version deploy hazards
- api-contract: request/response compatibility, schema/versioning, webhook/SDK/event changes
- red-team: adversarial gap finder after other specialists, only for large or high-risk diffs

Red-team runs after initial candidates exist. It looks for what the other passes missed, not for another generic review.

## Cross-Model Or Independent Challenge

For deep reviews, security-sensitive changes, or diffs over 200 changed lines, run one adversarial challenge when the runtime makes it safe:

- another model family if available
- a fresh subagent with no checklist bias
- an agent-process in read-only mode
- inline adversarial pass as fallback

The challenge is advisory. The primary reviewer still verifies every candidate before publishing.

## Stop Rules

Stop the loop when:

- coverage is complete or explicitly partial
- all candidates are classified
- remaining candidates are out of scope, unverified, duplicates, or follow-ups
- artifact validation passed or failed once after one repair

Do not keep reviewing just to get a cleaner "no issues" sentence. Do not run an extra panel after a clean, verified pass unless the user explicitly asked for a panel.
