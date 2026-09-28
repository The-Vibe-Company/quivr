# Connector Instances: operator guide

A Connector Instance pulls content from an external source into one Corpus on
a schedule. For example, a newsroom might watch several RSS feeds and a shared
Microsoft 365 mailbox. Collected items enter the engine through the same
ingestion path as `POST /v0/records`, so Record Keys, corrections, Receipts and
the change feed behave exactly as they do for pushed content.

This guide covers what every kind has in common. Each kind gets its own page
here when it ships (`rss`, `microsoft-365`, `x`). Until a kind is delivered, the
API refuses it with `422 unsupported_connector_kind`.

The authoritative request
and response shapes are in the [OpenAPI contract](../../contracts/http/v0/openapi.yaml).
The design rationale is in [ingestion contracts](../quivr-v2-ingestion-contracts.md#pull-acquisition-connector-instances).

Delivered kinds:

- [RSS and Atom feeds (`rss`)](rss.md)
- [Microsoft 365 mailbox (`m365_mail`)](microsoft-365.md)
- [X lists (`x_list`)](x.md)

## Before you start

- **Deployment secret (optional).** `credential_key` (32+ random bytes) in the
  `QUIVR_CONFIG` file of every `api` and `worker` process enables Deposited
  Credentials. On Railway it comes from `QUIVR_CREDENTIAL_KEY`, which
  `deploy/railway/provision.py` generates. Keep it stable and identical on api
  and worker: stored credentials cannot be decrypted without it. Without it the
  core starts and logs `credential deposits disabled`. Instances without a
  credential, such as public RSS, work normally. Any create carrying a
  `credential`, and every rotation, is refused with `503 credentials_unavailable`
  (not retryable) before anything is stored. Instances holding a credential
  sealed under an absent or different key fail runs with `access_error`
  (`credential_unreadable`) until the key is restored or the credential is
  redeposited. Adding, changing or removing the key changes how connector
  requests are fingerprinted for idempotency. A create sent before the change
  and retried after it returns `409 idempotency_conflict` instead of replaying.
- **API key.** Use a key with `connectors:write` (and `connectors:read`) whose
  Corpus scope includes the target Corpus. Add `changes:read` to follow events.
- **Minimum interval.** `connector_min_interval` (default `30s`) is the shortest
  polling interval the deployment accepts.
- **Test kind.** `connector_fixtures: true` enables the deterministic `fixture`
  kind. Use it only in local or CI environments.

## Create an instance

```http
POST /v0/connectors
{
  "idempotency_key": "wire-feed-1",
  "corpus_id": "corpus_…",
  "source_namespace": "wire-feed",
  "kind": "<kind>",
  "config": { … kind-specific, no secrets … },
  "schedule": { "interval_seconds": 300 },
  "health_policy": { "silent_after_seconds": 86400, "credential_warning_seconds": 1209600 },
  "credential": { "secret": { … kind-specific … }, "expires_at": "2027-01-01T00:00:00Z" }
}
```

- **Source Namespace.** It partitions Record Keys. Only one enabled instance
  may own a given Corpus + Source Namespace. If you replace an instance, disable
  the old one first and reuse its namespace, so existing Records keep their
  identity.
- **Validation.** `config` and `credential.secret` are checked against the
  kind's JSON Schema (`422 invalid_config` / `422 invalid_credential`). The error's
  `field` is a JSON Pointer to the rejected member, such as `/config/url`.
- **Discovery.** `GET /v0/connector-kinds` lists the kinds this deployment enables,
  with their config and credential schemas, default intervals, and whether it accepts
  credential deposits at all (`credential_deposits`).
- **Idempotency.** Retrying with the same `idempotency_key` and body returns the
  same instance. A different body under the same key is `409 idempotency_conflict`.
- **First run.** Collection starts right after creation. Later runs follow
  `interval_seconds`, which defaults per kind. At most one run per instance is
  in flight.

## Change the interval

```http
PUT /v0/connectors/{connector_id}/schedule
{ "interval_seconds": 600 }
```

Repeating the same value is harmless. A change commits `connector.schedule_changed`.
A shorter interval brings the next run forward; a longer one applies after the run
already scheduled. The interval must be between the deployment floor and 24 hours
(`422 invalid_interval`). A disabled instance is `409 connector_disabled`.

## Deposit and rotate credentials

- **Storage.** Secrets are encrypted at rest and never returned or logged.
  Responses show only `credential.version`, `deposited_at` and `expires_at`.
- **Rotation.** Rotate by depositing a replacement:

  ```http
  PUT /v0/connectors/{connector_id}/credential
  { "idempotency_key": "rotate-2027-01", "secret": { … }, "expires_at": "…" }
  ```

  The new version applies from the next run.
- **Expiry.** Set `expires_at` whenever the provider's secret expires. Health
  turns `credential_expiring` inside `credential_warning_seconds` (default 14
  days). Past expiry, runs stop with `access_error` (`credential_expired`).

## Watch health

`GET /v0/connectors/{connector_id}` returns `health`:

| State | Meaning | Typical action |
| --- | --- | --- |
| `active` | Collecting normally | none |
| `silent` | No new item for `silent_after_seconds` (default 24 h) | Check whether the source still publishes |
| `access_error` | The source refused access. This lasts until a later successful poll | Fix permissions or rotate the credential |
| `credential_expiring` | The credential expires within the warning window | Rotate before expiry |
| `disabled` | The instance was disabled | none |

- **Other failures.** Timeouts, 5xx responses and rejected items show only in
  `last_error{code, at}`. `last_success_at` and `last_item_at` date the last
  good poll and the last new item. A source rate limit postpones the next run
  until its reset.
- **Usage and diagnostics.** Kinds that read billed or rate-limited resources
  show `health.usage` (`items_read` today and `previous_day_items_read`, UTC
  days). `health.diagnostics` holds kind-specific details described on the
  kind's page.
- **Freshness.** `evaluated_at` shows when health was last committed. If it
  stops advancing, check the worker.
- **Alerting.** Subscribe to `connector.health_changed`, `connector.created`,
  `connector.disabled`, `connector.credential_replaced` and
  `connector.schedule_changed` through
  `GET /v0/changes?corpus_id=…` (or the SSE stream) rather than polling
  instances.

## Disable

`POST /v0/connectors/{connector_id}/disable` with an `idempotency_key` stops
scheduling immediately. Repeating it is harmless. Disabled instances cannot be
re-enabled; create a new instance on the same Source Namespace instead. Records
already collected are unaffected.

## From the web interface

The reference web app (`quivr-search/`) has a **Connecteurs** tab for the Corpus it
serves. There you can:

- **List** instances with their kind, source, interval, health badge, last success,
  last new item, last error and credential version and expiry. Disabled instances are
  listed separately.
- **Create** an instance. First pick a kind, then fill a form generated from that
  kind's schemas (`GET /v0/connector-kinds`). A kind added to the core, including a
  future plugin-provided one, appears with its form and needs no UI change. Settings
  the form cannot render as fields, such as nested lists, get a JSON text box.
- **Open** an instance to see its health and configuration, change its interval,
  deposit or replace its credential, or disable it (after a confirmation).

Health follows the change feed: the page polls `connector.*` events every 5 seconds
and rereads the instances they name, so it updates without a reload. Rejected values
are shown next to the field the API's `field` pointer names.

Secrets typed in the form are sent once, in the submit request, then erased from the
page. They are never shown again: only the credential's version, deposit date and
expiry are. The browser never holds the API key. Every call goes through the web
app's server (`server.mjs`), which keeps the key, holds the session and only accepts
changes coming from the web app's own origin. It also confines connectors to the
Corpus it serves.

What the page offers depends on the deployment:

- If the web app's key lacks `connectors:read`, the page says connectors are not
  enabled on this deployment. For live health, the key also needs `changes:read`;
  otherwise the page refreshes the list every 15 seconds.
- If the deployment has no `credential_key`, the page says credential deposits are
  disabled. Kinds that require a credential cannot be picked, and the others are
  created without one.

The hosted demo turns connectors on with `QUIVR_DEMO_CONNECTORS=1` (see
`deploy/railway/README.md`).

## Behaviour to expect

- **Re-fetched items.** An item fetched again (for example after a restart)
  replays its original Receipt; it never duplicates a Record. A changed item
  becomes a new Record Version.
- **Items gone from the source.** An item that disappears from the source is
  not withdrawn, unless the kind's page says otherwise.
- **Reserved keys.** Idempotency keys starting with `connector:` are reserved.
  Public ingestion requests using them are refused with
  `422 reserved_idempotency_key`.
