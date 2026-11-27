# __PLUGIN_ID__

A Quivr retrieval plugin written with the Go plugin SDK
(`github.com/The-Vibe-Company/quivr/sdks/go`). It answers a search in two
rounds: round 1 asks the engine for candidates, round 2 ranks the candidates
the engine served.

| Profile | Round 1 | Round 2 |
| --- | --- | --- |
| `default` | Keyword (`bm25`) and vector (`near_vector`) candidates, following the search mode | Reciprocal rank fusion |
| `composed` | The full ranking of `core.retrieve/default`, through a `profile` request | Hits containing every query word first, in the base order |

A `profile` request composes another plugin's profile: declare the plugin and
the profile under `requires` in `quivr-plugin.yaml`, or the engine refuses it.

## Files

- `quivr-plugin.yaml`: the manifest, with the profiles, their latency and cost
  budgets, the request limits and the configuration schema.
- `main.go`: starts the plugin server.
- `retriever.go`: the `Search` handler.
- `retriever_test.go`: unit tests of the handler.
- `fixtures/*.json`: retrieval fixtures, one per profile, that
  `quivr plugin test` replays with an offline catalogue and checks against
  `expect.top`.

## Develop

```bash
go test ./...
quivr plugin inspect .
quivr plugin test .
```

`quivr plugin test` starts the plugin with `go run .`, replays the normative
and own fixtures under every profile they name, and certifies the answers with
the engine's rules. Change the SDK version with
`go get github.com/The-Vibe-Company/quivr/sdks/go@<version> && go mod tidy`.
