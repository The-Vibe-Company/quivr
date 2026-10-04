# Ship PR Delivery Loop

The coordinator owns scope, architecture, writes to Git, PR state, and final acceptance. Workers receive bounded work orders from `agent-routing.md`.

## A. Preflight

1. Resolve the repository, branch, PR target, and `<base>...HEAD` comparison.
2. Capture status and separate in-scope, supporting, scope-drift, and unrelated dirty files.
3. Read repository guidance and referenced issue/spec/PR when accessible. If the handoff includes an approved plan, read its original request or ticket and relevant context, record approval and any deviations, and inventory which steps are already complete. Standalone Ship PR does not require a plan.
4. Classify impacted surfaces and the trivial/standard/deep budget tier.
5. Freeze goal, non-goals, checks, and worker limits in `ship-state.json` and `agent-budget.json`.

Stop if unrelated work cannot be separated safely. Never stash or discard it without approval.

## B. Implementation

Use the coordinator for trivial work and decisions that change scope or architecture. Use one write worker for standard/deep work only when owned files and acceptance checks are stable. Continue remaining authorized plan steps without restarting completed work; seek a decision for material departures from approved criteria.

The worker may edit only its owned scope and run explicitly allowed targeted checks. It never performs Git or remote actions. The coordinator inspects the resulting diff, resolves assumptions, and integrates it.

Resume the same worker for a follow-up. After two failed attempts on one root cause, the coordinator takes over diagnosis using `readiness-gates.md`'s reassessment policy. Never run simultaneous writers.

## C. Verification

Discover checks from package scripts, task runners, CI, and docs, including `docs/agents/plan-pr-validation.md` or its configured equivalent. Run quick static checks, lint, typecheck, targeted tests, broader tests, build, schema/migration checks, then UI smoke/visual checks as relevant. Launch an app only according to user or repository instructions. Capture the changed screens and interactions when feasible, and record why any visual evidence is unavailable. Apply the existing frontend critical-path gate rather than treating every unavailable screenshot as a blocker.

Record exact commands and skipped checks. Rerun affected checks after any source change. Deterministic checks are coordinator work, not delegation work.

## D. Single Review Gate

Follow `review-gate.md` for the `review-code-dev` v2 Alibaba contract: version check, portable OCR setup, frozen branch plus in-scope workspace coverage, one isolated reviewer, upstream severity and required artifacts. Provide the original need and approved plan when supplied, with a criterion-to-implementation-to-evidence map; require this single reviewer to assess both. Use the risk tier as a depth hint and frontend/security/API concerns as focus instructions within the same review.

Fix critical/high/medium findings under `readiness-gates.md`. Resume the same primary for affected files/contracts after corrections; no old specialist router, parser scripts, or native fallback is assumed. Review remains read-only and the coordinator owns every fix and Git operation.

## E. Commit And PR

Recheck status, stage intentional paths, inspect the staged diff, and use commitzen commits. Push normally. Create or update a PR only after local gates pass unless the user explicitly requests a visible draft with blockers.

PR titles are commitzen. The body must distinguish verified facts, skipped checks, residual risk, and human decisions. For UI changes, add shareable current screenshots to the PR through an available authorized mechanism. A machine-local path is not a PR attachment; if no mechanism is available, state the missing attachment precisely.

## F. CI To Green

For the latest pushed SHA, inventory all visible required/optional checks, statuses, suites, and workflow runs. Use deterministic provider/CLI waiting while work is queued.

For a failure:

1. read the useful logs and identify the first causal error;
2. classify it as diff-caused, pre-existing, or environmental;
3. use one read-only investigator only when direct evidence is insufficient;
4. fix in scope, run the matching local check, commit, push, and rebuild the inventory.

Use the completion and reassessment policy in `readiness-gates.md` when corrections repeat. Never bypass a check or report success with pending/stale/partial latest-SHA CI.

## G. Freshness, Learning, Handoff

When CI is green, ensure local checks and review evidence still match the latest source. Then run `capture-learning-tools` report-only. It may propose up to three recurring improvements but cannot mutate or reopen the loop without a concrete readiness violation.

The final handoff includes PR URL, latest SHA, branch/base, exact verification, review gate, latest-SHA CI inventory, residual risk, human decisions, and artifacts. “Ready to merge” means every hard gate passed on the same commit.
