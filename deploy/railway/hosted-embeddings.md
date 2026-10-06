# Switch hosted text embeddings with rollback

Run Cohere-Embed-V5-Pro at 1024 dimensions as the default ingestion for every source format. Rebuild existing Corpora for search. core.ingest stays reachable for historical work and rollback.

## Prerequisites

- The core image includes `quivr-hosted-embed`. The operator controls both Railway api and worker services.
- For PDFs, enable the bundled `pdf-text` normalizer with `QUIVR_DEMO_PLUGINS=1` on both services before ingestion; the hosted plugin embeds normalized text only.
- Set `AZURE_FOUNDRY_ENDPOINT` to the Foundry resource root URL, without `/providers/cohere/v2`, on both services. Store `AZURE_FOUNDRY_KEY` as a Railway secret on both; never put it in configuration or a command argument.
- Set `QUIVR_OPERATOR_KEY` on api. Its generated grant includes `plugins:admin`, `corpora:read`, `projections:rebuild`, `operations:read`, `operations:write` and `observability:read`; search uses the separate `QUIVR_API_KEY` configured on api, with `search:query`.
- Have `curl` and `jq`, the internal API address in `QUIVR_API_URL`, and a Corpus id in `CORPUS_ID`. Use the api container's port 8080, for example through `railway ssh --service api`.

These requests are operator examples, not run against a paid deployment. The coordinator runs live steps and confirms the dry-run cost before starting a backfill.

## Enable hosted ingestion

Set `QUIVR_DEMO_HOSTED_EMBED=1` identically on api and worker, then redeploy both. Newly arriving documents then incur hosted embedding calls. Without exactly `1`, no hosted pin or sidecar is added. An enabled service refuses startup if its endpoint or key is empty.

The entrypoint generates a model-locked manifest in `/tmp/hosted-embed/quivr-plugin.yaml` without calling Foundry. The endpoint becomes `base_url` with `/providers/cohere/v2` appended; the plugin appends `/embed`. Only the hosted sidecar receives the key. Both services run it on loopback port 9980.

The plan selects `hosted.embed` as the default for every source format, including NewsML-G2 XML, after normalization. It has no E5 evaluation routes, saving CPU for new hosted Versions. Existing Corpora keep their served generation until rebuilt, so they can still need E5 while transitioning. Keep core.ingest and TEI running.

The plugin requests `search_document` for documents and `search_query` for queries, with `output_dimension: 1024`. Segments use a conservative 6144-byte token bound and 192-byte overlap, rather than an exact provider tokenizer. The manifest declares $0.12 per million input tokens for the cost estimate. Recheck the rate before a paid run.

## Check registration and save the rollback plan

Before redeploy, read `GET /v0/admin/plugins/plan` with the operator key and save
its `plan_id` as `ROLLBACK_PLAN_ID`. Startup pins register the plugin automatically; do not create a second registration. Inspect its certification after redeploy.

```sh
curl -fsS "$QUIVR_API_URL/v0/admin/plugins" \
  -H "Authorization: Bearer $QUIVR_OPERATOR_KEY" \
  | jq '.items[] | select(.plugin_id == "hosted.embed") | {registration_id, state, check}'
export REGISTRATION_ID=<the active hosted.embed registration_id>
export SPACE_ID=hosted.embed.cohere-embed-v5-pro-1024-c025d1f04d71722f@1
```

Confirm the space name in the generated manifest. Model or prefix changes require regenerating it and using its new identity.

## Estimate and fill each affected Corpus

