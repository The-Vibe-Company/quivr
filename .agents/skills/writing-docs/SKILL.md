---
name: writing-docs
description: Writing Quivr's public documentation site. Use when adding or editing a docs page, documenting a new feature, plugin type, API or CLI change, or reviewing documentation.
---

The docs site is the first thing a developer sees of Quivr, and what people use to explain it to others. Every page answers one question for one reader, in plain words and the fewest that fully answer it, with a picture where a picture helps. The bar is the docs of the best-run open-source projects: calm, concrete, and correct to the last command.

## Steps

1. **Name the reader and their question.** Write one line: who arrives on this page (evaluator, operator, plugin author, API client) and what they want to do or understand when they leave. Done when the line fits in one sentence.

2. **Pick exactly one page type**: _tutorial_, _how-to_, _reference_ or _explanation_ (Diátaxis). Read its template in [PAGE-TYPES.md](PAGE-TYPES.md). A page that wants two types is two pages; link them. Done when the page's first heading matches the type's template.

3. **Find its home in the site map** below. Prefer extending an existing page over adding one; search the site folder for the topic first. A new page goes in the navigation next to its siblings. Done when you can name the page before and after it in the navigation.

4. **Write it** following [STYLE.md](STYLE.md). Use the domain terms of `CONTEXT.md`, and explain each in plain words the first time a newcomer meets it.

5. **Run everything you wrote.** Every command, request and code block runs on a clean checkout exactly as printed, with its output checked against what the page claims. API requests are [runnable blocks](../../../docs/runnable-guides.md) (a `{/* runnable */}` mark before the fence), which `make verify` replays; see [STYLE.md](STYLE.md#runnable-examples) for how to write them. A snippet you could not run is removed or introduced as an example ("For example, not run:"). Done when each block has been run once since its last edit, or is introduced as an example.

6. **Regenerate and check the site.** After a change to the HTTP contract, a command or the plugin schemas, run `make generate`, then `make docs-site` (it rewrites only the generated pages). Then run `make docs`, `make denylist` and `make docs-site-check` (Mintlify validation and anchor-checked links), and preview with `make docs-preview` any page that uses components. `docs/agents/documentation.md` names every failure rule. Done when all pass and the page renders.

7. **Reread as the reader from step 1**, top to bottom, then do the final pass in [STYLE.md](STYLE.md#final-pass). Cut every sentence that reader does not need. Done when every line of the final pass holds.

8. **Run the reader test.** Write 5 to 8 questions the reader from step 1 would ask: the page's main answer, a prerequisite or permission, the result of one step, one failure. Give each question to a fresh sub-agent that may read only the page's file, with no other file, tool or conversation. Ask for a short answer, a confidence, and the terms or steps it could not follow, with "the page does not say" instead of a guess. Check each answer against the facts. Fix the page where an answer is wrong or unsure, or where it names a gap that reader needs filled, then ask the failed questions again with new sub-agents. Record the questions, the verdicts and the fixes in the pull request. Done when every answer is right and no needed gap remains.

## Site map

The navigation lives in `docs-site/docs.json`. This table mirrors it; a pull request that changes the navigation updates the table too.

| Tab › group | Holds | Reader |
| --- | --- | --- |
| Documentation › **Get started** | Introduction, Quickstart, Core concepts | Someone deciding whether Quivr fits, then trying it |
| Documentation › **Plugins** | How plugins work, Plugin types, Build your first plugin, and _Write a plugin_ with one how-to per type (push sources included) | Plugin authors |
| Documentation › **Run Quivr** | First-party plugins, pin, upgrade or switch, migrate alert rules, retire evaluations, configure alert fields, backfill a vector space, reprocess quarantine, re-rank with Jev | Operators |
| Documentation › **Guides** | One task per page: add content, search, _Alerts_ (how alerts work, keyword and meaning alerts), _Collect from sources_ (connectors, RSS, Microsoft 365, X), connect an AI agent | Integrators calling the API |
| **Reference** tab | HTTP API overview and endpoints, CLI, MCP tools, plugin manifest and protocol, configuration | Anyone looking up an exact fact |

Name a page's reader from step 1. Plugin-author tasks go in Plugins; operator tasks go in Run Quivr next to the related procedure.

Reference pages generated from contracts (`openapi.yaml`, `reference/cli.mdx`, `reference/mcp.mdx`, `reference/plugin-*.mdx`) are edited at their source, never in the generated output. A guide joins the site once every step that needs no external credentials has been run end to end. A step that needs a third-party console, a provider account or a paid key may stay: the page lists those credentials in its prerequisites and says which steps were run and which are examples ([STYLE.md](STYLE.md#claims)).

What stays off the public site: contributor process (`AGENTS.md`, `docs/agents/`), dated documents and ADRs, internal limits notes. They live in the repository for contributors.

## When the product changes

A change to public behaviour updates its page in the same pull request: a new plugin type gets a card on "Plugin types", a how-to and a contract reference entry; a new API field gets its reference and, if it changes a task, the guide that performs that task. Name the page you changed in the pull request.
