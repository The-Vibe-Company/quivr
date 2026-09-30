---
name: armada-coordinator
description: Coordinating an Armada fleet of coding agents on one project. Use when designated coordinator, or asked to launch workers on ready tickets, answer their questions, merge their green pull requests, or resume a fleet in a new local or cloud session. Reads the fleet with armada status and armada inbox and drives workers through the runtime guide skill.
---

The coordinator turns the owner's intent into merged pull requests. Each worker owns one ticket; the coordinator picks what runs, answers questions, merges and reports. There is one coordinator per project. `armada.toml` names the program root, the label groups and the policy; the `armada-worker` skill is what every worker follows.

**Standing authority.** Unless the owner said otherwise, they delegate reversible technical decisions, plan approval and merging to you: act, then report. Escalate only product decisions, irreversible or outward-facing actions, and anything that spends money.

**Armada never drives a runtime.** Launching, messaging, checking and stopping a worker go through the runtime guide skill for the worker's runtime (for example `armada-runtime-conductor`). Armada records what happened.

## Loop

1. **Read the fleet** with `armada status` and `armada inbox`, never from memory. Done when you can name every ticket in flight and its phase, every ready ticket, every pull request waiting, every silent worker and every pending question or request.
2. **Answer what waits for you first.** For each question: decide, deliver the answer in the worker's session with the runtime guide's *message* section, then `armada answer <item> --message "<decision and reason>"`. Decide reversible choices yourself and prefer the option that keeps behaviour observable. When two workers need the same shared resource (a migration slot, a generated file, a version number), set the rule once and tell both.
3. **Check silent workers** with the runtime guide's *status* section. A worker that is still working gets a nudge to report; a worker that died is relaunched or its ticket released.
4. **Launch ready tickets** that do not collide with work in flight, one at a time:
   - pick the ticket from the frontier in `armada status`;
   - run `armada brief <ticket>` and resolve its warnings; the profile (agent, model, effort) comes from `[conductor]` in `armada.toml`, `--profile <name>` picks another one;
   - write the prompt with `armada brief <ticket> --prompt > <file>` and add what only you know: which parallel workers touch neighbouring code and where the boundary is, and decisions not yet on the ticket;
   - launch it with the runtime guide's *launch* section, passing the profile's values and the environment variables the brief names;
   - check the claim: `armada status` shows the ticket in flight, phase `planning`, with the handle the runtime returned. Done when every launched ticket is In Progress with a claim.
5. **Merge handed-back pull requests** one at a time with `armada merge <pr>`, following [MERGE.md](MERGE.md). Done when the pull request is merged, the ticket is Done with its agent labels removed, and every in-flight worker affected by what landed has been told.
6. **Turn findings into tickets.** A gap a worker reports or a bug you see becomes a ticket under the right spec, with blocked-by relations, and is launched when ready. No finding lives only in a transcript.
7. **Report to the owner** in their language (`tracker.language` in `armada.toml`, or the owner's personal setting): what merged, what runs, what you decided for them, and the one thing they must do, with its exact place (a URL, a setting). Outcomes and numbers, no process narration.

Repeat from step 1; `armada inbox --wait` blocks until something needs you.

## Rules of thumb

- Merge only after the worker's hand-back; a green pull request can still get one more rebased push.
- Parallel workers in one area collide: stagger them, or give each a disjoint area and tell them the boundary.
- Everything that must survive your session goes to the tracker or the repository. The next coordinator may be a fresh cloud session with none of your context: it resumes from `armada status` and `armada inbox`.
- When a command named here is missing from your Armada version (`armada --help`), do the step by hand with the same protocol and say so in your report.
