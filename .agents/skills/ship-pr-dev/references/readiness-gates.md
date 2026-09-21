# PR Readiness Gates

Use these gates to decide whether the PR can be presented as merge-ready.

## Hard Blocks

Stop before push or mark the PR blocked when any of these are true:

- unresolved merge conflicts
- any failed, cancelled, errored, stale, or partially inspected visible non-skipped PR check/check suite on the latest pushed commit
- queued, pending, or in-progress CI on the latest pushed commit for a `ship` or `update-pr` run
- unreviewed P0/P1 `review-code-dev` findings
- confirmed P2 findings without a documented accepted-risk reason
- frontend diff without the frontend lens inside `review-code-dev`
- CI unavailable or not fully green on the latest pushed commit for a `ship` or `update-pr` run, unless the user explicitly requested local-only work
- secrets in the diff, logs, fixtures, or config
- destructive migration without rollback/deploy-order notes
- auth, billing, permissions, export, or privacy behavior changed without tests or clear verification
- frontend critical path changed without at least one rendered or interaction-level check when tooling is available
- PR branch includes unrelated user work that cannot be safely separated
- push/PR credentials are missing
- use of `--no-verify`, skipped tests, disabled checks, weakened lint rules, or equivalent bypasses

## Required Evidence

`verification.md` should include:

- exact command
- pass/fail/skipped
- why it was selected
- important output summary
- timestamp or sequence marker
- whether code changed after the command

`review-gate.md` should include:

- `review-code-dev` mode and required lenses
- base branch
- artifact path
- finding counts by severity
- fixed findings
- accepted-risk findings
- requested and effective reviewer routing when observable
- focused reviewer count versus the tier budget
- reason if the review ran inline
- frontend lens coverage and impacted user paths when relevant

`ci.md` should include for pushed PRs:

- latest pushed SHA
- PR URL
- check names and final states
- whether every visible non-skipped latest-SHA check was inspected
- failed check log summary and first causal error when applicable
- fix attempts per check

## Severity Policy

- P0: never ship.
- P1: never present as merge-ready until fixed or conclusively false positive.
- P2: fix by default. If not fixed, document why it is accepted risk and make the PR non-merge-ready unless the human explicitly accepts it.
- P3: fix when cheap. Otherwise list in PR notes as follow-up or polish.

## Verification After Changes

Any source change after a passing test, build, or review can stale that evidence. Re-run the affected verification. If only PR text, changelog, or comments changed, say why verification remains fresh.

## Loop Limits

Default caps:

- 3 implementation/fix cycles
- 2 full verification cycles after source changes
- 2 full `review-code-dev` gate runs; prefer a targeted failed-lens rerun after bounded fixes
- 3 correction attempts for the same CI check

When a retry or count cap is reached (never a time reassessment checkpoint), stop and produce a blocked handoff with the current evidence and the smallest next action.

## Human Decision Gates

Ask or stop when the next step changes ownership or risk:

- splitting mixed unrelated work
- rewriting public history
- dropping files
- accepting unresolved P2 risk
- shipping with unavailable checks
- creating a draft PR despite blockers
- changing the target base branch
- treating a frontend diff as merge-ready without frontend coverage inside `review-code-dev`
- accepting CI failure as unrelated or environmental

Routine commit, normal push, and PR creation are part of this skill when the user asked to ship a PR.
