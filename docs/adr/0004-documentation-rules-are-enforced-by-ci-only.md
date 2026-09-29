# Documentation rules are enforced by CI only, and accepted decisions are superseded, never edited

Date: 2026-09-29

Status: accepted

Quivr's documentation rules are enforced by deterministic checks in `make docs`, the first step of `make verify`, and by nothing else. There is no CODEOWNERS file and no human approval gate on documentation. Several coding agents work on the repository in parallel and an agent coordinator merges green pull requests, so a rule that only a human reviewer enforces would not be enforced. The rules and their rationale come from [Spec 11/12, THE-693](https://linear.app/thevibecompany/issue/THE-693); this record is its slice [THE-703](https://linear.app/thevibecompany/issue/THE-703).

Documentation is either living or dated. Living pages describe current behaviour, are declared in `docs/inventory.toml` and change in the pull request that changes the behaviour. Dated documents record a decision, a measurement or a piece of research at a date: ADRs in `docs/adr/`, and design records, evidence and research in `docs/dated/`. Each carries a `Status:` line, and every dated document added from now on also carries a `Date:` line; ADRs 0001 to 0003 predate the rule and keep their text.

An accepted ADR or a dated document is never edited or deleted. To change a decision, add a new ADR (or dated document) that says which one it supersedes and why; the old one stays as it was. `make docs` compares every dated document with the commit where the branch forked from `origin/main` (their merge base), and fails when one differs or is missing. The only exception is a document whose status at that fork point is `proposed`, which may still change until it is accepted. A new dated document must have its `Date:` and `Status:` lines.

## Considered Options

- **CODEOWNERS on `docs/` and `AGENTS.md`.** A human must then approve every documentation change. That blocks the agent coordinator from merging, and slows every pull request that touches a doc, without checking anything a script cannot check.
- **Allow editing an accepted ADR to mark it superseded.** It is common practice, but it lets an agent rewrite a past decision in the same edit. The check cannot tell a status update from a rewrite, so the superseding ADR carries the link instead.
- **Freeze only ADRs.** Design records and evidence are cited as the reason behind current behaviour. A silent rewrite of them does the same damage.

## Consequences

- An old ADR does not say it has been superseded. Readers find the newer decision through the ADR list, where later numbers win, and through links from the living pages.
- A fix to a typo or a broken link in a dated document needs a superseding document, or is left alone. Dated documents are not link-checked.
- The comparison needs the history back to the fork point, so CI checks out the full history (a shallow clone fails with the command to fix it). Comparing with the merge base rather than main's tip means a branch that is behind main is judged only on its own changes. `DOCS_BASE` or `--base` selects another base.
- A path that is removed from the `dated` globs in `docs/inventory.toml` is no longer frozen. That is how documents were moved into `docs/dated/`. Such a change shows in review as an edit to the inventory.
