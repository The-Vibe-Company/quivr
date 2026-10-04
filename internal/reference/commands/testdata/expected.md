<!-- Generated from cmd/tool/table.go by `make generate`. Do not edit. -->

# Command-line reference

> Generated from `cmd/tool/table.go` by `make generate`. Do not edit this page: change the source and regenerate.

Every command of `tool`.

## Commands

| Command | Needs | Summary |
| --- | --- | --- |
| [`tool lint`](#tool-lint) | nothing: works offline | Check a directory. |
| [`tool fetch`](#tool-fetch) | a running server | Fetch one item. |

## Offline commands

They need no server.

| Exit code | Meaning |
| --- | --- |
| 0 | success |
| 2 | invalid \| arguments |

### tool lint

Check a directory.

```text
tool lint [--fix] <dir>
```

## Online commands

| Environment variable | Meaning |
| --- | --- |
| `TOOL_URL` | server URL |

### tool fetch

Fetch one item.

`tool fetch --help` prints:

```text
usage: tool fetch <id>

Flags:
  -json
    	print JSON

Exit codes:
  0  success
```