Backfill is for gaps in an eligible owner's projection. Selecting a registration
alone does not convert Versions served by another owner without corresponding
source evaluation assignments. For an E5-only Corpus, use [Rebuild and verify](#rebuild-and-verify)
to re-run ingestion with the hosted default. A zero dry-run count is not evidence
of complete hosted coverage. The requests below apply when hosted Versions are
already in backfill scope; they do not switch the search owner.

List Corpora with `GET /v0/corpora` and follow its pagination. Repeat for every Corpus, regardless of source type, including empty Corpora. Pick a unique `BACKFILL_KEY` for each Corpus and scope; reuse it for the dry run and start. The dry run estimates without embedding documents.

```sh
export BACKFILL_KEY=<a unique key for this Corpus and fill>
curl -fsS -X POST "$QUIVR_API_URL/v0/admin/backfills" \
  -H "Authorization: Bearer $QUIVR_OPERATOR_KEY" -H 'Content-Type: application/json' \
  -d @- <<EOF | jq
{"idempotency_key":"$BACKFILL_KEY","corpus_id":"$CORPUS_ID",
 "registration_id":"$REGISTRATION_ID","spaces":["$SPACE_ID"],"dry_run":true}
EOF
```

Record `versions`, `segments`, `input_tokens`, `estimated_seconds`, `duration_basis`, `estimated_cost_usd` and `confirmation_required`. Duration uses the configured rate or recent throughput when slower. For an owner without segments, the estimate uses the existing owner's cuts. Treat it as an estimate; the hosted window can produce different cuts. At the default rate of 2 Versions/s, 1000 Versions take at least about 8.3 minutes; provider, storage and serial processing can take longer. Use `estimated_seconds` and measured progress for this Corpus's expectation.

After the coordinator confirms the estimate, send the same body with `"dry_run":false` and `"confirm_cost":true`. Poll `GET /v0/operations/{operation_id}` until `succeeded`; inspect skipped Versions and diagnostics. Use the [backfill guide](https://docs.quivr.thevibecompany.co/run-quivr/backfill-a-vector-space) for pause, resume, cancel and retry; controlling Operations additionally needs `operations:write`. This step calls Foundry; enabling hosted ingestion also embeds newly arriving documents.

Inspect each Corpus with `GET /v0/corpora/{corpus_id}/vector-spaces`. Compare hosted `coverage.segments` with its own `coverage.total_segments` and check `coverage.versions_covered` for eligible current Versions. Different models can cut different numbers of segments.

## Rebuild and verify

Changing startup ingestion routing preserves each existing Corpus's search generation.
Backfill does not change its serving owner. For each existing Corpus, rebuild under
the hosted default: it re-runs segmentation
and embedding of existing Versions with `hosted.embed`. A rebuild prepares a
new search generation and switches to it only after it is ready; it can call Foundry.
These requests are operator examples, not run against a paid deployment.

```sh
curl -fsS -X POST "$QUIVR_API_URL/v0/corpora/$CORPUS_ID/rebuilds" \
  -H "Authorization: Bearer $QUIVR_OPERATOR_KEY" -H 'Content-Type: application/json' \
  -d '{"idempotency_key":"<a unique rebuild key>"}' | jq
```

Poll `GET /v0/operations/{operation_id}` until `succeeded`. Inspect
`GET /v0/corpora/{corpus_id}/vector-spaces` again and repeat ordinary searches without
evaluation fields: hits must name the hosted space. Keep both sidecars running for
old work; new hosted generations have no E5 evaluation route. Do not use space
promotion to change owners: it only switches models within one owner.

## Redeploy after the switch

Keep api and worker variables identical. A new build with the same plugin id, version, endpoint, installed settings and contribution contracts automatically replaces the current registration in a new plan, preserving the hosted default without adding E5 evaluation. Configuration-schema changes are allowed if the unchanged settings still validate. A change to the model, vector-space declaration or other contribution contract follows normal startup reconciliation instead; inspect the active plan and search after every redeploy.

Earlier plans still name their exact build. Work already started never moves to the replacement: keep the earlier build reachable until its `pinned_work` is zero. For an upgrade that must drain live work, run builds at separate addresses and use the [upgrade guide](https://docs.quivr.thevibecompany.co/run-quivr/upgrade-a-plugin).

## Roll back

New hosted Versions have no E5 vectors. Saved-plan rollback can refuse missing E5 coverage with `409 plugin_conflict` or a replaced build with `409 plugin_unreachable`. Selecting E5 in a backfill does not convert hosted-only Versions: the source-scope restriction applies.

For a complete return to E5, keep both plugins reachable while changing the startup default to `core.ingest`, then rebuild every Corpus with the procedure above and verify E5 coverage and ordinary searches. For example, not run: stage a deployment
configuration that retains the hosted pin and sidecar but sets
`ingestion: {default: core.ingest}` without evaluation routes. The current switch
alone removes hosted reachability when disabled, so do not disable it on a Corpus
that still searches the hosted generation or has hosted pinned work. Once E5
rebuilds have succeeded and hosted `pinned_work` is zero, unset the hosted switch on
both services and redeploy. Keep api and worker configured with the same default.

## Next
See [hosted.embed](../../plugins/hosted-embed/README.md) for provider configuration, and [switch plugins](https://docs.quivr.thevibecompany.co/plugins/switch-plugins-without-restarting) for activation and rollback contracts.
