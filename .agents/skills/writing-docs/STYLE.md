# Style

The site has two kinds of readers: developers with a task and little time, and people who have to explain Quivr to someone else. Write the way a senior engineer explains something to a colleague at their desk: plain words first, one picture where it helps, and done when the point is made.

## Open with the answer

- The first paragraph answers the page's question in at most two sentences a newcomer could repeat aloud. Detail comes after.
- One page serves one reader. A section written for another reader moves to that reader's page, and this page links to it.
- A how-to or explanation ends with a short "Next" section: one to three links to the step after this one, or the concept behind it. A tutorial ends with "What you built"; a reference page needs neither.

## Voice

- Second person, present tense, active voice, subject first: "You send a document", "Quivr returns the passages". The product is "Quivr".
- One idea per sentence, one point per paragraph of two to four sentences.
- Sentence length is a diagnostic, not a gate: aim for 20 words or fewer on average. A condition sometimes needs a longer sentence; a sentence that carries two ideas becomes two.
- Plain verbs and nouns: _use_, _run_, _send_, _stores_, _returns_. A claim is a fact the reader can check: a field, a status code, a limit, a latency.
- State limits and failure modes plainly, next to the feature they limit.

## Words

- **Ground every term before a block leans on it.** A heading, sentence, table, diagram label or code block may use a term only once the page has said what it is in plain words, or links to where it is explained. A diagram placed before the prose uses only plain-word labels.
- Plain word first, product term second, the first time: "a document (a Record, in the API)". After that, use one name per thing.
- Pick words for the page type:

  | Page | Words |
  | --- | --- |
  | Introductory: the home page, Quickstart, concept overviews | Plain words. "Document" is the plain gloss for a Record, "collection" for a Corpus. Product terms appear once, glossed. |
  | How-to and tutorial | The API terms the reader will type and see in responses and errors (Corpus, Record, Version, Receipt), each grounded on first use. |
  | Reference | The exact API names. |

- Internal terms (projection, generation, lease, currentness, owner of a space, pinned work) stay out of introductory prose. An operator page that needs one defines it in a sentence where it is used.
- Terms come from `CONTEXT.md`. Related objects keep their own names: a Saved Query and a Subscription are two things, and so are a connector kind and a Connector Instance. Explain how they relate rather than merging them into one word.
- Code identifiers, fields, paths, commands and values go in `code`.
- Replace AI filler with the specific fact it hides. Examples of the swap:

  | Instead of | Write |
  | --- | --- |
  | "a robust, seamless way to leverage plugins" | "plugins are HTTP services Quivr calls with JSON" |
  | "Quivr plays a crucial role in…" | what Quivr does, as a verb |
  | "It's important to note that X" | "X" |
  | "not only A but also B" | "A and B" |
  | three adjectives in a row | the one that carries information |
  | an em-dash aside | a separate sentence or a comma |
  | "In summary, …" closing paragraph | end on the last useful fact or the next step |

## Claims

- **Current behaviour only.** Describe what the code does today. A concept from `CONTEXT.md` or a design document that has not shipped stays off the page, or gets one sentence saying it is planned, with no instructions. Leave out history ("now", "moved", "before 0.12").
- **Conditional guarantees keep their condition.** State a guarantee with its bound, and its failure case: webhook attempts can repeat, so a receiver deduplicates, and retries stop on a permanent failure or when the retry window ends, so delivery is not guaranteed; a retry with the same idempotency key and the same request returns the same Receipt, and the same key with a different request is refused. "Nothing is lost" and "never duplicates" are written only with the condition that makes them true.
- **Say how each step is known to work.** A runnable block is replayed by `make verify`; another block was run by hand; a step that needs external credentials (a third-party console, a paid model key) says so in its prerequisites; a block that was not run is introduced as an example ("For example, not run:"). A label does not replace running a step that needs no external credentials.

## Examples and steps

- **Prerequisites are explicit.** List everything step 1 needs: each tool with the command that checks it, a running Quivr, and the exact API key permissions (`content:write`, `search:query`) or operator access the task requires.
- **Placeholders tell the truth.** A placeholder looks like one (`<your API key>`), and the text says where its value comes from. A value that only works on the local stack says so: "`http://127.0.0.1:41863`, the address `make dev` prints". Never write a made-up value that looks real.
- **Expected output is checked.** Show the output the reader should see, copied from a run, so they know it worked. Output that can drift (versions, counts) is replayed or left out.
- **Cleanup and reruns.** A page that creates something says how to remove it, or that leaving it is harmless. Say what running a step twice does: the same result through its idempotency key, a conflict, or a duplicate.
- Use neutral, consistent example data, and reuse the data of neighbouring pages (the harbour and ferry articles of Add content, Search and the alert guides): `acme`, `example.com`. The repository is public and generic.
- Every example is complete: real commands, real field names, real responses trimmed with `…` only where the omission is obvious. One example that teaches beats three that repeat.

