# Connect an AI agent

`quivr mcp` lets an AI agent use a running Quivr as a knowledge source. It speaks
MCP (Model Context Protocol, the standard way agents plug into tools) on stdin and
stdout. With the `read` profile the agent can list the Corpora it may reach, search
them, and open a Record to cite exactly where a passage comes from. No tool of this
profile changes data.

## Start it

The agent's MCP client starts the command itself. It needs a running Quivr server and
an API key, read from the environment like every [online command](api-walkthrough.md#from-the-command-line):

```sh
QUIVR_API_URL=http://127.0.0.1:<api_port> \
QUIVR_API_KEY=<key with corpora:read, content:read and search:query> \
quivr mcp --profile read
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

`--profile` is required. `quivr mcp --help` lists the profiles and their tools.

## What the agent can do

| Tool | Use it to |
| --- | --- |
| `list_corpora` | learn which Corpora the key may read, and their `corpus_id` |
| `search` | find passages in one or more Corpora for a question |
| `read_record` | open a hit's Record and Version, with the full Manifest |

`list_corpora` and `search` return the public API response unchanged. `read_record`
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

## Who can see what

The API key decides what every call may reach, on the server. The profile only
narrows which tools the agent sees, so it can never widen access. A Corpus outside the
key's scope never appears in `list_corpora`. Searching it returns a tool error with the
public code (`forbidden`), never an empty result. The agent receives failed calls as
tool errors that carry the public error code and a hint, for example `unreachable` when
no server answers.
