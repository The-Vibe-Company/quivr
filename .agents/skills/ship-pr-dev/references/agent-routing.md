# Credit-Aware Agent Routing

Use host-native workers for bounded work. The coordinator keeps product judgment, architecture, safety, Git, PR state, CI state, and final acceptance.

## Host Matrix

| Host | Worker model | Effort | Context rule |
| --- | --- | --- | --- |
| Codex | `gpt-5.6-luna` | `max` | use a fresh self-contained work order; if `fork_turns` exists, set it to `none` rather than inheriting full history |
| Claude Code | `claude-sonnet-5` | request `high` when supported; reserve `max` for deep risk or an unresolved P0/P1 | send the work order and paths, not the whole conversation |
| OpenCode | no override | no override | inherit the user's default model |
| Unknown host | no override | no override | use the first safe native adapter or work inline |

An override is a request, not proof. If the adapter rejects it, retry once with the host default and record `effective_model: unknown`. Never claim a credit saving from requested routing alone.

Do not automatically invoke another installed agent CLI for “model diversity.” Use a second model family only when the user explicitly requests it or one ambiguous high-risk finding needs independent validation. A second full review is not a default budget item.

## Dispatch Test

Delegate only when all are true:

1. the task is bounded and separable;
2. inputs, owned files, non-goals, and acceptance checks can be frozen;
3. saved coordinator context is greater than dispatch/synthesis overhead;
4. the worker has the needed tools without broadening authority;
5. a retry or takeover path is defined.

Stay in the coordinator for one-tool-call edits, deterministic discovery, checks, Git/PR operations, CI polling, and final synthesis.

## Worker Types

### Write worker

- At most one active writer.
- Owns only listed files or a sharply bounded behavior.
- May edit and run targeted checks when allowed by the work order.
- Never performs Git or remote mutations.
- Returns changed files, checks, assumptions, and remaining risks.

### Primary reviewer

- Read-only and isolated.
- Owned by `review-code-dev`, including frontend and specialist routing.
- Verifies specialist candidates before publishing findings.

### Focused reviewer or CI investigator

- One independent question and bounded evidence.
- Read-only, no nested agents.
- Used only when the coordinator/primary cannot answer efficiently from direct evidence.

### Learning worker

- Runs `capture-learning-tools` report-only at handoff.
- Returns `NONE` or up to three recurring process improvements.
- Cannot mutate the repository or reopen the loop without a concrete gate violation.

## Retry And Concurrency

- Resume the same worker for follow-up when supported.
- After two failed attempts on one root cause, the coordinator takes over or stops.
- Run at most three read-only workers concurrently.
- Never run two workers with the same angle.
- Never use model turns as process or CI pollers; use deterministic wait commands.

## Time Targets And Reassessment Checkpoints

Time limits cover local orchestration and model work, not healthy external CI waiting:

| Tier | Local target | Reassessment checkpoint | Write worker checkpoint | Primary review checkpoint | Focused/CI checkpoint | Learning checkpoint |
| --- | --- | --- | --- | --- | --- | --- |
| trivial | 10 min | 15 min | none | inline/5 min | none | 3 min |
| standard | 25 min | 40 min | 15 min | 12 min | 8 min | 5 min |
| deep | 50 min | 90 min | 25 min | 20 min | 12 min | 5 min |

When a worker reaches its checkpoint, inspect progress instead of cancelling by default. Resume with a narrower request, extend the same worker, or let the coordinator take over according to the evidence. Time alone never creates a blocker, forbids a new worker, or permits partial readiness; continue when additional work is genuinely necessary.

External CI time is tracked separately. Wait with a provider/CLI watch process that consumes no model turns. If a check stops changing state beyond the repository's normal duration, inspect it once and classify it as queued, stuck, unavailable, or failed; do not burn agents by repeatedly re-reading the same status.

Write `phase-timing.json`:

```json
{
  "tier": "trivial",
  "local_target_seconds": 600,
  "reassessment_checkpoint_seconds": 900,
  "phases": [{"name": "verification", "started_at": null, "ended_at": null, "duration_seconds": null}],
  "external_ci_wait_seconds": 0,
  "checkpoint_crossed": false,
  "continuation_reason": null
}
```

## Work Order

```text
Role: <write-worker | primary-reviewer | focused-reviewer | ci-investigator | learning-worker>
Objective: <one bounded outcome>
Repository: <absolute path>
Inputs: <files/artifacts>
Owned scope: <files or question>
Allowed actions: <explicit>
Forbidden actions: <Git, remote mutation, out-of-scope edits, nested agents as applicable>
Acceptance checks: <observable>
Output: <artifact path and schema>
```

## Budget Ledger

Write `agent-budget.json` before dispatch and update it after each worker:

```json
{
  "tier": "standard",
  "limits": {"write_workers": 1, "primary_reviewers": 1, "focused_reviewers": 2, "max_concurrent_read_only": 3},
  "workers": [
    {"role": "write-worker", "adapter": "subagent", "requested_model": "gpt-5.6-luna", "requested_effort": "max", "effective_model": null, "effective_effort": null, "resumed": false, "duration_seconds": null, "tokens": null, "outcome": "pending"}
  ]
}
```

Use `null` for unavailable usage. The useful optimization signal is the number of worker starts, repeated contexts, failed retries, wall time, and outcome—not invented token counts.
