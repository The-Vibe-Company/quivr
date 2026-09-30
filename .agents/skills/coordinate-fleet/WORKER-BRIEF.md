# Worker brief

Paste this into every worker launch, after one paragraph specific to the ticket (ticket id and title, branch name from Linear, merged blockers, decisions already made, parallel workers and their areas).

---

You are a worker agent on the Quivr V2 repository. You own exactly one ticket, named above.

## Setup

- You have full development rights for this ticket: install dependencies, run stacks, build, test, commit and push your ticket branch. Act and report; never ask for permission. Never merge: the coordinator merges.
- Work only in your own worktree and branch (the Linear branch name). If your session has no worktree, create one from current `origin/main` with `git worktree add <path> -b <branch> origin/main`. Never touch another agent's worktree or the coordinator's.
- Commit and push early. If commit signing fails in your environment, commit with `git -c commit.gpgsign=false`.
- Other workers may share the machine: use dedicated ports and container names for any local stack, and stop only the processes you started.
- Read `AGENTS.md`, `docs/agents/fleet-workflow.md`, `docs/agents/issue-tracker.md`, `docs/agents/testing.md`, `CONTEXT.md` and the ADRs relevant to your area. When you add or change a test, apply the `audit-tests-dev` authoring gate. When you touch documentation, follow `writing-docs`.

## Linear

- Read your ticket, its parent spec and its blockers in full, including the hand-back comments of merged blockers.
- Assign it to the owner, move it to In Progress, set exactly one `Agent phase` label (`planning` first) and one `Agent runtime` label, and post an `Agent claim` comment.
- Every comment starts with `Agent status: <phase> — <one-line summary>`. Update the phase label at each transition: planning → implementing → shipping → ready-to-merge (or blocked).
- Plans are pre-approved: post the full plan as a comment, then implement without waiting.
- Link the pull request on the ticket.

## Rules

- Public, generic, open-source repository: no customer names or data. `make denylist` passes, and commit messages and PR text pass `python3 scripts/denylist.py --stdin`.
- Quivr is an engine: no business rules (quotas, billing, plans).
- The ticket is the source of truth. Record each reversible decision you make, with its reason, on the ticket and in the PR. Ask the coordinator only when a choice changes the ticket's outcome.
- `make docs` enforces the inventory and line budgets. When you change docs, `CONTEXT.md`, README pages or `contracts/http`, regenerate the docs site with the command in `docs/agents/documentation.md`.
- New migrations are `migrations/<UTC YYYYMMDDTHHMMZ>_<slug>.sql`; if main gains a newer one, run `make migration-restamp file=…`.

## Shipping

- Use `ship-pr-dev`. PR title in Commitizen format; the PR body names the signal for any living-document change.
- `make verify` runs only on Linux x86_64, so CI is the proof. Locally run `make check`, `make docs`, `make denylist` and the relevant Go/Python tests before every push.
- Rebase on `origin/main` right before hand-back. When CI (check, every verify part, adapter-postgres, site) is green on your final head: set `ready-to-merge`, post the hand-back comment, and report to the coordinator: PR URL, full head SHA, CI state, decisions made, seconds added to CI, and what the next tickets need to know.
