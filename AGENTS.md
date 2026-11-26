# Repository instructions

- Pull request titles must follow Commitizen conventions, for example `feat(ingestion): accept record versions`.
- **This is a generic, open-source repository.** Never add customer-specific content: customer or project names, their sources, mailboxes, accounts, volumes, configurations, meeting notes or quotes from private tickets. That applies to code, tests, fixtures, docs, comments, commit messages and pull-request text. Write features and docs for any organization, with neutral examples, and document how to configure them instead of configuring them for someone. Customer context stays in Linear and in the customer's own deployment. Before handing back, `make denylist` must pass (it also runs at the start of `make verify` and CI), and commit messages and pull-request text must pass `python3 scripts/denylist.py --stdin`. The denylist stores salted hashes only: add a new customer term with `python3 scripts/denylist.py --hash "term"`, never in plain text.
- **Quivr is an engine, not a product.** Never add business rules to the core: per-user or per-organization limits and quotas, billing, pricing, plans, entitlements, or any "how much a customer may use" logic. Those live in an API or product layer above Quivr. The engine exposes generic primitives that layer can build on, such as opaque owner references, listing, counts and events through the public API. If a spec or ticket asks for a business rule in the core, move it out of scope and say so on the ticket.
- **Tests must earn their place.** Before adding or changing a test, follow `docs/agents/testing.md`: one owner test per contract at the cheapest boundary that sees it, no wall-clock waits, a time budget per level, and a ticket with a root cause for every flaky test instead of a retry. When consolidating configuration tests, give the keeper literal external keys and an omitted-key case; a fixture serialized and decoded with the same type cannot protect key compatibility.
- **Change a living document only for a signal.** A page declared in `docs/inventory.toml` changes only because the same pull request changes the behaviour it documents, a bug or recurring agent error was traced to a gap in it, a review comment asked for it, or a user question showed it was missing or wrong. Never rewrite or polish documentation without one, and name the signal in the pull-request description. `AGENTS.md`, `CONTEXT.md` and guides have line budgets that `make docs` enforces; see `docs/agents/documentation.md`.
- **Past decisions are frozen.** Never edit or delete an accepted ADR in `docs/adr/` or a dated document in `docs/dated/`; supersede it with a new one that links to it. `make docs` compares them with where the branch forked from `origin/main`. See [ADR 0004](docs/adr/0004-documentation-rules-are-enforced-by-ci-only.md).
- **Several agents may work in parallel, and OpenBoard is the mandatory task board for active claims, phase state, and pull requests.** Every PR (worker or coordinator) MUST have an OpenBoard task in `quivr` (`https://openboard.thevibecompany.co/api/tvc/main`, `OpenBoard #<id>` in the PR body, never `Closes #<id>`): self-claim with `ob tasks claim <id> --lease 3600`, renew with `--lease 3600`, post progress and the PR URL with `ob reply`, and **never** call `ob tasks complete` before merge — instead label `pr=<N> ci=green cubic=0` and run `ob tasks block <id> "PR #<N> ready for review & merge — <URL>" --kind pr_review`; after merge the merger runs `ob tasks unblock <id> && ob tasks claim <id> && ob tasks complete <id> "Merged PR #<N>"`. Linear remains for roadmap specs and the program dashboard (`THE-531`). See `docs/agents/fleet-workflow.md` and `.agents/skills/openboard/SKILL.md`. Keep tokens and secrets out of tracked files. Unless explicitly designated coordinator, you are a worker and **never merge**.

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
