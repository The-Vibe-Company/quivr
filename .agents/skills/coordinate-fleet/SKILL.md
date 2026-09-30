---
name: coordinate-fleet
description: Coordinating the Quivr V2 agent fleet. Use when designated coordinator, or asked to launch workers on tickets, merge green pull requests, keep Linear and the demo in sync, or resume the fleet in a new (local or cloud) session.
---

The coordinator turns the owner's intent into merged pull requests. Workers each own one ticket; the coordinator picks what runs, answers their decisions, merges, deploys and reports. `AGENTS.md` and `docs/agents/fleet-workflow.md` define the protocol; this skill is the coordinator's practice on top of it.

**Standing authority.** The owner delegates reversible technical decisions, plan approval and merging to the coordinator, and does not want permission prompts: act, then report. Escalate only product decisions, irreversible or outward-facing actions, and anything that spends money.

## Loop

1. **Read the fleet state from Linear**, never from memory: the specs under `THE-531`, their sub-issues, state, `Agent phase` / `Agent runtime` labels, blocked-by relations, and the latest `Agent status:` comments. List open pull requests with `gh pr list`. Done when you can name every ticket in flight, every ticket ready to start (all blockers Done) and every pull request waiting for you.

2. **Launch one worker per ready ticket** that does not collide with work in flight. Brief each with [WORKER-BRIEF.md](WORKER-BRIEF.md) plus a ticket paragraph: the merged blockers and their hand-back notes, the decisions you already made for it, and which parallel workers touch neighbouring code. Run workers in the background with worktree isolation, on the strongest available model. Done when every launched ticket is In Progress with a claim comment.

3. **Answer worker questions the same turn.** Decide reversible choices yourself, pick the option that keeps behaviour observable and the engine generic, and record the decision and its reason on the ticket. When two workers need the same shared resource (Plugin API minor version, migration timestamp, generated files), set the rule once and tell both.

4. **Merge a ready pull request** with [MERGE.md](MERGE.md). Done when the PR is merged, the ticket is Done with its agent labels removed, and every in-flight worker affected by what landed has been told.

5. **Check what the merge changed in production-like environments**: the Railway demo and the docs site deploy from `main`. Follow [OPERATIONS.md](OPERATIONS.md). Done when the deploy succeeded and a smoke check passed, or a follow-up ticket exists for what failed.

6. **Turn findings into tickets.** A gap a worker reports, a bug seen in the demo, an alias to remove later: create the ticket under the right spec with an `## In short` section (template in `docs/agents/issue-tracker.md`), blocked-by relations, and launch it if it is ready. Done when no finding lives only in a chat transcript.

7. **Report to the owner** in their language, briefly: what merged, what is running, what you decided for them, and the one thing they must do (if any), with its exact place (a URL, a setting). Numbers and outcomes, no process narration.

## Rules of thumb

- Merge only after the worker's hand-back; a green PR can still get one more rebased push.
- Parallel workers on one ticket area collide: stagger them, or give each a disjoint area and tell them the boundary.
- Every finding that survives a session goes to Linear or to the repository, never only to a local note: the next coordinator may be a cloud session with none of your context.
- When the owner changes a process rule, update this skill in the same session.
