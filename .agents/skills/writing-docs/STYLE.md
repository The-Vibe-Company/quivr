# Style

The reader is a developer with a task and little time. Write the way a senior engineer explains something to a colleague at their desk: direct, specific, friendly, and done when the point is made.

## Voice

- Second person and present tense: "You pin a plugin in `QUIVR_CONFIG`." The product is "Quivr".
- Short sentences, one idea each. A paragraph holds one point in two to four sentences.
- Lead with the action or the answer, then the reason if the reader needs it.
- Plain verbs and nouns: _use_, _run_, _send_, _stores_, _returns_. A claim is a fact the reader can check: a field, a status code, a limit, a latency.
- State limits and failure modes plainly, next to the feature they limit.

## Words

- Domain terms come from `CONTEXT.md` (Corpus, Record, Version, Receipt, Plugin, Contribution…). Introduce each with one plain sentence on first use in a page, then use it consistently. One thing, one name.
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

## Examples

- Every example is complete and runnable: real commands, real field names, real responses trimmed with `…` only where the omission is obvious.
- Use neutral example data: `acme`, `example.com`, "A new library opens downtown". The repository is public and generic.
- Show the output the reader should see, so they know it worked.
- One example that teaches beats three that repeat.

## Mintlify components

Use a component when it changes how the reader moves through the page:

- `<Steps>` for tutorials and how-tos with ordered actions.
- `<CardGroup>` / `<Card>` for "choose where to go next" and the plugin types overview.
- `<Tabs>` or `<CodeGroup>` when the same action has variants (curl, CLI, Go).
- `<Note>` for context worth a detour, `<Warning>` for data loss, security or irreversible actions. At most one or two per page.
- Mermaid for one diagram per concept page, with five to eight boxes.

Plain Markdown is the default; a page with no component is fine.

## Final pass

Read the page once for each line below and fix what you find:

1. Every sentence helps the reader from step 1 of the skill; the rest is cut.
2. Every heading is a noun phrase or a task ("Pin a plugin"), and the headings alone outline the page.
3. No paragraph restates the previous one or summarises the page.
4. Every term is either in `CONTEXT.md` or explained in the page.
5. Every code block was run since its last edit.
6. The page links to its neighbours: the prerequisite before it, the next step after it.
7. The swaps in the Words table are applied; run the `humanizer` skill if it is available.
