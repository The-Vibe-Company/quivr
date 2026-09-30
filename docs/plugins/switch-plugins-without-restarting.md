# Switch plugins without restarting

Quivr never starts a plugin. You run each plugin version wherever you like (a
sidecar, a service, a pod) and tell Quivr its address. Quivr records it in the
plugin registry, checks it with the same Contract Runner as `quivr plugin test`,
and, once you activate it, api and worker call it without restarting. Every
route below needs a key with the `plugins:admin` action, which organization keys
never get; request and response shapes are in the
[HTTP API reference](../reference/http-api.md).

## Register, check, activate

1. **Run the new version** at its own address, next to the one in service.
2. **Register it** with `POST /v0/admin/plugins`: the exact text of the
   `quivr-plugin.yaml` it was built from, its endpoint, and the settings you
   would give it in a `QUIVR_CONFIG` pin (`configuration`, `routes`, `kinds`,
   `spaces`). The engine refuses at once, with 422 `invalid_plugin`, what it
   would refuse at startup. The same `idempotency_key` returns the same
   registration.
3. **Wait for the check.** `GET /v0/admin/plugins/{registration_id}` shows
   `registered` while the Contract Runner runs against the endpoint, then
   `validated`, or `rejected` with every check and its issues. Discovery must
   report the digest of the manifest you sent: a plugin built from another
   manifest is rejected. Register again with a new key to check a fixed build.
4. **Activate it** with `POST /v0/admin/plugins/{registration_id}/activate`. The
   answer is the new Pipeline Plan: the registration serves every role it
   declares, the previous version of the same plugin leaves the plan, and so
   does a plugin whose roles it takes over entirely.
5. **Stop the old version** once nothing uses it. Earlier plans stay readable at
   `GET /v0/admin/plugins/plans/{plan_id}`; `GET /v0/admin/plugins/plan` is the
   active one.

An activation that would break a rule the engine applies at startup is refused
with 409 `plugin_conflict`, naming what breaks: one normalizer per media type,
one provider per connector kind (built-in kinds included), one ingestion and
one retrieval plugin, extension namespaces, and vector spaces (an ingestion
plugin's new version may not change a space's model, dimensions or metric
without bumping the space version).

## How api and worker follow the plan

Each process reads the active plan's id every `plugin_plan_poll` (default 2s)
and, when it changed, loads the new plan and swaps it in whole. The api that
served the activation switches at once. A call already in progress finishes on
the plugin it started with; Temporal workers keep running, and only the plugin
resolution changes. Work already started is not yet pinned to its plan: an
activity that begins after the switch uses the new plugin even when its
workflow began before it.

## The startup configuration and the plan

The plugins pinned in `QUIVR_CONFIG` are registered at every start. The
configuration then applies, role by role, only what it changed since it last
applied:

- a role whose configured plugin changed is added, replaced or removed, as a
  new plan recorded from the configuration;
- a role the configuration did not change keeps what the plan says, so an
  activation survives restarts;
- editing the pins of a role wins over an earlier activation of that role, and
  the log names both plugins.

A registry recorded before this rule existed takes the configuration whole on
its first start.

## Not yet

- Alert-rule (subscription) plugins switch through the configuration only:
  a Subscription pins its rule's version.
- An activation cannot add or remove the retrieval role; pin or unpin it in the
  configuration.
- Draining, rollback in one call and backfill come next in the same spec.
