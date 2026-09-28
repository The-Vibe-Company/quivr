# Agent fleet workflow

Several coding agents may work on Quivr V2 at the same time. **Linear is the fleet's database:** claims, phases and hand-offs are written to and read from Linear only, never from local files, session memory or worktrees. The canonical protocol is the Linear document [Registre de la flotte d'agents — protocole](https://linear.app/thevibecompany/document/registre-de-la-flotte-dagents-protocole-fffbdd359a1d); this file mirrors it for agents working from the repository. Each agent takes exactly one of two roles.

## Roles

- **Worker.** Plans, implements and ships one ticket as a green pull request. A worker never merges, never enables auto-merge and never approves its own plan.
- **Coordinator.** Dispatches tickets to workers, approves plans, merges green pull requests, closes tickets and tells the remaining workers what changed on `main`. There is one coordinator at a time. A human, or an agent the human has explicitly designated as coordinator in its session, fills this role. Assume you are a worker unless you were told otherwise.

## Worker lifecycle

1. **Pick a ticket.** Take a ticket only if the coordinator assigned it to you, or if it is on the frontier: under `THE-531`, labelled `ready-for-agent`, not started, and every blocked-by ticket is Done. Prefer the coordinator's order when one was given.
2. **Claim it before any work.** Re-read the ticket in Linear. If it is already In Progress or assigned to someone else, stop and pick another. Otherwise assign it, move it to In Progress, set the labels **Agent phase = `planning`** and **Agent runtime = your tool** (`Claude Code`, `Codex` or `Conductor`), and post a first comment `Agent claim — runtime: <tool> · session: <session name or id> · branche: feature/the-<n>-… · démarré: <ISO date>`, then `Agent status: planning — <what you are about to plan>`. Re-read the ticket afterwards: if another claim appeared meanwhile, the older claim wins and you withdraw.
3. **Branch.** Create the Linear-suggested branch (`feature/the-<number>-…`) from the latest `origin/main`, in your own worktree.
4. **Plan.** Run the `plan-pr` skill for the ticket. Put the complete plan in the Linear comment, starting with `Agent status: awaiting-approval — …` (set Agent phase = `awaiting-approval`), because local plan files under the gitignored `plans/` can disappear with a worktree. Push your branch before you stop: a worktree with no changes can be removed while you wait, so recreate it from the branch when you resume. Then stop and wait for the coordinator's approval. Do not implement before it arrives.
5. **Implement.** After approval, set Agent phase = `implementing`, post `Agent status: implementing — …` and build the approved plan test-first. A material change to the approved plan needs a new approval.
6. **Ship.** Run the `ship-pr-dev` skill: it verifies, reviews, opens the pull request and drives CI to green. Set Agent phase = `shipping`, post `Agent status: shipping — …`, and link the pull request on the ticket. If the pull request ships a user-visible capability, update the README's "What works today" / "What comes next" lists and `docs/api-walkthrough.md` in the same pull request.
7. **Rebase before hand-back.** Rebase on the latest `origin/main`, resolve conflicts, restamp your migration if `make verify` reports it ordered before `main`, and get CI green on the final head.
8. **Hand back.** Set Agent phase = `ready-to-merge` and post `Agent status: ready-to-merge — PR #<n>, head <sha>, CI green` with the iteration record required by `AGENTS.md`. Leave the ticket In Progress; the coordinator closes it.

If you stop without finishing, remove your Agent phase and Agent runtime labels, move the ticket back to Todo or Backlog, and post `Agent status: released — <reason>` so the ticket is free again.

If you are blocked, set Agent phase = `blocked`, post `Agent status: blocked — <reason and what would unblock it>` and stop.

### Declaring your state

The **Linear labels are the source of truth** for where an agent is; the control tower reads them.

| Label group (single-select) | Values |
| --- | --- |
| Agent phase | `planning`, `awaiting-approval`, `implementing`, `shipping`, `blocked`, `ready-to-merge` |
| Agent runtime | `Claude Code`, `Codex`, `Conductor` |

Keep exactly one label of each group on your ticket and change the phase label at every transition. Each transition also gets a comment starting with `Agent status: <phase> — <one-line summary>`: the label says where you are, the comment says why and keeps the history.

## Coordinator duties

- **Dispatch** frontier tickets in parallel waves, following Linear blocked-by relations. The dashboard's control tower (`/tour`) ranks the frontier by critical path and flags collisions.
- **Approve plans** by replying to the worker. Settle product decisions with the human; settle technical choices yourself and record them on the ticket.
- **Merge** only when all of the following hold:
  - the pull request is not a draft and its title follows Commitizen conventions;
  - every check on the current head is green and GitHub reports it mergeable;
  - the worker has handed back.

  Squash-merge pinned to the verified head, for example `gh pr merge <n> --squash --match-head-commit <sha>`. Merge one pull request at a time.
- **After each merge:**
  - move the ticket to Done and remove its Agent phase label and make sure the pull request is linked;
  - delete the branch and the worker's worktree;
  - tell every in-flight worker that `main` moved, including any migration number or shared file that changed;
  - dispatch the tickets the merge unblocked.

## Collision rules

- **Migrations.** Name new migrations `migrations/<UTC YYYYMMDDTHHMMZ>_<slug>.sql` (`make migration name=<slug>`). Never add a numbered `0xx_` file; that set is closed. Readiness derives from the embedded set, so a migration touches no other file. `make verify` fails if your new migration sorts before the latest migration on `origin/main`; if that happens at your final rebase, rename your file to the current UTC time with the `git mv` command it prints (or `make migration-restamp file=<name>.sql`). Nothing else changes.
- **Shared files** such as `internal/transport/httpapi/api.go`, `contracts/http/v0/openapi.yaml`, generated transport code and `scripts/local.py`: keep edits small and localized, and regenerate generated code instead of hand-merging it.
- **Acceptance tests** are order- and load-sensitive. Give new acceptance tests their own Organization or Corpus, and schedule ingestion-heavy tests after the timed scenarios in `scripts/local.py`.

## Observing the fleet

The dashboard at https://quivr-v2-dashboard.vercel.app is for humans; it is private, and agents do not need it. It reads the same Linear and GitHub signals that workers write. If a worker skips the phase label, the status line, the branch name, the assignment or the pull-request link, its phase can only be guessed. Linear is the source of truth for agents.