## Runnable examples

- Mark each API request with `{/* runnable */}` on the line before its fence; `make verify` replays it against the local stack in page order.
- Use the variables the dev stack exports (`eval "$(make -s env)"`): `QUIVR_API_URL`, `QUIVR_API_KEY`, and the ones a page defines itself. Carry an identifier forward with `export X=<the x above>` and say where it comes from.
- Send JSON bodies with a heredoc, and pipe responses through `jq` so the printed output matches the page exactly.
- Every verify command exits 0 under `pipefail`: no `| head`, and no bare command that prints usage and exits 2.

## Diagrams

Add a diagram where it carries understanding that prose alone does not: three or more actors, a flow that branches, a life cycle with states. There is no quota; a page without one is fine. Load the `mermaid-diagrams` skill for the syntax.

- Mermaid `flowchart`, `sequenceDiagram` or `stateDiagram-v2` only, with no `%%{init}%%` theme and no `style` or `classDef` colours: Mintlify styles Mermaid for light and dark mode.
- At most nine boxes (nodes or participants). Labels are plain words the page has grounded; endpoints and field names go in the text around the diagram.
- The sentence just before it is its one-line takeaway: what the reader should remember from the picture.
- A text alternative follows it: a short numbered list or paragraph that says the same thing in words, for screen readers, search and anyone who skips the picture.
- Flowcharts run top to bottom (`flowchart TD`). Mintlify shrinks a diagram to the page width, so a left-to-right row of four boxes is already too small to read on a phone; keep `LR` for three boxes or fewer.
- SVG only when Mermaid cannot carry the idea, such as an annotated screenshot or a spatial layout. Put it in `docs-site/images/`, and make it work in light and dark mode: colours that read on both, or a light and a dark file shown with `className="block dark:hidden"` and `className="hidden dark:block"`. The design-phase pictures in `docs/assets/` describe old designs; do not reuse them.

## Readable on a phone

- On tutorials, how-tos and explanations, tables have at most three columns, with cells under about 25 words, and two columns when cells hold code or field names, which do not wrap. Longer content becomes a list or a section. Reference tables may be wider: readers look them up rather than read them.
- Keep code lines under about 100 characters; break long commands with `\`.
- Check the page at phone width in `make docs-preview` (browser developer tools, 390 pixels wide) whenever it has a table, a diagram or a wide code block.

## Mintlify components

Use a component when it changes how the reader moves through the page:

- `<Steps>` for tutorials and how-tos with ordered actions.
- `<CardGroup>` / `<Card>` for "choose where to go next" and the plugin types overview.
- `<Tabs>` or `<CodeGroup>` when the same action has variants (curl, CLI, Go).
- `<Accordion>` for long material most readers skip, such as a full manifest or shell plumbing.
- `<Note>` for context worth a detour, `<Warning>` for data loss, security, data leaving the installation, or paid calls. At most one or two per page, and never a requirement hidden in a `<Note>`.

Plain Markdown is the default; a page with no component is fine.

Mintlify traps:

- Any word after the fence language becomes the block's title. Title partial code `file.py (excerpt)`, never a bare path.
- `<Step>` titles create no anchors, so `mint broken-links --check-anchors` fails on links to them; link to a heading instead.
- Troubleshooting, lifecycle actions and option lists read better as tables than as bullets with bold lead-ins.

## Final pass

Read the page once for each line below and fix what you find:

1. The first paragraph answers the question from step 1 of the skill; every other sentence helps that reader, and the rest is cut.
2. Every heading is a noun phrase or a task ("Pin a plugin"), and the headings alone outline the page.
3. No paragraph restates the previous one or summarises the page.
4. Every term is grounded before a block leans on it, with words that suit the page type.
5. Every code block was run since its last edit, or is introduced as an example; prerequisites, permissions and placeholders are explicit.
6. Every guarantee carries its condition, and nothing describes behaviour that has not shipped.
7. Every diagram has a takeaway before it and a text alternative after it, and renders in light mode, dark mode and at phone width.
8. The page links to its neighbours: the prerequisite before it, the next step after it.
9. The swaps in the Words table are applied; run the `humanizer` skill if it is available.
