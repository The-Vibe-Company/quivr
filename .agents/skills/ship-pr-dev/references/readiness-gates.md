# PR Readiness Gates

Use these gates to decide whether the PR can be presented as merge-ready.

## Hard Blocks

These conditions prevent a ready claim. Fix what is safely in scope and wait for healthy CI; a failing gate is work to finish, not by itself a reason to end the task. Keep unverified changes local unless repairing an existing PR or the user requested a draft. Produce a blocked handoff only when progress needs unavailable access, an external change, or a user decision:

- unresolved merge conflicts
- any failed, cancelled, errored, stale, or partially inspected visible non-skipped PR check/check suite on the latest pushed commit
- queued, pending, or in-progress CI on the latest pushed commit for a `ship` or `update-pr` run
- unresolved critical/high (P0/P1) Alibaba review findings
- missing/incompatible review-code-dev v2, failed OCR setup, missing independent reviewer, or incomplete review coverage
- confirmed medium (P2) findings without explicit human acceptance recorded
- frontend diff without the frontend lens inside `review-code-dev`
- CI unavailable or not fully green on the latest pushed commit for a `ship` or `update-pr` run, unless the user explicitly requested local-only work
- secrets in the diff, logs, fixtures, or config
- destructive migration without rollback/deploy-order notes
- auth, billing, permissions, export, or privacy behavior changed without tests or clear verification
- frontend critical path changed without at least one rendered or interaction-level check when an authorized runnable app or accessible preview and suitable tooling are available; a disabled launch policy with no accessible preview is an explicit visual-verification limitation, not a requirement to start the app. Repository-required checks remain required.
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

- `review-code-dev` package version, Alibaba delegation backend, OCR version and required focus areas
- base branch
- artifact path
- finding counts by severity
- fixed findings
- accepted-risk findings
- requested and effective reviewer routing when observable
- exact reviewed branch SHAs and workspace fingerprint; evidence bound to the latest candidate/pushed source
- reviewed, excluded and skipped counts reconciled against the full changed-file inventory
- one isolated primary reviewer and no review subagents
- frontend lens coverage and impacted user paths when relevant

`ci.md` should include for pushed PRs:

- latest pushed SHA
- PR URL
- check names and final states
- whether every visible non-skipped latest-SHA check was inspected
- failed check log summary and first causal error when applicable
- fix attempts per check

## Severity Policy

- critical / P0: never ship.
- high / P1: never present as merge-ready until fixed or conclusively false positive.
- medium / P2: fix by default. If not fixed, document why it is accepted risk and make the PR non-merge-ready unless the human explicitly accepts it.
- low / P3: fix when cheap. Otherwise list in PR notes as follow-up or polish.

## Verification After Changes

Any source change after a passing test, build, or review can stale that evidence. Re-run the affected verification. If only PR text, changelog, or comments changed, say why verification remains fresh.

## Completion And Reassessment

For `ship` and `update-pr`, finish the scoped corrections, fresh local verification, independent review, normal push/PR update, every visible non-skipped latest-SHA CI item green, and the report-only learning pass before handing back success. For `prepare` and `local-handoff`, finish local verification, review, and the learning pass; report remote CI as unverified and the branch as locally prepared, without pushing or claiming merge readiness.

Use counts to notice a failing approach: reassess after three implementation/CI corrections, two failed worker attempts on one root cause, or before a third full verification/review cycle. Counts and elapsed time are diagnostic checkpoints, not automatic stop conditions. User-specified budgets or stop instructions still bind.

At a checkpoint, record the failing gate, first causal error, attempted fixes and their effects, and the next evidence-backed hypothesis in `ship-state.json`. Take over from a stalled worker or change the diagnostic approach. Continue scoped repairs and affected checks when the evidence supports a useful next step; rerun a full review only when scope or architecture changed enough to require it.

When the same action yields the same failure with no new evidence, stop repeating that action. Inspect the missing evidence or use a bounded investigator within the existing agent budget. If that establishes that progress requires unavailable access, an external change, or a user decision, record that concrete dependency and return a blocked handoff. Never weaken a gate to escape a loop or label a PR ready because a retry count was reached.

## Human Decision Gates

Ask or stop when the next step changes ownership or risk:

- splitting mixed unrelated work
- rewriting public history
- dropping files
- accepting unresolved medium (P2) risk
- shipping with unavailable checks
- creating a draft PR despite blockers
- changing the target base branch
- treating a frontend diff as merge-ready without frontend coverage inside `review-code-dev`
- accepting CI failure as unrelated or environmental

Routine commit, normal push, and PR creation are part of this skill when the user asked to ship a PR.
