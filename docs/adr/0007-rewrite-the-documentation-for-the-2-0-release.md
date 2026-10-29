# Rewrite the documentation for the 2.0 release

Date: 2026-10-28

Status: accepted

The living documentation grew one capability at a time, as the signal rule in `AGENTS.md` requires: each pull request documented its own behaviour. Two months and 27 specs later, the result is accurate but hard to enter:

- the README lists every shipped capability in 56 bullets, so it reads as a changelog;
- the concepts page introduces receipts, phases and error codes before it says what a document is;
- 26 operator pages sit at one level, mixing everyday tasks with experimental tooling such as search campaigns and load tests.

No single pull request owns these problems, so no signal would ever fix them. The owner decided on 2026-10-28 to ship Quivr 2.0.0 with documentation a newcomer can follow ([THE-1936](https://linear.app/thevibecompany/issue/THE-1936)).

## Decision

The 2.0 release is the signal for a one-time restructuring of the README and the documentation site ([THE-1939](https://linear.app/thevibecompany/issue/THE-1939), [THE-1940](https://linear.app/thevibecompany/issue/THE-1940)). Those pull requests may rewrite, reorganise and shorten any living page they touch, without a behaviour change, review comment or user question behind each edit. They name this ADR as their signal.

The restructuring follows four rules:

- **People first.** A page explains an idea in plain words and one example before it names an API field. API names appear next to the plain word the first time, and the glossary maps each one back.
- **One reader per section.** The site is split into *Get started*, *Use Quivr* (application developers), *Run Quivr* (operators), *Extend Quivr* (plugin authors) and *Reference*. Experimental operator tooling sits in a separate group, marked as such.
- **One home per fact.** The README links to the site instead of repeating it. Contract facts stay in the contract and the generated references.
- **Same guards.** Runnable blocks keep their expected output, generated pages are regenerated rather than edited, and `make docs` and `make docs-site-check` stay green.

## Consequences

- After these pull requests merge, the signal rule applies again unchanged: later edits need their own signal.
- Page paths on the site stay where they are when possible, so existing links keep working; the navigation, not the URL, carries the new structure.
- Dated documents and accepted ADRs are not touched.
