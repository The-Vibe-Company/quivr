# Repository instructions

- Pull request titles must follow Commitizen conventions, for example `feat(ingestion): accept record versions`.

## Agent skills

### Issue tracker

Specs and tickets are tracked in Linear, in The Vibe Company workspace and under the Quivr V2 investigation `THE-531`. See `docs/agents/issue-tracker.md`.

**Required: record every substantive iteration in Linear.** Before research, prototyping, or implementation, identify or create its ticket and record the objective, intended outcome, and assumptions being tested. After each iteration, append a succinct comment with the research or experiments performed, evidence links, actual results versus the objective, and the decision or next step with its rationale. Include failed attempts and remaining uncertainty when they affect that rationale. Preserve prior comments; an iteration is complete only when its evidence and conclusions are recorded on the ticket.

### Implementation dashboard

Program progress is shown at https://quivr-v2-dashboard.vercel.app (private: Vercel Authentication, The Vibe Company team). It is derived entirely from Linear, so every ticket you create or update must stay readable by it:

- **Everything lives under `THE-531`** in the `Quivr V2` project, reachable through parent links. An issue outside that tree is invisible.
- **A spec is a direct child of `THE-531` titled `Spec N/M — <name>`** (em dash). `N` orders the roadmap; when adding a spec, bump `M` on every spec title. Other direct children of `THE-531` are shown as investigation or decision work.
- **Spec work is a sub-issue of its spec**, at any depth. Implementation slices keep `Implementation slice N/T` in their description.
- **Dependencies use native Linear blocked-by relations**, between sub-issues and between specs. Text in a description is not read.
- **Status is the Linear workflow state**: move a ticket to In Progress when work starts and to Done only when its PR is merged. Use Canceled or Duplicate, never deletion, for dropped work.
- **Link the pull request** on the ticket (GitHub attachment or PR URL) so it appears in the in-flight view.

Dashboard code, data derivation, snapshot refresh and redeploy instructions live in the separate `quivr-v2-dashboard` repository's `AGENTS.md`. Never put Linear or GitHub tokens in either repository.

### Triage labels

The project uses the five canonical triage labels. See `docs/agents/triage-labels.md`.

### Domain docs

The repository uses a single-context domain documentation layout. See `docs/agents/domain.md`.
