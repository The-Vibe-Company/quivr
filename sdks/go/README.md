# Quivr Plugin SDK for Go

Package `quivrplugin` implements the connector Contribution of the Plugin
Protocol v0 ([contract](../../contracts/plugins/v0/README.md#connector-contribution),
Plugin API 0.3), so a source collector is one Go type. The core keeps the
schedules, checkpoints, credentials and health; your plugin only fetches pages
from the source. It depends only on `gopkg.in/yaml.v3` and
`santhosh-tekuri/jsonschema/v6`, never on Quivr's engine packages. Go 1.24 or
later. This SDK serves connectors only; write normalizers and alert rules with
the [Python SDK](../python/README.md).

```bash
go get github.com/The-Vibe-Company/quivr-v2/sdks/go
```

The sample [`examples/static-source`](examples/static-source/) is a complete
plugin: a manifest, a connector, fixtures and a test.

## A connector

Declare each kind in `quivr-plugin.yaml` with the JSON Schemas of its
configuration and credential:

```yaml
id: example.feeds
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.3.0 <0.4.0"
contributions:
  connector:
    kinds:
      feed:
        config_schema: {type: object, required: [url], properties: {url: {type: string}}}
        credential_schema: {type: object, required: [token], properties: {token: {type: string}}}
        default_interval_seconds: 900
run:
  command: [go, run, .]
```

Then implement `Fetch` and `CheckCredential` and serve:

```go
type feed struct{}

func (feed) Fetch(ctx context.Context, req *quivrplugin.FetchRequest) (*quivrplugin.Page, error) {
	var cfg struct{ URL string `json:"url"` }
	var cred struct{ Token string `json:"token"` }
	var at struct{ Cursor string `json:"cursor"` }
	_ = req.Connector.DecodeConfig(&cfg)
	_ = req.Credential.Decode(&cred)
	_ = req.DecodeCheckpoint(&at) // unchanged on the first run
	entries, next, err := fetchAfter(ctx, cfg.URL, cred.Token, at.Cursor)
	if err != nil {
		return nil, quivrplugin.TransientError("source_unavailable", "the feed did not answer")
	}
	page := &quivrplugin.Page{Checkpoint: map[string]string{"cursor": next}, More: next != at.Cursor && len(entries) == 50}
	for _, e := range entries {
		page.Items = append(page.Items, quivrplugin.Item{RecordKey: e.ID, Revision: e.ETag, Content: quivrplugin.Text(e.Body)})
	}
	return page, nil
}

func main() {
	plugin, err := quivrplugin.New("") // QUIVR_PLUGIN_MANIFEST, else ./quivr-plugin.yaml
	if err == nil {
		err = plugin.Connector("feed", feed{})
	}
	if err == nil {
		err = plugin.Serve() // QUIVR_PLUGIN_HOST / QUIVR_PLUGIN_PORT, until SIGTERM
	}
	if err != nil {
		log.Fatal(err)
	}
}
```

## What the SDK does

| Concern | Behavior |
| --- | --- |
| Routes | `GET /v0/discovery` (identity, `connector`, the highest Plugin API version the range admits, up to 0.3.1, the `sha256:` digest of the exact manifest bytes), `GET /v0/health`, `POST /v0/contributions/connector/fetch` and `/check_credential` |
| Request checks | Request schema → 400 `invalid_request`; undeclared kind → 400 `unknown_kind`; installer configuration, instance config and credential against the declared schemas → 400 `invalid_configuration`, `invalid_config` or `invalid_credential`. All terminal; a credential never appears in the message |
| Errors | `AccessError` → 403, `SourceError` → 422 (terminal), `TransientError(...).WithRetryAfter(d)` → 503 with `retry_after_seconds`, all with `error_class`. `ErrNotDue` → a skipped run with the checkpoint unchanged. Any other error → 503 `transient` `unexpected_error`; a panic → 500 `source` `internal_error`. Details go to the log, never to the envelope |
| Response checks | Before sending: the response schema, `max_items`, `max_response_bytes`, a checkpoint of at most `limits.max_checkpoint_bytes` (64 KiB by default, at most 1 MiB) and diagnostics of at most 16 KiB (500 `invalid_response`). The engine still applies its own item validation |
| Credentials | `Credential` prints `[redacted]` in `fmt`, JSON and `slog`. Its string values are scrubbed from error messages and from `req.Logger()` |
| Deadline | The request context ends at the manifest's `timeout_ms` |

## Checkpoints, items and errors

- **The checkpoint is yours.** Any JSON value that resumes after the returned
  items: a cursor, a timestamp, a set of seen ids. The core stores it and
  sends it back unchanged, and moves it forward only after your items are
  accepted, so a crash never loses items. Keep it under 64 KiB, or declare
  `limits.max_checkpoint_bytes` (at most 1 MiB).
- **Pages.** Return at most `max_items` items and `More: true` to be called
  again at once in the same run, with the checkpoint moved. Use `req.Now`,
  not `time.Now()`, and `req.PageInRun` and `req.ReadsToday` to respect
  source quotas. `req.Connector.CorpusID` and `req.Connector.SourceNamespace`
  (Plugin API 0.3.1) name where the instance writes, to bind Relation targets.
- **Items** carry a stable `RecordKey`, a `Revision` when the source has one
  (the core skips an unchanged revision), text or a Manifest of text Parts
  (`quivrplugin.NewManifest`), extensions in namespaces your manifest
  declares, or `Withdraw: true` for an item deleted at the source. Binary Parts
  are `Attachments` with an opaque `Ref`; the core will ask for their bytes in
  a later Plugin API version.
- **Errors** tell the core what to show operators: `AccessError` for a
  refused credential (health `access_error`), `TransientError` for an outage or
  a rate limit (retried), `SourceError` for data you cannot use.

## Test it

`plugintest` replays a fixture in process, through the same handler the core
talks to:

```go
f, _ := plugintest.Load("fixtures/pages.json")
if err := plugintest.Verify(plugin, f); err != nil { // pages, errors, credential check
	t.Fatal(err)
}
```

A fixture ([schema](../../contracts/plugins/v0/connector-fixture.schema.json))
names the kind, config, credential and starting checkpoint, and what to
expect: the Record Keys of each page, an error class, the credential check.
Use test credentials of 8 characters or more, so the leak check can see them.

Then certify the plugin with the Contract Runner:

```bash
quivr plugin inspect .
quivr plugin test --startup-timeout 120s .   # go run compiles on the first start
```

It runs every fixture page by page, feeding each checkpoint back, resumes from
the final checkpoint, checks credentials, error classes and invalid requests,
and fails when a credential value appears in an answer or in your plugin's
output. CI certifies the sample and publishes its report as the
`go-connector-contract-report` artifact. To run it in Quivr, pin it as in
[Run a connector plugin](../../docs/plugins/run-a-connector-plugin.md).

## Rules

- Import only this SDK and public packages: `make plugin-boundary` fails when
  code under `plugins/` or `sdks/go/` imports Quivr's `internal/` packages or
  requires the engine module.
- The schema copies under `quivrplugin/schemas` come from `contracts/`; run
  `python3 sdks/go/scripts/sync_schemas.py` after a contract change
  (`make test` fails when they are stale).
