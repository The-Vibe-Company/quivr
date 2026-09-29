# Repository instructions

- Pull request titles must follow Commitizen conventions, for example `feat(ingestion): accept record versions`.
- **This is a generic, open-source repository.** Never add customer-specific content: customer or project names, their sources, mailboxes, accounts, volumes, configurations, meeting notes or quotes from private tickets. That applies to code, tests, fixtures, docs, comments, commit messages and pull-request text. Write features and docs for any organization, with neutral examples, and document how to configure them instead of configuring them for someone. Customer context stays in Linear and in the customer's own deployment. Before handing back, `make denylist` must pass (it also runs first in `make verify` and CI), and commit messages and pull-request text must pass `python3 scripts/denylist.py --stdin`. The denylist stores salted hashes only: add a new customer term with `python3 scripts/denylist.py --hash "term"`, never in plain text.
- **Several agents may work in parallel, and Linear is their shared database.** Who works on what, and where each agent is, lives only in Linear (assignee, `Agent phase` and `Agent runtime` labels, `Agent claim` and `Agent status` comments). The canonical protocol is the Linear document [Registre de la flotte d'agents — protocole](https://linear.app/thevibecompany/document/registre-de-la-flotte-dagents-protocole-fffbdd359a1d) in the Quivr V2 project; `docs/agents/fleet-workflow.md` mirrors it. Read one of them before taking a ticket. Unless you were explicitly designated coordinator, you are a worker: claim one ticket, get your plan approved, ship a green pull request, and **never merge**. Only the coordinator merges.

## Agent skills

### Issue tracker

Specs and tickets are tracked in Linear, in The Vibe Company workspace and under the Quivr V2 investigation `THE-531`. See `docs/agents/issue-tracker.md`.

**Required: record every substantive iteration in Linear.** Before research, prototyping, or implementation, identify or create its ticket and record the objective, intended outcome, and assumptions being tested. After each iteration, append a succinct comment with the research or experiments performed, evidence links, actual results versus the objective, and the decision or next step with its rationale. Include failed attempts and remaining uncertainty when they affect that rationale. Preserve prior comments; an iteration is complete only when its evidence and conclusions are recorded on the ticket.

### Implementation dashboard

Program progress is shown at https://quivr-v2-dashboard.vercel.app (private: Vercel Authentication, The Vibe Company team). It is derived entirely from Linear, so every ticket you create or update must stay readable by it:

- **Everything lives under `THE-531`** in the `Quivr V2` project, reachable through parent links. An issue outside that tree is invisible.
- **A spec is a direct child of `THE-531` titled `Spec N/M — <name>`** (em dash). `N` orders the roadmap; when adding a spec, bump `M` on every spec title. Other direct children of `THE-531` are shown as investigation or decision work.
- **Spec work is a sub-issue of its spec**, at any depth. Implementation slices keep `Implementation slice N/T` in their description.
- **Titles use plain words that someone outside the code understands.** A spec's `<name>` or a ticket title states what a user, operator or developer can do once it ships, for example `Search images, audio and video` or `Fix: going back to an earlier text of a document is ignored`. Start tickets with a verb. Keep titles short, under about 60 characters after the spec prefix. Leave out internal type, file and function names (`Contribution`, `Projection Generation`, `pluginhttp`); they belong in the description. Pull-request titles keep the Commitizen format.
- **Every spec and ticket description opens with an `## In short` section** that someone outside the code can read in 30 seconds. It says what changes, why, when the work is done in checks a person could observe, and what it depends on. Technical detail comes after a `---` separator. The exact template is in `docs/agents/issue-tracker.md`, and it overrides any template bundled in a skill such as `/to-tickets` or `/to-spec`.
- **Dependencies use native Linear blocked-by relations**, between sub-issues and between specs. Text in a description is not read.
- **Status is the Linear workflow state**: move a ticket to In Progress when work starts and to Done only when its PR is merged. Use Canceled or Duplicate, never deletion, for dropped work.
- **Link the pull request** on the ticket (GitHub attachment or PR URL) so it appears in the in-flight view.
- **Assign the ticket and work on a branch named after it** (`feature/the-<number>-…`, as Linear suggests) so the control tower can match branches, PRs and CI to the ticket.
- **Declare your state with Linear labels**: exactly one **Agent phase** (`planning`, `awaiting-approval`, `implementing`, `shipping`, `blocked`, `ready-to-merge`) and one **Agent runtime** (`Claude Code`, `Codex`, `Conductor`), updated at every transition.
- **Start every Linear comment you post with a status line**: `Agent status: <phase> — <one-line summary>`, using the same phase names. The rest of the comment keeps the iteration record required above.

Dashboard code, data derivation, snapshot refresh and redeploy instructions live in the separate `quivr-v2-dashboard` repository's `AGENTS.md`. Never put Linear or GitHub tokens in either repository.

### Triage labels

The project uses the five canonical triage labels. See `docs/agents/triage-labels.md`.

### Domain docs

The repository uses a single-context domain documentation layout. See `docs/agents/domain.md`.
