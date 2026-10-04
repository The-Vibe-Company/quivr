# Runnable guide blocks

A page of the documentation site can mark its shell commands as runnable.
`make verify` then replays them against the real local stack, so a change in the
API breaks the page in CI instead of leaving it silently wrong. The
[Quickstart](https://docs.quivr.thevibecompany.co/quickstart)
(`docs-site/quickstart.mdx`) is the reference example.

## Mark a block

Put an MDX comment on the line before a shell fence, and follow the block with
the output it must print, marked the same way. The comments do not show on the
site:

````mdx
{/* runnable */}
```bash
curl -s -X POST "$QUIVR_API_URL/v0/corpora" -H "Authorization: Bearer $QUIVR_API_KEY" \
  -H "Content-Type: application/json" -d '{"name": "News", "idempotency_key": "news-corpus"}' | jq '{corpus_id, name}'
```

{/* output */}
```json
{"corpus_id": "{{CORPUS_ID}}", "name": "News"}
```
````

- The command runs with `bash -eo pipefail` in an empty temporary folder, shared by
  the page's blocks, with `QUIVR_API_URL` (the API address), `QUIVR_API_KEY` (a key
  with every action on the whole Organization) and `QUIVR_DESTINATION` (a webhook
  destination of that Organization) set, as `eval "$(make -s env)"` sets them for a
  reader. A non-zero exit fails the block.
- The output block is optional and must come right after its command, with only prose
  in between. A `json` output is compared as JSON; any other output is compared as
  trimmed text. Pipe the response through `jq` so that readers see exactly what the
  page shows.
- `{/* runnable retry */}` reruns the command, once a second for up to 60 seconds,
  until its output matches. Use it where the reader waits too: a Receipt resolving, a
  Record becoming searchable.
- Blocks without a mark, such as the reader's own `export` lines, are not run. A kept
  value (below) that a later block uses must be exported in such a block before that
  block, or the page is refused: readers copying it would miss it.

A Markdown page declared in the [documentation inventory](inventory.toml) can carry
runnable blocks too, marked `sh runnable` and `json output` on the fence itself.

## Expected JSON

The expected output asserts only what it shows, so write the stable fields a
reader relies on and leave the rest out.

| You write | It matches |
| --- | --- |
| an object | an object with at least these keys, each matching; other keys are ignored |
| an array | an array of the same length, item by item |
| an array ending with `"..."` | an array starting with these items, then any others |
| `"..."` | any value, such as a timestamp or an identifier nobody reuses |
| `"{{NAME}}"` | any string or number the first time, which is kept as `NAME`; the same value afterwards |
| anything else | exactly that value |

A kept value is exported as `$NAME` to the page's later blocks. Tell readers to do
the same with an unmarked block, for example `export CORPUS_ID=<the corpus_id above>`.

## How pages are replayed

`scripts/guides.py` reads every MDX page of `docs-site/` and every page declared in
the inventory. In `make verify`, after the timed scenarios, it replays each page with
runnable blocks in order, in its own Organization, and stops a page at its first
failing block because later blocks depend on it. Results are written to `guides.json`
in the verification folder. A failure names the page, the block and the difference:

```text
docs-site/quickstart.mdx:119: block 4: $.items[0].excerpt: expected "…Tuesday…", got "…Monday…"; output "{…}" (after 61 attempts)
```

Against a stack you started with `make dev`: idempotency keys are fixed, so a second
run replays the first one's results, and a page that changes content no longer
matches (`make reset` starts over):

```sh
python3 scripts/guides.py --list
eval "$(make -s env)" && python3 scripts/guides.py docs-site/quickstart.mdx
```
