# Switch hosted text embeddings with rollback

Run Cohere-Embed-V5-Pro at 1024 dimensions beside core.ingest, fill past documents, then activate it for search. core.ingest keeps its TEI/E5 implementation and vectors for rollback.

## Prerequisites

- The core image includes `quivr-hosted-embed`. The operator controls both Railway api and worker services.
- For PDFs, enable the bundled `pdf-text` normalizer with `QUIVR_DEMO_PLUGINS=1` on both services before ingestion; the hosted plugin embeds normalized text only.
- Set `AZURE_FOUNDRY_ENDPOINT` to the Foundry resource root URL, without `/providers/cohere/v2`, on both services. Store `AZURE_FOUNDRY_KEY` as a Railway secret on both; never put it in configuration or a command argument.
- Set `QUIVR_OPERATOR_KEY` on api. Its generated grant includes `plugins:admin`, `corpora:read`, `operations:read`, `operations:write` and `observability:read`; search uses the separate `QUIVR_API_KEY` configured on api, with `search:query`.
- Have `curl` and `jq`, the internal API address in `QUIVR_API_URL`, and a Corpus id in `CORPUS_ID`. Use the api container's port 8080, for example through `railway ssh --service api`.

These requests are operator examples, not run against a paid deployment. The coordinator runs live steps and confirms the dry-run cost before starting a backfill.

## Enable evaluation

Set `QUIVR_DEMO_HOSTED_EMBED=1` identically on api and worker, then redeploy both. Newly arriving documents then incur hosted embedding calls. Without exactly `1`, no hosted pin or sidecar is added. An enabled service refuses startup if its endpoint or key is empty.

The entrypoint generates a model-locked manifest in `/tmp/hosted-embed/quivr-plugin.yaml` without calling Foundry. The endpoint becomes `base_url` with `/providers/cohere/v2` appended; the plugin appends `/embed`. Only the hosted sidecar receives the key. Both services run it on loopback port 9980.

The initial plan keeps `core.ingest` as default and selects `hosted.embed` for evaluation of inline text (`text/plain`), HTML (`text/html`) and PDF (`application/pdf`). PDF selection uses the original source type after normalization. Other source types keep core.ingest; inspect the deployment's source types before assuming the whole collection is covered.

The plugin requests `search_document` for documents and `search_query` for queries, with `output_dimension: 1024`. Segments use a conservative 6144-byte token bound and 192-byte overlap, rather than an exact provider tokenizer. The manifest declares $0.12 per million input tokens for the cost estimate. Recheck the rate before a paid run.

## Check registration and save the rollback plan

Startup pins register the plugin automatically; do not create a second registration. Inspect its certification and save the plan immediately before promotion.

```sh
curl -fsS "$QUIVR_API_URL/v0/admin/plugins" \
  -H "Authorization: Bearer $QUIVR_OPERATOR_KEY" \
  | jq '.items[] | select(.plugin_id == "hosted.embed") | {registration_id, state, check}'
export REGISTRATION_ID=<the active hosted.embed registration_id>
export SPACE_ID=hosted.embed.cohere-embed-v5-pro-1024-c025d1f04d71722f@1
```

Confirm the space name in the generated manifest. Model or prefix changes require regenerating it and using its new identity.

## Estimate and fill each affected Corpus

List Corpora with `GET /v0/corpora` and follow its pagination. Repeat for every Corpus containing an enabled source type, including empty Corpora. Pick a unique `BACKFILL_KEY` for each Corpus and scope; reuse it for the dry run and start. The dry run estimates without embedding documents.

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

After the coordinator confirms the estimate, send the same body with `"dry_run":false` and `"confirm_cost":true`. Poll `GET /v0/operations/{operation_id}` until `succeeded`; inspect skipped Versions and diagnostics. Use the [backfill guide](https://docs.quivr.thevibecompany.co/plugins/backfill-a-vector-space) for pause, resume, cancel and retry; controlling Operations additionally needs `operations:write`. This step calls Foundry; enabling evaluation also embeds newly arriving documents.

Inspect each Corpus with `GET /v0/corpora/{corpus_id}/vector-spaces`. Require complete hosted coverage of its own segments and eligible current Versions, rather than equal segment counts between owners. Leave served search on core.ingest until coverage and quality pass.

## Compare the same documents

Use the application key for the served request below. Repeat it with `evaluation_plugin` and `evaluation_space` to select the hosted projection. Compare document results in semantic and hybrid modes on the same representative queries, recording which articles should appear and each response's latency.

```sh
curl -fsS -X POST "$QUIVR_API_URL/v0/search" \
  -H "Authorization: Bearer $QUIVR_API_KEY" -H 'Content-Type: application/json' \
  -d @- <<EOF | jq
{"corpus_ids":["$CORPUS_ID"],"query":"<a representative query>",
 "mode":"hybrid","profile":"default","limit":10}
EOF
```

For the evaluation request, add `"evaluation_plugin":"hosted.embed","evaluation_space":"$SPACE_ID"`. Queries call Foundry. Record comparison results before promotion; public benchmark gains alone do not establish this Corpus's quality. Hybrid search keeps core.retrieve's alpha 0.5 and relative-score default. Tune fusion separately through the engine's evaluation lane using Weaviate BM25F and its tokenization; an offline lexical approximation cannot establish the engine's best weight.

## Promote and verify

```sh
curl -fsS "$QUIVR_API_URL/v0/admin/plugins/plan" \
  -H "Authorization: Bearer $QUIVR_OPERATOR_KEY" | jq '.plan_id'
export ROLLBACK_PLAN_ID=<the plan_id above>
curl -fsS -X POST "$QUIVR_API_URL/v0/admin/plugins/$REGISTRATION_ID/activate" \
  -H "Authorization: Bearer $QUIVR_OPERATOR_KEY" | jq '{plan_id, roles}'
```

Activation of the evaluation owner switches its selected source formats together; it refuses incomplete coverage. Use this activation, rather than `/admin/spaces/{id}/promote`, which switches spaces within one owner. Inspect plan roles and repeat ordinary searches without evaluation fields. Hits should name the hosted space. Keep both sidecars running: core.ingest becomes an evaluation owner and fills new Versions for rollback.

Identical startup pins preserve the operator's active plan across restarts. Changing the endpoint, model configuration, or evaluation switch changes the startup pins and can reconcile affected roles. Inspect the active plan after every redeploy; keep api and worker variables identical.

## Roll back

```sh
curl -fsS -X POST "$QUIVR_API_URL/v0/admin/plugins/plan/rollback" \
  -H "Authorization: Bearer $QUIVR_OPERATOR_KEY" -H 'Content-Type: application/json' \
  -d @- <<EOF | jq '{plan_id, roles}'
{"idempotency_key":"<a unique rollback key>","plan_id":"$ROLLBACK_PLAN_ID","pinned_work":"stop"}
EOF
```

Verify core.ingest serves the selected formats and repeat ordinary searches. Rollback checks the returning owner's complete coverage; if new Versions lack E5 vectors, backfill core.ingest before retrying. Keep the hosted switch enabled during this drill so both implementations remain reachable. `stop` stops outgoing pinned work after processes follow the plan; use `drain` if that work should finish. After rollback is verified, disabling the switch on both services and redeploying removes hosted evaluation; first drain work that still needs it. Check the hosted registration's `pinned_work` at `GET /v0/admin/plugins` until zero before removing its sidecar.

## Next

See [hosted.embed](../../plugins/hosted-embed/README.md) for provider configuration, and [switch plugins](https://docs.quivr.thevibecompany.co/plugins/switch-plugins-without-restarting) for activation and rollback contracts.
