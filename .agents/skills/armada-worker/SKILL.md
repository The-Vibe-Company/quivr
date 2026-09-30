---
name: armada-worker
description: Working one ticket as an Armada worker agent. Use when you were launched on a ticket by an Armada coordinator, or asked to claim, plan, implement and ship one ticket of a project that has an armada.toml. Covers the claim, the phase reports every 15 minutes, questions to the coordinator, the green pull request and the hand-back. A worker never merges.
---

You are a worker. You own exactly one ticket, named in your launch brief. You turn it into one green pull request and hand it back to the coordinator, who merges. `armada.toml` at the repository root names the tracker program, the label groups and the policy; `AGENTS.md` (or `CLAUDE.md`) holds the repository's own rules, and they win over this skill where they are stricter.

## Rules

- **Never merge**, never enable auto-merge, never approve your own plan. The coordinator merges.
- **Report at least every 15 minutes** of work with `armada report <phase> --message "<what you are doing>"`. The same phase is allowed: it is your heartbeat. A worker silent for longer than `policy.silence_minutes` shows as silent, and the coordinator comes to check on you.
- **Ask instead of guessing** when a choice changes the ticket's outcome: `armada ask "<question, the options, your recommendation>"`. Your phase becomes `blocked` until the coordinator answers in your session. Decide reversible technical choices yourself and record each one, with its reason, on the ticket and in the pull request.
- **The ticket is the source of truth.** Read it, its parent spec and the hand-back comments of its merged blockers in full before planning.
- Work only in your own branch and worktree. Never touch another agent's checkout, and stop only the processes you started.
- Never print, commit or log a secret.

## Steps

1. **Claim** with the exact command your brief starts with, for example `armada claim <ticket> --runtime conductor --handle "$CONDUCTOR_WORKSPACE_ID/$CONDUCTOR_SESSION_ID"`. Install the Armada version the brief names first; if it gives a fallback command, use it wherever this skill says `armada`. It assigns the ticket, moves it to In Progress, sets `Agent phase = planning` and your `Agent runtime`, and posts the `Agent claim` comment. If it refuses because another claim exists, stop: the older claim wins.
2. **Branch.** Use the branch named after the ticket (the tracker suggests one, for example `feature/abc-12-short-title`), created from the latest default branch.
3. **Plan.** Write the plan: what changes, how it is tested, and the checks a person could observe when it is done. If your brief says plans are pre-approved, post it with `armada report implementing --message "<plan>"` and go on. Otherwise post it with `armada report awaiting-approval --message "<plan>"`, push your branch, and wait for the coordinator's answer in your session.
4. **Implement** test-first. A material change to the approved plan needs a new approval.
5. **Ship.** `armada report shipping --pr <number>`. Use the `ship-pr-dev` skill when the repository has it; otherwise run the repository's local checks, open the pull request with a Commitizen title (`feat(scope): …`), link it on the ticket, and fix CI until every required check is green.
6. **Rebase** on the latest default branch right before hand-back, and get CI green on that final head.
7. **Hand back.** `armada report ready-to-merge --pr <number> --sha <full 40-character head SHA>`. Armada refuses a short SHA, a SHA that is not the pull request head, or a red check. Then report to the coordinator: pull request URL, full SHA, CI state, the decisions you made, and what the next tickets need to know.

If you stop without finishing, run `armada release --reason "<why>"` so the ticket is free again.

## When a command is missing

This skill is versioned with Armada; an older CLI may lack a command (`armada --help` lists them). Do the same step by hand in the tracker, keeping the protocol: exactly one label from the phase group (`planning`, `awaiting-approval`, `implementing`, `shipping`, `blocked`, `ready-to-merge`) and one from the runtime group; a first comment `Agent claim — runtime: <runtime> · session: <id> · branch: <branch> · started: <ISO date>`; and every comment starting with `Agent status: <phase> — <one-line summary>`.
