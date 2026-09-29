# RSS and Atom connector plugin

`connector.rss` is the first-party connector plugin that provides the `rss`
kind: it polls one RSS 2.0, RSS 1.0, Atom or JSON Feed document per Connector
Instance. What it collects, its bounds and its health codes are in
[the operator guide](../../docs/connectors/rss.md). It is a Go module built
only on the [Go plugin SDK](../../sdks/go/README.md).

## Build and pin

```sh
cd plugins/rss
go build -o quivr-rss .
QUIVR_PLUGIN_PORT=9920 ./quivr-rss
```

It prints one line when it starts, for example
`quivr-rss: serving connector.rss@1.0.0 (connector kind rss) on 127.0.0.1:9920`.
Then pin it in the core configuration of api and worker and restart them:

```json
{"plugins": [{"manifest": "/path/to/plugins/rss/quivr-plugin.yaml", "endpoint": "http://127.0.0.1:9920", "configuration": {}}]}
```

The local stack (`scripts/connector_plugin.py`) and the Railway image
(`deploy/railway/core-entrypoint.py`) build, run and pin it by default.

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `allow_private_addresses` | `false` | Fetch feeds on loopback, private and link-local addresses. Local and CI stacks only. |

The credential is optional (`credential_required: false`): public feeds need
none, protected ones take `{"username", "password"}` or `{"token"}`.
`check_credential` refuses a credential only when the feed answers 401 or 403;
any other outcome is left to the next fetch.

## Compatibility with the built-in kind

The kind was built into the core before. The plugin keeps its kind name, its
schemas, its Record Keys and revisions, its extension namespace and its
checkpoint format, so existing instances continue from their checkpoints with
no duplicate and no gap. `testdata/golden` holds pages and checkpoints written
by the built-in code; `parity_test.go` replays them. One bound is new: a page
stops after about 12 MiB of items, under the 16 MiB response limit. The
manifest declares `max_checkpoint_bytes: 131072` (Plugin API 0.3.1) so the
2000 revisions the checkpoint keeps still fit.

## Test

```sh
go test ./...                          # unit, parity and cutover tests
quivr plugin test --report report.json .  # the Contract Runner on fixtures/
```

`make test` runs both (`scripts/plugin_sdk_go.sh`).
