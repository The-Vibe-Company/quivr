# Documentation conventions

How documentation is organised in this repository, and when it may change. `make docs` (`scripts/docs.py`, the first step of `make verify`) checks the inventory, links and repository paths, line budgets, the glossary form, start pages, the site navigation and frozen dated documents, and each failure names the rule, the file, the line and the fix. The other conventions here, including the signal rule, are checked in review.

## Living and dated documents

- **Living** documents describe current behaviour. They are updated in the pull request that changes that behaviour, and their links and repository paths must resolve.
- **Dated** documents record what was decided or observed at a date: ADRs in `docs/adr/`, and design records, evidence and research in `docs/dated/` (listed in [its index](../dated/README.md)). Each starts with `Date:` and `Status:` lines and keeps its original language. Once merged it is frozen: `make docs` fails when a branch edits or removes one, unless its status is `proposed`, so a newer document supersedes it instead ([ADR 0004](../adr/0004-documentation-rules-are-enforced-by-ci-only.md)).

Living documentation is written in English, with neutral examples: no customer names or configurations (see the generic-repository rule in `AGENTS.md`). Contract facts such as endpoints, request shapes, limits and error codes live in their artifacts (the OpenAPI contract, the code); prose links to them instead of restating them.

## The inventory

`docs/inventory.toml` is the single list of living pages. Every Markdown file is either declared under `[pages]`, matched by the `dated` list, or matched by the `excluded` list (Markdown that is not Quivr documentation). A new living page is one line, in path order:

```toml
"docs/path/to/page.md" = { audience = "functional", kind = "guide" }
```

- **audience**: `functional` (integrators and non-developers), `plugin-author` (people extending Quivr with plugins) or `contributor` (people and coding agents changing this repository).
- **kind**: `guide` (steps a reader follows), `concept` (how and why something works), `generated-reference` (generator output; edit the source), `index` (mostly links) or `start-page` (see below).
- **summary** (optional): one line shown after the page's title on its start page.

Each audience has one start page in `docs/start/`, generated from the inventory: it lists every other page of that audience, grouped by kind, and never a dated document. After declaring, moving or retitling a page, run `make start-pages`; `make docs` fails with `stale-start-page` until the start pages match. The README links to the start pages instead of listing files.

## Line budgets

`AGENTS.md`, `CONTEXT.md` and every page of kind `guide` have a maximum number of lines under `[budgets]` in the inventory. A new budget is set about 5% above the page's size when it is added; `make docs` prints the line to paste. When a page reaches its budget, shorten it first: link to the authoritative source, remove repetition, split a guide by task. Raise a budget only in a pull request whose signal needs the extra lines, and say so in its description.

## The documentation site

The public site (Mintlify, built from `docs-site/` on main) is where users and plugin authors learn Quivr. Its MDX pages and `docs.json` (settings and navigation) are written by hand: each page answers one question and is one of tutorial, how-to, reference or explanation. A change to user-visible behaviour updates its page in the same pull request. Show API requests there only as [runnable blocks](../runnable-guides.md), which `make verify` replays. Contributor process, dated documents and ADRs stay off the site. Site pages are living documents: the signal rule below applies to them.

Generated pages are never edited: `openapi.yaml`, `reference/cli.mdx`, `reference/mcp.mdx` and `reference/plugin-*.mdx`. After changing the HTTP contract, a command or the plugin schemas, run `make generate`, then `make docs-site`. `make docs` fails with `stale-docs-site` until they match, `site-navigation` when a page and `docs.json` disagree, `site-link` on a broken root-relative link, and `site-configuration` when an engine configuration key is missing from `reference/configuration.mdx`. `make docs-preview` serves the site locally; CI runs Mintlify's checks (`make docs-site-check`).

## Glossary form

`CONTEXT.md` is the engine glossary. Each term is one paragraph: the term in bold followed by a colon, one or two sentences of definition, then an `_Avoid_:` line listing words not to use for it. Repository conventions such as living, dated, inventory and signal belong on this page, not in the glossary.

```markdown
**Corpus**:
A logical collection of records that share an access and retrieval boundary.
_Avoid_: Index, database
```

## When documentation may change

A living document changes only because of a **signal**:

1. the same pull request changes the behaviour it documents;
2. a bug, or an error coding agents keep making, is traced to a gap or mistake in it;
3. a review comment asks for the change;
4. a user question shows it is missing or wrong.

Without a signal, do not rewrite, reorganise or polish documentation. Name the signal in the pull-request description so a reviewer can check it. CI does not enforce this rule.
