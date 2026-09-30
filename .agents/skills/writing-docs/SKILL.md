---
name: writing-docs
description: Writing Quivr's public documentation site. Use when adding or editing a docs page, documenting a new feature, plugin type, API or CLI change, or reviewing documentation.
---

The docs site is the first thing a developer sees of Quivr. Every page answers one question for one reader, in the fewest words that fully answer it. The bar is the docs of the best-run open-source projects: calm, concrete, and correct to the last command.

## Steps

1. **Name the reader and their question.** Write one line: who arrives on this page (evaluator, operator, plugin author, API client) and what they want to do or understand when they leave. Done when the line fits in one sentence.

2. **Pick exactly one page type**: _tutorial_, _how-to_, _reference_ or _explanation_ (Diátaxis). Read its template in [PAGE-TYPES.md](PAGE-TYPES.md). A page that wants two types is two pages; link them. Done when the page's first heading matches the type's template.

3. **Find its home in the site map** below. Prefer extending an existing page over adding one; search the site folder for the topic first. A new page goes in the navigation next to its siblings. Done when you can name the page before and after it in the navigation.

4. **Write it** following [STYLE.md](STYLE.md). Use the domain terms of `CONTEXT.md`, and explain each in plain words the first time a newcomer meets it.

5. **Run everything you wrote.** Every command, request and code block runs on a clean checkout exactly as printed, with its output checked against what the page claims. API requests are [runnable blocks](../../../docs/runnable-guides.md) (a `{/* runnable */}` mark before the fence), which `make verify` replays; see [STYLE.md](STYLE.md#runnable-examples) for how to write them. A snippet you could not run is removed or marked as illustrative in the text around it. Done when each block has been run once since its last edit.

6. **Regenerate and check the site.** After a change to the HTTP contract, a command or the plugin schemas, run `make generate`, then `make docs-site` (it rewrites only the generated pages). Then run `make docs`, `make denylist` and `make docs-site-check` (Mintlify validation and anchor-checked links), and preview with `make docs-preview` any page that uses components. `docs/agents/documentation.md` names every failure rule. Done when all pass and the page renders.

7. **Reread as the reader from step 1**, top to bottom, then do the final pass in [STYLE.md](STYLE.md#final-pass). Cut every sentence that reader does not need. Done when no sentence survives that fails the reader test.

## Site map

| Section | Holds | Reader |
| --- | --- | --- |
| **Get started** | Introduction, Quickstart, Core concepts | Someone deciding whether Quivr fits, then trying it |
| **Plugins** | How plugins work, Plugin types, Build your first plugin, one how-to per type, first-party plugin catalogue | Plugin authors |
| **Guides** | One task per page: add content, search, alerts, connectors, AI agents (MCP) | Operators and API clients |
| **Reference** | HTTP API, CLI, MCP tools, plugin contract, configuration, errors | Anyone looking up an exact fact |

Reference pages generated from contracts (`openapi.yaml`, `reference/cli.mdx`, `reference/mcp.mdx`, `reference/plugin-*.mdx`) are edited at their source, never in the generated output. A guide joins the site only once its steps can be run end to end; one that needs paid infrastructure or a third-party console waits until it can.

What stays off the public site: contributor process (`AGENTS.md`, `docs/agents/`), dated documents and ADRs, internal limits notes. They live in the repository for contributors.

## When the product changes

A change to public behaviour updates its page in the same pull request: a new plugin type gets a card on "Plugin types", a how-to and a contract reference entry; a new API field gets its reference and, if it changes a task, the guide that performs that task. Name the page you changed in the pull request.
