# Run a connector plugin

A connector plugin adds a kind of Connector Instance to Quivr without a core
release. The core keeps what must stay safe and uniform: Connector Instances,
schedules, Acquisition Checkpoints, Deposited Credentials and Connector Health.
The plugin only fetches. To write one, follow the [Go plugin kit](../../sdks/go/README.md);
this page is for the operator who pins it.

## Pin it

Add the plugin to `plugins` in the `QUIVR_CONFIG` file of every `api` and
`worker` process, next to your other pins:

```json
{"plugins": [
  {"manifest": "/etc/quivr/plugins/acme-source/quivr-plugin.yaml",
   "endpoint": "https://acme-source.internal:9443",
   "configuration": {}}
]}
```

- **Endpoint.** A connector plugin receives credentials in its request bodies,
  so its endpoint must use `https://`. Plain `http://` is accepted only on a
  loopback address (`127.0.0.1`, `::1`, `localhost`). Plugin calls never follow
  redirects.
- **Kinds.** Every kind the manifest declares under
  `contributions.connector.kinds` becomes available, beside the built-in kinds.
  Each kind has exactly one provider: startup refuses a kind that a built-in or
  another pinned plugin already provides (`kind_conflict`, or a message naming
  both providers).
- **Startup.** The pin is validated without contacting the plugin: an invalid
  manifest or endpoint refuses startup, an unreachable plugin does not.

`GET /v0/connector-kinds` lists the plugin's kinds with the config and
credential schemas from its manifest, so the web interface offers them
without changes. A kind that declares a credential schema needs a credential.

## What the core does on each run

1. At the start of a run whose credential was deposited after the last
   successful poll, the worker asks the plugin to check the credential. A
   refusal ends the run as `access_error` before any fetch.
2. The worker asks the plugin for pages from the Acquisition Checkpoint, at
   most 10 per run. Each call is bounded by the manifest's `timeout_ms`,
   capped at 30 seconds.
3. Each answer is checked like the Contract Runner checks it. Its items go
   through the ingestion path, with the Connector Instance as producer, and only
   then does the checkpoint advance. `reads`, `diagnostics` and `notice` feed
   the usage counters and health as they do for a built-in kind. For an item
   not accepted yet, each attachment is described by the plugin, uploaded by it
   to a presigned PUT the core issues, and read back before the item is
   accepted (Plugin API 0.4). A run starts no new page after 2 minutes.

The Deposited Credential is decrypted in the worker for the run and sent only
in request bodies. It is never logged, and it is never shown by the API.

## Health

| What happens | `health.last_error.code` | Effect |
| --- | --- | --- |
| The plugin does not answer, times out, redirects, or its discovery does not match the pinned manifest | `plugin_unavailable` | Transient: retried at the next interval, nothing lost, the checkpoint stays |
| The plugin reports an access error | the plugin's code | `access_error` until a later run succeeds |
| The plugin reports a transient or source error | the plugin's code | Transient errors wait for `retry_after_seconds` when given |
| The error has no class, or a class contradicting `retryable` | `plugin_invalid_error` | The run ends; fix the plugin |
| The answer breaks the contract | `plugin_invalid_response` | Nothing from the page is accepted |
| The answer contains the credential | `credential_leak` | Nothing from the page is accepted |
| An item has attachments, but the manifest declares no `attachments` | `attachments_unsupported` | Nothing from the page is accepted |
| Storage does not hold the granted bytes after an upload | `attachment_unverified` | The item is not accepted; the checkpoint stays |

The operator guide for Connector Instances is [Connector Instances](../connectors/README.md).
