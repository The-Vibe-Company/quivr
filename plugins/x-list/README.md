# X list connector plugin (`x-list`)

The first-party connector plugin behind the `x_list` kind: it polls the posts
of one X list through the X API v2 and withdraws posts deleted or made
protected at the source. The operator guide is
[X lists](https://docs.quivr.thevibecompany.co/guides/x); this page is for people who
build, test or pin the plugin.

- **Built only on the Go SDK** ([sdks/go](../../sdks/go/README.md)), as its own
  module; `make plugin-boundary` keeps it off the engine's packages.
- **Manifest** [quivr-plugin.yaml](quivr-plugin.yaml): plugin id and extension
  namespace `connector.x_list`, the names the former built-in kind used, so
  existing Connector Instances and Records carry over. Plugin API 0.5: the
  plugin binds Relation targets to the instance's `corpus_id` and
  `source_namespace`, receives webhook deliveries (`modes: [pull, push]`), and
  declares `max_checkpoint_bytes` 512 KiB for the deletion recheck set (up to
  2,000 posts) and the members of a webhook resync.
- **Webhook mode** ([guide](https://docs.quivr.thevibecompany.co/guides/x#real-time-mode)): pull runs
  keep Filtered Stream rules, the webhook and its link in step with the list
  members (`stream.go`); `Receive` answers the CRC check and maps signed
  deliveries with `MapPost`, like polling (`webhook.go`). The X endpoint shapes
  follow X's public docs and are tested against fakes only.
- **Configuration**: `api_endpoint`, the X API origin (default
  `https://api.x.com`). Set it only to point at a test fake.
  `allow_short_recheck` accepts `recheck_interval_seconds` below 60 so tests
  need not wait a minute; it holds only with a loopback `api_endpoint`, and
  every fetch fails with `invalid_configuration` otherwise.
- **Credential check**: answers ok without calling X, because X bills every
  request. A refused token shows at the first fetch as `unauthorized`.

## Pin it

`make dev`, `make verify` and the browser demo build and pin it on every stack
start (`QUIVR_X_LIST=off make dev` leaves it out); they point `api_endpoint` at
the local fake X API and set `allow_short_recheck`. The Railway image runs it beside the worker on
127.0.0.1:9930, and beside the API for webhook deliveries. Elsewhere, build it (`go build .` here) and pin it as
[Run a connector plugin](https://docs.quivr.thevibecompany.co/plugins/pin) explains.

## Test it

`make test` runs `scripts/plugin_sdk_go.sh`, which:

- runs `go vet` and `go test` here. The tests replay the scenarios of
  [testdata/scenarios.json](testdata/scenarios.json) against a fake X API and
  compare every X request, item, checkpoint, billed-read count, notice,
  diagnostic and error with [testdata/builtin](testdata/builtin), what the
  former built-in kind returned before it was deleted. Each run starts from the
  built-in kind's checkpoint, so each run also proves the cutover;
- certifies the plugin with `quivr plugin test` against `scripts/fake_x.py`
  ([fixtures](fixtures)); CI uploads the report as `x-list-contract-report`.

The goldens are frozen: a behaviour change updates them on purpose, in the same
pull request, and says why.
