<!-- Generated from catalogue.go by `make generate`. Do not edit. -->

# MCP reference

> Generated from `catalogue.go` by `make generate`. Do not edit this page: change the source and regenerate.

Tools of `tool mcp`.

## Profiles

`--profile` picks one profile. A profile only narrows which tools the agent sees; the API key decides, on the server, what every call may reach.

| Profile | Summary | Tools |
| --- | --- | --- |
| [`read`](#profile-read) | read items; no tool changes data | [`find`](#tool-find) |
| [`write`](#profile-write) | add items | [`find`](#tool-find), [`add`](#tool-add) |

## Profile read

Read items; no tool changes data.

```sh
quivr mcp --profile read
```

| Tool | Title | Read-only |
| --- | --- | --- |
| [`find`](#tool-find) | Find items | yes |

Instructions the agent receives when it connects:

```text
Call find first.
```

## Profile write

Add items.

```sh
quivr mcp --profile write
```

| Tool | Title | Read-only |
| --- | --- | --- |
| [`find`](#tool-find) | Find items | yes |
| [`add`](#tool-add) | Add an item | no |

Instructions the agent receives when it connects:

```text
Call add, then find.
```

## Tools

### Tool find

**Find items**

| Read-only | Profiles |
| --- | --- |
| yes: the tool changes no data | [`read`](#profile-read), [`write`](#profile-write) |

Description the agent receives:

```text
Find items.
Returns {"items": [...]}.
```

| Argument | Type | Required | Description |
| --- | --- | --- | --- |
| `query` | string | yes | what to find<br><br>At least 1 character. |
| `kinds` | array of string | no | At most 3 items. Items are unique. Each item: one of `a`, `b`. |
| `limit` | integer | no | maximum hits<br><br>Minimum 1. Maximum 50. |

<details>
<summary>Input schema</summary>

```json
{
  "type": "object",
  "required": [
    "query"
  ],
  "properties": {
    "query": {
      "type": "string",
      "minLength": 1,
      "description": "what to find"
    },
    "kinds": {
      "type": "array",
      "maxItems": 3,
      "uniqueItems": true,
      "items": {
        "type": "string",
        "enum": [
          "a",
          "b"
        ]
      }
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "description": "maximum hits"
    }
  }
}
```

</details>

### Tool add

**Add an item**

| Read-only | Profiles |
| --- | --- |
| no: the tool can change data | [`write`](#profile-write) |

Description the agent receives:

```text
Add an item.
```

The tool takes no arguments.

<details>
<summary>Input schema</summary>

```json
{
  "type": "object"
}
```

</details>
