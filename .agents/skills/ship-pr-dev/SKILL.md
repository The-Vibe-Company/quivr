---
name: ship-pr-dev
description: "Ship PR: credit-aware autonomous pull request readiness workflow.
  Use when the user asks to ship work, prepare a branch for review, create or
  update a PR, make CI green, clean up a branch before PR, or produce a PR a
  human can confidently merge. This skill may edit code, run checks, commit,
  push, create/update a PR, and iterate on CI, but it keeps Git ownership in the
  coordinator, delegates bounded work to cheaper host-native workers, uses
  review-code-dev as the only independent read-only review gate, waits for all
  visible latest-SHA CI to be green, runs capture-learning-tools report-only,
  and never merges."
metadata: {}
allowed-tools: Bash Read Edit Write Glob Grep Agent
---

# Ship PR

Own delivery from the current branch to a PR that is ready for human review. The coordinator keeps scope, architecture, safety decisions, Git state, PR state, and final verification. Delegate only bounded work orders whose saved context is worth more than the dispatch overhead.

## Protected Invariants

1. Never merge, enable auto-merge, force-push, hard-reset, discard user work, or rewrite public history unless the user explicitly requests that exact operation.
2. Stage only intentional files. Never use broad staging when unrelated changes exist.
3. The coordinator is the only Git owner. Workers never stage, commit, push, rebase, merge, stash, create/update a PR, or alter CI settings.
4. Allow at most one write-capable worker at a time. Reviewers and investigators are read-only.
5. `review-code-dev` is the single independent review gate. Do not run a separate Ship PR review board or a second full frontend review; pass the required risk and frontend lenses into one review run.
6. A PR is ready only when local verification is fresh, `review-code-dev` has no unresolved P0/P1 findings, every visible non-skipped CI item is green on the latest pushed SHA, and the report-only `capture-learning-tools` pass completed.
7. Never bypass checks, use `--no-verify`, weaken validation, or claim success while CI is pending, stale, partially inspected, failed, cancelled, or attached to another SHA.
8. Never expose secrets. Stop push/PR work, redact values, and give rotation guidance if one is found.
9. Write artifacts only under ignored `plans/ship-pr-dev/runs/<timestamp>-<repo-slug>/`.

## References

Read only what the current phase needs:

- `references/workflow.md` — detailed delivery loop and retry policy.
- `references/agent-routing.md` — host-specific worker models, effort, context, budgets, telemetry, and fallbacks.
- `references/readiness-gates.md` — hard blocks and required evidence.
- `references/pr-template.md` — PR body and final handoff.

Load the installed `review-code-dev` skill before the review gate and `capture-learning-tools` only for the final report-only learning pass. Resolve skills by canonical name; never guess an install path.

## Workflow

### 0. Classify The Invocation

| Cue | Mode | Exit target |
| --- | --- | --- |
| ship, create PR, merge-ready | `ship` | PR updated, all visible latest-SHA CI green |
| prepare, cleanup before PR | `prepare` | coherent and verified local branch |
| update PR, fix CI | `update-pr` | existing PR updated and latest-SHA CI green |
| local only, do not push | `local-handoff` | verified local handoff, no remote mutation |

Infer the base from the PR target, `origin/HEAD`, `origin/main`, then local `main`. Compare with `<base>...HEAD`. Stop outside a Git repository.

### 1. Prepare Deterministic Context

```bash
SKILL_DIR="<directory containing this SKILL.md>"
RUN_META="$(mktemp -t ship-pr-dev-run.XXXXXX.json)"
python "$SKILL_DIR/scripts/prepare_ship_run.py" --cwd . > "$RUN_META"
RUN_DIR="$(python -c 'import json,sys; print(json.load(open(sys.argv[1]))["run_dir"])' "$RUN_META")"
python "$SKILL_DIR/scripts/collect_ship_context.py" --cwd . --output "$RUN_DIR/context.json"
```

Create `ship-state.json` with the goal, non-goals, base/branch, changed and unrelated files, impacted surfaces, checks, review status, CI inventory, PR status, retry counts, and blockers. Create `agent-budget.json` and `phase-timing.json` from `references/agent-routing.md` before the first delegation.

### 2. Choose The Smallest Useful Agent Budget

Classify the change before dispatch:

Use `impact.agent_workflow` for paths under agent skill roots. Do not infer application backend or security risk solely from executable or security-named files inside `.agents/skills`, `.claude/skills`, or `.codex/skills`. Treat agent-only documentation/metadata as trivial and agent-only executable workflow changes as standard unless their actual authority or remote effects justify deep review.

