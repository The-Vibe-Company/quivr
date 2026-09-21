# Ship PR Delivery Loop

The coordinator owns scope, architecture, writes to Git, PR state, and final acceptance. Workers receive bounded work orders from `agent-routing.md`.

## A. Preflight

1. Resolve the repository, branch, PR target, and `<base>...HEAD` comparison.
2. Capture status and separate in-scope, supporting, scope-drift, and unrelated dirty files.
3. Read repository guidance and referenced issue/spec/PR when accessible.
4. Classify impacted surfaces and the trivial/standard/deep budget tier.
5. Freeze goal, non-goals, checks, and worker limits in `ship-state.json` and `agent-budget.json`.

Stop if unrelated work cannot be separated safely. Never stash or discard it without approval.

## B. Implementation

Use the coordinator for trivial work and decisions that change scope or architecture. Use one write worker for standard/deep work only when owned files and acceptance checks are stable.

The worker may edit only its owned scope and run explicitly allowed targeted checks. It never performs Git or remote actions. The coordinator inspects the resulting diff, resolves assumptions, and integrates it.

Resume the same worker for a follow-up. After two failed attempts on one root cause, take over or stop. Never run simultaneous writers.

## C. Verification

Discover checks from package scripts, task runners, CI, and docs. Run quick static checks, lint, typecheck, targeted tests, broader tests, build, schema/migration checks, then UI smoke/visual checks as relevant.

Record exact commands and skipped checks. Rerun affected checks after any source change. Deterministic checks are coordinator work, not delegation work.

## D. Single Review Gate

Run `review-code-dev` after the branch is coherent:

- quick for trivial low-risk changes;
- standard for normal changes;
- deep for auth, billing, permissions, migrations, public APIs, broad frontend paths, cross-module architecture, or release-critical work.

Pass required lenses into that run. Frontend is a lens inside the same review, not a separate full pass. Ship PR does not launch a parallel review board.

Fix confirmed P0/P1/P2 findings. Prefer the same write worker for bounded follow-ups. Rerun affected verification and only the failed review lens. Run a second full review only after material scope/architecture changes; cap it at two full runs.

## E. Commit And PR

Recheck status, stage intentional paths, inspect the staged diff, and use commitzen commits. Push normally. Create or update a PR only after local gates pass unless the user explicitly requests a visible draft with blockers.

PR titles are commitzen. The body must distinguish verified facts, skipped checks, residual risk, and human decisions.

## F. CI To Green

For the latest pushed SHA, inventory all visible required/optional checks, statuses, suites, and workflow runs. Use deterministic provider/CLI waiting while work is queued.

For a failure:

1. read the useful logs and identify the first causal error;
2. classify it as diff-caused, pre-existing, or environmental;
3. use one read-only investigator only when direct evidence is insufficient;
4. fix in scope, run the matching local check, commit, push, and rebuild the inventory.

Stop after three distinct corrections for a single check or two repeated fixes for one root cause. Never bypass a check or report success with pending/stale/partial latest-SHA CI.

## G. Freshness, Learning, Handoff

When CI is green, ensure local checks and review evidence still match the latest source. Then run `capture-learning-tools` report-only. It may propose up to three recurring improvements but cannot mutate or reopen the loop without a concrete readiness violation.

The final handoff includes PR URL, latest SHA, branch/base, exact verification, review gate, latest-SHA CI inventory, residual risk, human decisions, and artifacts. “Ready to merge” means every hard gate passed on the same commit.
