# Connect an AI agent

`quivr mcp` lets an AI agent use a running Quivr as a knowledge source. It speaks
MCP (Model Context Protocol, the standard way agents plug into tools) on stdin and
stdout. With the `read` profile the agent can list the Corpora it may reach, search
them, and open a Record to cite exactly where a passage comes from; no tool of this
profile changes data. The `ingest` profile adds two tools to add text and follow it
until it is searchable. No profile has a tool that withdraws, deletes or rebuilds.

## Start it

The agent's MCP client starts the command itself. It needs a running Quivr server and
an API key, read from the environment like every [online command](api-walkthrough.md#from-the-command-line):

```sh
QUIVR_API_URL=http://127.0.0.1:<api_port> \
QUIVR_API_KEY=<key with corpora:read, content:read and search:query> \
quivr mcp --profile read   # or --profile ingest, with a key that also has content:write
```

Most MCP clients accept a configuration like this one:

```json
{
  "mcpServers": {
    "quivr": {
      "command": "quivr",
      "args": ["mcp", "--profile", "read"],
      "env": {
        "QUIVR_API_URL": "http://127.0.0.1:<api_port>",
        "QUIVR_API_KEY": "<api key>"
      }
    }
  }
}
```

`--profile` is required. `quivr mcp --help` lists the profiles and their tools; the
[MCP reference](reference/mcp.md) details every tool and its arguments.

## What the agent can do

| Tool | Use it to |
| --- | --- |
| `list_corpora` | learn which Corpora the key may read, and their `corpus_id` |
| `search` | find passages in one or more Corpora for a question |
| `read_record` | open a hit's Record and Version, with the full Manifest |
| `ingest_text` | `ingest` only: add text to a Corpus as a Record |
| `read_receipt` | `ingest` only: follow the Ingestion Receipt of that text |

`list_corpora`, `search`, `ingest_text` and `read_receipt` return the public API
response unchanged. `read_record`
returns `{"record": …, "version": …}`, the unchanged responses of the Record and
Version endpoints. Every field is the one the
[OpenAPI contract](../contracts/http/v0/openapi.yaml) describes. The tools' own
descriptions tell the agent when to use each one and what its output means.

## Cite a source

A search hit carries everything a citation needs: `record_id`, `version_id` (the
Record Version the passage comes from), `part_key` (the Part of that Version) and
`excerpt`, whose `text` is the exact slice `[start, end)` of that Part's text. Offsets
count Unicode code points. `read_record` with the hit's `record_id` and `version_id`
returns that Version, so the agent can read the passage in context. When
`record.current_version_id` differs, the Record has a newer Version.

## Add text

`ingest_text` takes a `corpus_id`, a `record_key` (the agent's stable name for the
Record; another text under the same key corrects it) and the `text`, plus an optional
`namespace` (default `mcp`). Ingestion is asynchronous: the call returns an Ingestion
Receipt once the text is accepted. The agent calls `read_receipt` until `state` is
`resolved` and `availability.searchable` is `true`; from then on `search` finds the
text. It stops and reports the diagnostics on a `conflict` outcome, a `quarantined`
Version, `blocked` processing, or a `retrieval_ready` Version that is no longer current
because another text replaced it.

Retries never duplicate. Without an `idempotency_key` argument the tool derives one
from `corpus_id`, `namespace`, `record_key` and `text`, so the same call returns the
same Receipt. If another text has replaced that Receipt's Version since, the call
ingests the text again, so going back to an earlier text works once the replacing text
is searchable; before that, pass an explicit key. An agent that passes its own key
controls replay itself; reusing it with other arguments returns `idempotency_conflict`.

## Who can see what

The API key decides what every call may reach, on the server. The profile only
narrows which tools the agent sees, so it can never widen access: `ingest_text` with a
key that lacks `content:write` returns a `forbidden` tool error. A Corpus outside the
key's scope never appears in `list_corpora`. Searching it returns a tool error with the
public code (`forbidden`), never an empty result. The agent receives failed calls as
tool errors that carry the public error code and a hint, for example `unreachable` when
no server answers.