| Tier | Typical change | Delegation budget | Local target / reassessment checkpoint (CI excluded) |
| --- | --- | --- | --- |
| trivial | docs, metadata, obvious one-file edit | no implementation worker; quick review, normally inline | 10 / 15 min |
| standard | bounded feature/fix across a few files | at most 1 write worker, 1 primary reviewer, up to 2 focused reviewers total inside `review-code-dev` | 25 / 40 min |
| deep | auth, billing, permissions, migration, public API, broad frontend flow, cross-module architecture | at most 1 write worker, 1 primary reviewer, up to 3 focused reviewers total inside `review-code-dev` | 50 / 90 min |

Do not spend a worker on repository discovery, deterministic checks, Git operations, CI polling, PR text, or a task the coordinator can complete in roughly one tool call. Use at most three concurrent read-only workers. Never launch two workers with the same review angle. At a checkpoint, explain what is consuming time, narrow or resume work when useful, and continue for as long as correctness requires. A time checkpoint is never a blocker and never justifies an incomplete handoff.

### 3. Implement Or Clean Up

Freeze a work order before delegation: objective, owned files, allowed edits, non-goals, acceptance checks, artifact path, and explicit no-Git rule. Use the host routing in `references/agent-routing.md`.

- Trivial: implement in the coordinator.
- Standard/deep: use one write worker only when the change is separable and the work order is stable.
- Review the worker diff before accepting it. The coordinator resolves architectural choices and integrates the result.
- For follow-up fixes, resume the same worker/context when supported. After two failed attempts on the same root cause, the coordinator takes over or stops with evidence.

Keep implementation/fix cycles to three. Preserve unrelated user work.

### 4. Verify Deterministically

Discover formatter, lint, typecheck, tests, build, migration, and UI checks from repository config and CI. Run targeted checks first, then the broadest practical set. Record exact commands and results in `verification.md`. Rerun affected checks after every source change.

Workers may diagnose a non-obvious failure, but the coordinator runs and records the authoritative command. Do not use model turns to poll a process or CI status. Record phase start/end, worker wait, deterministic command time, and CI wait separately so a long provider check is not confused with expensive agent orchestration.

### 5. Run One Independent Review Gate

Run `review-code-dev` once after the branch is coherent and local verification is green enough to review:

- `quick` for trivial low-risk changes;
- `standard` for normal changes;
- `deep` for the deep-risk tier.

Pass repository path, base, user goal, changed-file summary, impacted surfaces, and required lenses. For frontend work, require the `frontend` lens inside this same run. Do not run an earlier frontend mega-pass or a separate Ship PR board.

Fix confirmed P0/P1/P2 findings in the coordinator or with the same bounded write worker. Rerun affected verification. Rerun only the targeted failed review lens when evidence changed; perform a second full review only if the fix materially changed scope or architecture. Cap full review runs at two.

### 6. Commit, Push, PR, And CI

After local gates pass:

1. Recheck status and staged diff.
2. Use a safe branch; when creating one under Codex, prefer `codex/<purpose>` unless repo guidance or the user says otherwise.
3. Commit with a commitzen message, normal hooks, and only intentional files.
4. Push normally and create/update the PR with a commitzen title.
5. Inventory required checks, optional checks, workflow runs, commit statuses, and check suites for the latest pushed SHA.
6. Use deterministic provider/CLI waiting for queued work. Do not repeatedly ask an agent whether CI is done.
7. For a failure, inspect logs and identify the first causal error. Use one read-only investigator only when the cause is not apparent. Fix, verify locally, commit, push, rebuild the inventory, and resume.

Stop after three distinct corrections for one CI check or two repeated fixes for the same root cause. Never hand off success while a visible non-skipped item is not final and green.

### 7. Learning Pass And Handoff

After green CI, an explicit blocker, or a user-requested local-only stop, run `capture-learning-tools` in report-only mode. Give it the goal, corrections, verification, review/CI evidence, retry history, and artifact paths. It returns `NONE` or up to three recurring process improvements. It must not edit, stage, commit, push, alter CI, or reopen the ship loop without a concrete readiness violation.

Return the PR URL or blocker, latest SHA, branch/base, verification, review gate, latest-SHA CI state, remaining human decisions, and `RUN_DIR`. Say “ready to merge” only when every protected gate passed on the same commit.

## Response Shape

```markdown
PR ready for human review: <url>

Branch: <branch> -> <base>
Verification: <commands passed>
Review gate: review-code-dev <passed / findings fixed>
CI: <all visible non-skipped items green on latest SHA>
Artifacts: <RUN_DIR>
```

If blocked, state the exact blocker, verified evidence, and smallest next action. Do not soften a blocked state into a success claim.
