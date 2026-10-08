# Railway evaluation demo

## Local demo first

`make demo` runs the same web UI against a real local stack at http://127.0.0.1:5183.
It keeps its own data across restarts; `make demo-reset` deletes only that demo's data
and `make verify-demo` runs the browser checks. See
[`quivr-search/README.md`](../../quivr-search/README.md) for prerequisites, frontend
development and the optional shared password.

## Hosted deployment

This deployment runs the [`quivr-search`](../../quivr-search/) UI and V2 core in a dedicated Railway project.
Only `web` is exposed publicly; bulk workers can [scale on backlog](autoscaler/README.md).

| Service | Runtime / responsibility | Persistence |
| --- | --- | --- |
| web | Node facade, static React bundle, shared-password session | Stateless |
| api | Go HTTP API; applies migrations before serving | PostgreSQL/S3 |
| worker | Live Go/Temporal processing, fixed at one replica | PostgreSQL/S3/Temporal |
| worker-bulk | Bulk Go/Temporal processing, one to eight replicas with autoscaling | PostgreSQL/S3/Temporal |
| postgres | Pinned PostgreSQL 17 | `/data/pgdata` on `/data` volume |
| seaweed | Pinned SeaweedFS mini, authenticated S3 | `/data` volume |
| temporal | Pinned Temporal dev server, headless | SQLite `/data/temporal.db` volume |
| weaviate | Pinned standalone search projection | `/var/lib/weaviate` volume |
| tei | Pinned CPU E5 inference | Model baked into image; derived artifacts in S3 |

This evaluation deployment uses the Temporal dev server and single-replica dependencies without high availability.
Redeploying a volume-backed service can interrupt requests. No public dependency domains are needed.

## Size PostgreSQL before importing

The template builds `postgres.Dockerfile` and applies the [shared startup settings](../compose/README.md):
256 server connections, memory-based buffers/cache, bounded maintenance memory,
`dynamic_shared_memory_type=mmap`, `pg_stat_statements` and I/O timing.
WAL budgets scale with the data volume up to 32 GiB/4 GiB, with 15-minute checkpoints
and LZ4 compression. Set `QUIVR_POSTGRES_VOLUME_MB` on postgres if the mount reports
host capacity instead of its quota; the shared guide covers overrides and trade-offs.
`synchronous_commit`, `fsync` and `full_page_writes` stay on for durable acknowledged writes.
`jit=on` is retained: a local A/B run of the updated backlog query did not trigger JIT at default thresholds.
These settings are **command-line options**: they override old `ALTER SYSTEM` values
in `postgresql.auto.conf` on existing volumes. Removing an option restores config-file precedence.
Fresh databases install the statistics extension; an existing database needs `CREATE EXTENSION IF NOT EXISTS pg_stat_statements` once.

On each API/worker service, `QUIVR_POSTGRES_MAX_CONNECTIONS` sets the generated
`postgres.max_connections` pool limit (default 16). On the **postgres** service,
the same variable sets the server limit (default 256). Usable server connections
must cover the **sum of pool sizes across all replicas**, other clients and
rolling-deployment headroom, after subtracting PostgreSQL's reserved connections.
One API, one live worker and eight bulk workers can use `10 × 16 = 160` connections.
Choose memory and connection budgets together; worker activity slots are separate.

## Upgrade the search database

The template pins Weaviate 1.39.10 by digest. Existing 1.37.15 data needs no
Quivr rebuild or re-ingestion when following the [upstream upgrade route](https://docs.weaviate.io/deploy/migration):
back up the volume, pause writes, upgrade one minor at a time, then resume writes.
Keep the same `/var/lib/weaviate` volume and `CLUSTER_HOSTNAME` at every step.
Before each clean shutdown, check `/v1/.well-known/ready` and
`/v1/cluster/statistics`: there must be one node and top-level `synchronized: true`.
Stop if a deployment is killed or runs out of memory instead of shutting down cleanly.

For a 1.37 deployment, temporarily set the service image to
`cr.weaviate.io/semitechnologies/weaviate:1.38.20@sha256:d23d7bb6242026106ee1ec5aefdbb6a867e260ff171cedc9c3af55c9688e30f6`
and redeploy. Wait for readiness and synchronized metadata, then deploy the
1.39.10 image from `services.json` on that same volume. Check `/v1/meta` for the
expected version and exercise lexical, semantic and hybrid search before resuming
writes. Recover from the pre-upgrade backup if needed; do not assume a newer
volume can be downgraded safely. Preview drop-vector-index functionality in
1.39 is not enabled by this template. Index types and compression are unchanged.

Set startup variables on the **weaviate** service for the installation's resources:

| Variable | Purpose |
| --- | --- |
| `RAFT_BOOTSTRAP_TIMEOUT` | Seconds allowed to bootstrap/rejoin while loading the database. The pinned 1.39.10 default is **600 s**; increase it if index loading needs longer, and allow the deployment's startup deadline to cover it. |
| `GOMEMLIMIT` | Go runtime soft memory limit, for example `3GiB` on a 4 GiB service. Choose about 80–90% of the service memory to leave headroom; this does not bound total RSS or make an oversized index fit. |

The [pinned configuration source](https://github.com/weaviate/weaviate/blob/v1.39.10/usecases/config/environment.go)
still honors deprecated `HNSW_STARTUP_WAIT_FOR_VECTOR_CACHE`; leave it unset so
shard loading determines prefill behavior. `ASYNC_INDEXING_BATCH_SIZE` was removed.
The 1.38–1.39 release-note review found no required Quivr schema migration:
1.39.1's auto-schema named-vector default does not apply because Quivr disables
auto-schema and declares named vectors explicitly. See [memory sizing](https://docs.weaviate.io/weaviate/concepts/resources).

Webhook delivery and the RSS connector refuse private and internal addresses
(checked after DNS resolution, so Railway's private network is unreachable
through them). The generated configuration never sets
`delivery.allow_private_destinations` or the rss pin's `allow_private_addresses`;
operators may enable them for trusted internal receivers or feeds, accepting
the SSRF exposure. The local harness enables them for loopback fixtures.

The generated configuration also sets `observability.record_query_text`, off by default in
the engine, so the admin view can list the most frequent searches: query text is stored,
lowercased and cut to 200 characters, for 7 days. Remove it where queries must not be kept.

The RSS connector is the first-party plugin [`plugins/rss`](../../plugins/rss/README.md),
built into the core image as `/usr/local/bin/quivr-rss`. `core-entrypoint.py` always
pins it and the worker always runs it on `127.0.0.1:9920`, whatever `QUIVR_DEMO_PLUGINS`
says, so feed instances keep polling. It logs `quivr-rss: serving connector.rss@…` at start.

The core image includes the [`NewsML-G2 normalizer`](../../plugins/newsml-g2/README.md), pinned by api and worker
with required routes for `application/vnd.iptc.g2.newsitem+xml` and `application/vnd.iptc.g2.newsmessage+xml`.
The worker runs it on `127.0.0.1:9905`, regardless of `QUIVR_DEMO_PLUGINS`: the always-pinned archive connector needs it.

## Credential key (optional)

The demo runs without `QUIVR_CREDENTIAL_KEY`. Ingestion, search and connectors that
need no credential, such as public RSS feeds, work normally. api and worker each log
`credential deposits disabled` once at startup. Credentialed connectors (Microsoft 365
mail, X lists, authenticated RSS) are then refused: creating one with a `credential`,
or rotating a credential, returns `503 credentials_unavailable`.

To enable them later, set `QUIVR_CREDENTIAL_KEY` (32+ random bytes) to the SAME value
on both api and worker, then restart api and worker. No migration is needed. The one
side effect is that adding, changing or removing the key changes how connector
requests are fingerprinted for idempotency. A connector create sent before the change
and retried after it returns `409 idempotency_conflict` instead of replaying. Keep the
value stable afterwards: changing or removing it also makes stored credentials
unreadable (`access_error` / `credential_unreadable`) until they are deposited again. `provision.py` generates this value by default.

## Operator key (optional)

`QUIVR_OPERATOR_KEY` on api adds a second key with `projections:rebuild`, `plugins:admin`, `operations:write` and
`observability:read` and `queues:read`; the web app never gets administration or queue grants. Use it from inside the deployment
(`railway ssh --service api`, port 8080) to rebuild a Corpus projection, to follow documents through
their steps (`GET /v0/admin/documents`), or to register and activate plugins ([Switch plugins without restarting](https://docs.quivr.thevibecompany.co/plugins/switch-plugins-without-restarting)); a redeploy that changes the plugin pins applies them, even over an earlier activation of the same role. `QUIVR_DEMO_CORE_INGEST=0` on api, worker and worker-bulk drops the E5 `core.ingest` plugin: set it only after every Corpus has been rebuilt onto the hosted space and `core.ingest` pinned work has drained to zero, since generations still serving E5 need it for semantic queries. `QUIVR_REBUILD_CONCURRENCY` on worker sets how many Versions a rebuild step covers in parallel (1–256, default 8); raise it when a large Corpus rebuild is slow while PostgreSQL and Weaviate stay idle.
`QUIVR_QUEUE_KEY` adds a distinct, read-only queue key for the [autoscaler](autoscaler/README.md). `QUIVR_DEMO_ADMIN=1` on api gives the web app's key `observability:read`, which turns on the web app's read-only **Admin** tab (live flow of documents, timelines, throughput).

## Core plugins and the rebuild after THE-777

api and worker run the core.ingest plugin (`127.0.0.1:9950`, `CONNECTORS` in `core-entrypoint.py`),
which segments and embeds with TEI; the api alone runs core.retrieve (`127.0.0.1:9960`), which
ranks every search (THE-779). Neither starts without its plugin. After the first deploy with core.ingest,
rebuild each earlier Corpus once with the operator key (later ones start on core.ingest):
`POST /v0/corpora/{corpus_id}/rebuilds` with an `idempotency_key`, then poll the Operation.
Until then search works in every mode and new articles are searchable by keyword at once;
their vectors attach at the rebuild, which re-embeds with the same model, so results hold.

## EmbeddingGemma 2 on Modal (selected demo default)

The coordinator deploys a pinned text-only `google/embeddinggemma-2` service on
Modal L4 and selects it with `QUIVR_DEMO_EMBEDDING=gemma` on api and worker.
`EMBED_URL` is the deployed HTTPS origin; `EMBED_API_KEY` is its bearer secret.
`hosted.embed` uses 768 dimensions and Gemma's search/document prefixes.
Follow the [Modal rollout guide](../modal/README.md) to create the secret,
run `modal deploy`, rebuild every Corpus, check coverage/search and roll back
with `QUIVR_DEMO_EMBEDDING=cohere` and retained Foundry endpoint/key variables.
The hosted sidecar sends up to 16 concurrent provider requests of at most 32 inputs; on the demo deployment, throughput saturated near 140 passages per second from 16 requests. Provider concurrency is execution tuning, so changing it keeps the ingestion recipe and needs no rebuild.
Model changes need a maintenance window while old generations rebuild. Real GPU latency/throughput remain for coordinator
validation; this change's measurements use fake inference only.

## Optional CPU query encoding

Build the core image with Docker build argument `QUIVR_BUILD_LOCAL_QUERY_ENCODER=1`
to include the pinned fp32 Sentence Transformers text runtime. The default `0`
includes neither CPU wheels nor model weights. The build verifies every file
against `third_party/query-encoder/model-lock.json`; runtime loading stays offline.
The model revision, 768 dimensions, input prefixes and stored vector space stay
unchanged. No Corpus rebuild is needed for this execution change.

Start with an API allocation of **4 CPU cores and 4 GB RAM**, then measure actual
memory and latency. Text-only fp32 weights occupy about 1.1 GB of RAM, with
additional runtime, tokenizer and activation memory. The baked snapshot is about
1.5 GB because its weight file includes disabled modality encoders. Workers do
not load the model.

Set `QUIVR_LOCAL_QUERY_ENCODER=1` on the API only, while retaining
`QUIVR_DEMO_EMBEDDING=gemma`, `EMBED_URL` and `EMBED_API_KEY` on API and workers.
`QUIVR_QUERY_ENCODER_THREADS` defaults to `4` (range `1..32`).
`QUIVR_QUERY_ENCODER_STARTUP_SECONDS` defaults to `120` (range `1..600`). The API
waits for offline loading, warm-up and matching readiness metadata before starting
its other processes. A missing optional image, mismatched model, early encoder
exit or startup timeout refuses startup. Termination also stops the encoder.

The encoder binds `127.0.0.1:9995`, runs one inference at a time and bounds queued
work. Queries receive a one-second total plugin deadline, no remote retry or
fallback, and no remote bearer credential. Saturation or an expired request returns
an error. Document requests still use the remote provider, batching and retries.
API and worker pins remain identical; the local URL exists only in the API's
hosted plugin environment. The text service accepts already-prefixed document
input for future reuse, but this deployment option never routes documents locally.

Before rollout, the coordinator runs `scripts/eval/query_encoder_parity.py` through
its bounded job runner, with the remote bearer key supplied as `EMBED_API_KEY`.
The input JSON contains `queries` (at least 200 distinct `{id, text}` objects)
and `candidates` (at least ten frozen `{id, vector}` document vectors from the
remote model). Keep private data and reports in an ignored directory. For a
measurement through the hosted plugin, also include `plugin.configuration` and
`plugin.space`, taken from that deployment's installed pin. Supply that plugin's
verification ring privately as `QUIVR_PLUGIN_SIGNING_KEYS`; the CLI signs each
request with a fresh invocation ID and never writes keys or tokens to reports.
Use a numeric loopback HTTP origin or HTTPS for remote plugin calls.

Install the CLI numerical dependency with `python3 -m pip install numpy==2.5.3`.
Candidate vectors are normalized once and ranked with float64 matrix operations.

Example command, not run against a live provider here:

```sh
python3 scripts/eval/query_encoder_parity.py \
  --input .scratch/query-parity/input.json --output .scratch/query-parity/report.json \
  --local-url http://127.0.0.1:9995/v1 --remote-url "$EMBED_URL/v1" \
  --plugin-url http://127.0.0.1:9980 --cpu-cores 4 --threads 4 --concurrency 4
```

The CLI records input digest, query length distribution, CPU/thread/concurrency
settings, first-query timing, cosine similarity, ordered top-10 agreement, and
sequential/concurrent p50/p95. It exits unsuccessfully for cosine below `0.999`,
a ranking mismatch, incomplete evidence, errors or local/plugin p95 of `100 ms`
or more. A passing direct-encoder report leaves demo acceptance unmeasured:
require the plugin-boundary comparison and record the deployed demo's
`query_encoding` p50/p95 and cold-start/error observations on the ticket too.
If fp32 misses the latency target, stop activation and report the numbers; do not
substitute a quantized model or fp16. Unset `QUIVR_LOCAL_QUERY_ENCODER` and redeploy
the API to restore remote queries. Keep the remote document service available
throughout rollout and rollback.

## Hosted Azure embeddings for every source (optional)

Set `QUIVR_DEMO_HOSTED_EMBED=1`, `AZURE_FOUNDRY_ENDPOINT` (Foundry resource root)
and the secret `AZURE_FOUNDRY_KEY` identically on api and worker. For existing Corpora,
save the plan and restore it if needed before redeploying; finish step 1's checks afterward.
The default ingestion is `hosted.embed`: Cohere-Embed-V5-Pro, 1024 dimensions, for every source format after normalization, including NewsML-G2 XML.
PDFs still need `QUIVR_DEMO_PLUGINS=1` for `pdf-text`. Other switch values keep E5.
The generated config has no E5 evaluation route, saving CPU after plan verification. Keep core.ingest and TEI reachable for historical generations and already-pinned work.

Each hosted sidecar uses `batch_size=32`, `max_concurrent_requests=16` and `max_batch_tokens=196608` (32 × the unchanged 6144 segment bound). The document queue admits up to 256 document subrequests per process (`min(256, max_concurrent_requests × batch_size)`). These settings preserve `hosted.embed.cohere-embed-v5-pro-1024-c025d1f04d71722f@1`, so tuning them needs no Corpus rebuild.
Concurrency 16 is half the plugin maximum: at roughly one second per call it permits about 960 requests/minute per process. [Foundry's documented defaults](https://learn.microsoft.com/en-us/azure/foundry/foundry-models/quotas-limits) for other models are 1000 requests/minute, 400000 tokens/minute and 300 concurrent requests; check the actual deployment allowance and combined api/worker load. Concurrency does not enforce a token or request rate. The plugin shares a 429 `Retry-After` cooldown across its process, falls back to bounded exponential backoff, and retries twice; lower concurrency if throttling persists.
Redeploy both services together. Changed settings create a new registration and apply startup routing, retaining this deployment's configured hosted default. Inspect the active plan afterward if you added operator routes: compatible build replacement preserves those overrides only when settings are unchanged. Already-pinned work and historical plans keep their exact registrations; saved plans can require restoring the old settings for rollback.

Existing Corpora keep their old search generation after redeploy and can still
require E5 until rebuilt. Use the operator key inside api (port 8080), with
`plugins:admin`, `corpora:read`, `projections:rebuild`, `operations:read` and
`operations:write`. These are operator examples, not run against the paid provider:

1. Save `GET /v0/admin/plugins/plan` before redeploy for rollback. If earlier hosted
   promotions added E5 evaluation, restore the saved pre-promotion plan first:
   startup preserves operator-created routes. After redeploy, verify the active
   plan has `ingestion-default` on `hosted.embed`, no `ingestion-evaluation:*:core.ingest`
   and no source routes to E5. Inspect `GET /v0/admin/plugins` and list every Corpus
   with `GET /v0/corpora`, following pagination.
2. Inspect `GET /v0/corpora/{corpus_id}/vector-spaces`. Re-index each existing
   Corpus with `POST /v0/corpora/{corpus_id}/rebuilds` and a fresh `idempotency_key`.
   The rebuild re-runs segmentation and embedding of existing Versions with the
   active default, `hosted.embed`, calling the paid provider. Search keeps its old
   generation until the prepared hosted generation is ready. Poll the Operation
   at `GET /v0/operations/{operation_id}` until `succeeded`; inspect failures.
3. Recheck hosted `coverage.segments` against its own `coverage.total_segments`
   and confirm `coverage.versions_covered` includes every eligible current Version.
   Run semantic/hybrid searches with `profile: default`: hits must name the hosted
   space without evaluation fields. The default profile uses the served vectors.

Backfill cannot convert an E5-only Corpus without hosted source assignments.
For eligible hosted gaps, use `POST /v0/admin/backfills`: `dry_run: true`, inspect
cost, then resend with `dry_run: false` and `confirm_cost: true`. A zero estimate
does not prove coverage. The [hosted guide](hosted-embeddings.md) gives full request
bodies and rollback precautions: new hosted Versions have no E5 rollback vectors.

## Jev deep searches (optional)

The core image includes [`jev.rerank`](../../plugins/jev-rerank/README.md).
To enable it, set `QUIVR_DEMO_JEV_RERANK=1` identically on api and worker,
then redeploy both. Set `TYPESAFE_API_KEY` on api for paid re-ranking;
without it Jev returns the hybrid fallback. Both reconcile the shared plugin
registry at startup; matching pins keep a worker restart from undoing the choice.
Enable it after measuring retrieval quality for the
deployment. It runs at `127.0.0.1:9970` beside core.retrieve; the worker runs neither
retrieval sidecar. This switch is independent of `QUIVR_DEMO_PLUGINS`.
The key goes into the Jev sidecar environment, never its pin or the web bundle;
alerts also receives it when separately enabled.

The configured aliases route `default` to `core.retrieve/default` and `deep`
to `jev.rerank/deep`; `GET /v0/search/profiles` lists both providers.
`profile: default` keeps the requested index ranking and makes no paid call.
`profile: deep` re-ranks 30 hybrid candidates with Noul, trims passages to 256
tokens using core.ingest's pinned E5 tokenizer, and caches up to 4096 pairs.
An uncached deep search is estimated at about **0.06 cent**, with a **1-cent
maximum** including retries; actual cost depends on provider input tokens.
Provider failures return hybrid order with an explicit unavailable explanation.
See [Re-rank with Jev](https://docs.quivr.thevibecompany.co/run-quivr/rerank-with-jev)
for usage reporting, caching and fallback behavior.

To turn it off, unset `QUIVR_DEMO_JEV_RERANK` or set it to `0` on api and worker, then redeploy both.
Only core.retrieve remains pinned for retrieval. No migration or projection rebuild is needed.

## Connectors in the web app (optional)

The web app has a **Sources** view (THE-679, THE-732). By default the demo key cannot use
it, so the view shows "Les connecteurs ne sont pas activés sur ce déploiement". To
enable it, set `QUIVR_DEMO_CONNECTORS=1` on api and worker, then redeploy them. The
demo key then also gets `connectors:read` and `connectors:write`; it always has
`changes:read`, which the live health and the **Veille** feed use. Without `QUIVR_CREDENTIAL_KEY` only credential-free kinds, such as
public RSS, can be created, and the view says so. Unset the variable and redeploy to
turn it off again. Instances created meanwhile keep polling; pause or remove them
first from the view if they should stop.

Optional web variables for the Sources view (see `quivr-search/README.md`):
`DEMO_FEED_SUGGESTIONS` sets the one-click suggested feeds (JSON array of
`{"title", "url"}`), and `DEMO_STATE_FILE` keeps the list of removed sources on a
volume. Without a volume, removed sources come back as paused after a web restart.
Set the real feed list in the Railway variables, never in this repository.

X lists in webhook mode ([guide](https://docs.quivr.thevibecompany.co/guides/x#real-time-mode)) need the API's public
address: set `QUIVR_PUBLIC_URL` (for example `https://<api domain>`) on api and
worker. The API service then runs the x-list plugin beside itself to relay X's
deliveries; without the variable, X lists only poll.

## Keyword alerts and PDF text (optional)

The core image bakes in [`alerts`](../../plugins/alerts/README.md), which the **Alertes** tab needs, and [`pdf-text`](../../plugins/pdf-text/README.md).
Set `QUIVR_DEMO_PLUGINS=1` on api and worker, then redeploy them. `core-entrypoint.py`:

- pins pdf-text on `127.0.0.1:9900` (`application/pdf`) and alerts on `127.0.0.1:9910`; worker runs both, api also runs alerts for previews. A sidecar exit stops its container for Railway to restart;
- enables external [described alerts](https://docs.quivr.thevibecompany.co/guides/described-alerts) when `TYPESAFE_API_KEY` is the same on api and worker; alerts receives it, as does the API's Jev sidecar when enabled. Set `DEMO_DESCRIBED_ALERTS=true` on web to offer them;
- grants the demo key `monitoring:read` and `monitoring:write`.

The web app creates alerts without a delivery destination and reads Matches through the API. No webhook receiver or signing secret is needed.

The worker logs its actual versions in `plugins pinned`. Without a web `DEMO_STATE_FILE` volume, paused alerts leave the list after a web restart; active ones are found through the API.

## Upgrade a bundled alert rule without losing alerts

Use an operator key with `plugins:admin` and the relevant Corpus grants. Call the admin API from inside the deployment; `web` never gets this permission.

1. Keep the exact old implementation A running at a reachable endpoint while starting B at another endpoint. A new image that only contains B cannot serve A: retain and run A's old code separately. Never relabel B as A.
2. [Register, certify and activate B](https://docs.quivr.thevibecompany.co/plugins/switch-plugins-without-restarting#upgrade-an-alert-rule). Dry-run `POST /v0/admin/subscriptions/evaluator-migrations`, inspect refusals, then migrate in pages. New Subscription Versions apply only to future changes; queued pins stay on A.
3. Keep A reachable until its registry `subscriptions` and `pinned_work` are both zero, including undispatched old triggers. Only then remove A's endpoint or image.

If A has already disappeared, its missing-evaluator retries continue unchanged. Restore A for zero-loss recovery, or intentionally abandon potential Matches with `POST /v0/admin/subscriptions/evaluation-retirements`.
Follow the [bounded retirement procedure](https://docs.quivr.thevibecompany.co/run-quivr/retire-alert-evaluations): inspect `GET /v0/admin/subscriptions/evaluation-backlog`, dry-run first, wait for leases and dispatch, then repeat real batches with new keys. Retirement records `evaluator_retired`, never `no_match`, and creates no Match or Delivery.

## Provision and deploy

In-place upgrades from 2.0.0-alpha.6 and older are not supported. Reset the installation, or export its data and re-import it into a fresh installation before following these steps.

Authenticate `railway login`, then create/link a dedicated project in the intended
workspace. The provisioner refuses any project not named `quivr-v2-demo` or whose ID
does not match the explicit argument.

```sh
railway init --name quivr-v2-demo --workspace YOUR_WORKSPACE_ID --json
python3 deploy/railway/provision.py --project-id YOUR_PROJECT_ID
python3 deploy/railway/provision.py --project-id YOUR_PROJECT_ID --apply
```

The first command previews the service plan. Applying creates missing services and
volumes and configures runtime variables through stdin, without deploying code or
adding public domains. Generated credentials are kept in Railway and an ignored 0600
file under `.scratch/railway/PROJECT_ID/secrets.json`. Keep this file privately for
repeated provisioning; do not rotate the database or S3 password by rerunning with a
new file against an existing deployment. The web password is `demo_password` in that
file. It is a shared evaluation space, not per-user access.

The deployment helper connects pinned image sources or uploads Dockerfile services
from the repository root. It starts deployment but does not wait for readiness:

```sh
python3 deploy/railway/deploy.py --project-id YOUR_PROJECT_ID postgres temporal seaweed weaviate tei
# Wait for dependencies to start successfully, then:
python3 deploy/railway/deploy.py --project-id YOUR_PROJECT_ID api
# Wait for API readiness, then:
python3 deploy/railway/deploy.py --project-id YOUR_PROJECT_ID worker worker-bulk web
```

The provisioner sets each Dockerfile path. Deploy in order:

1. postgres, seaweed, temporal, weaviate and tei; inspect deployment status/logs.
2. api; its startup migration bootstraps schema, bucket and projection, then readiness.
3. worker, worker-bulk and web; verify readiness before publishing.

Core readiness uses `PORT=8081`; internal API traffic uses port 8080. The worker
also probes on 8081. Only the web service uses its port 3000 for public traffic.
All core connections use fixed service DNS names within this project environment.

The tokenizer and model are downloaded and checksum-verified at **build time** from
accepted locks. TEI's image contains the complete snapshot and `HF_HUB_OFFLINE=1`;
runtime startup does not download a model. Startup migration failure exits instead
of exposing a partially initialized API. Logs go to service stdout/stderr; runtime
configuration is generated privately in `/tmp`, never printed. The tokenizer stage
supports x86_64 only: on an arm64 machine, build the core image locally with
`docker build --platform linux/amd64 -f deploy/railway/core.Dockerfile .`.

## Domain and HTTPS

```sh
railway domain --service web --port 3000 --json
railway domain quivr.thevibecompany.co --service web --port 3000 --json
railway domain status quivr.thevibecompany.co --service web --json
```

Use the exact CNAME and ownership TXT records returned by Railway; do not guess a
`*.up.railway.app` target. `thevibecompany.co` currently has Cloudflare authoritative
nameservers, so changes in Vercel's DNS UI alone do not publish those records.
Keep the facade `DEMO_SECURE_COOKIE=true` for HTTPS and preserve the original Host
header. TLS terminates at Railway; no public API key belongs in the browser bundle.

## Verification and operations

- Inspect `railway service list --json` and scoped logs for readiness/failures.
- Open the public HTTPS URL, sign in, add a unique synthetic text, search in hybrid
  and lexical modes, and read the unchanged source. Check the cookie is Secure and
  HttpOnly. Anonymous `/demo/session` must return 401.
- Save that Record/Version/Receipt identity, restart application and dependency
  services, then repeat search/source reads. Volumes must remain attached; never use
  service/volume deletion for this persistence check.
- Browser checks can target the public deployment with `QUIVR_DEMO_URL` and
  `QUIVR_DEMO_PASSWORD` set in the test process environment. Do not publish traces
  containing passwords or submitted text.
- Inspect actual memory/CPU/storage usage after startup and during an ingestion;
  idle estimates are not a monthly bill. Keep this single-replica demo on only while
  needed. Back up/export valuable content before deleting the demo project.

## Primary references checked

- [Railway private networking](https://docs.railway.com/networking/private-networking/how-it-works): environment-scoped service DNS, private HTTP connections, IPv4/IPv6 for new environments.
- [Railway volumes](https://docs.railway.com/volumes/reference): persistent mounts and deployment behavior.
- [Railway domains](https://docs.railway.com/networking/domains/working-with-domains): custom domain records and automatic TLS.
- [Railway config as code](https://docs.railway.com/config-as-code): deployment configuration.
- Live CLI help and GraphQL schema introspection were used for the installed CLI;
  `Builder` does not include `DOCKERFILE`, so the provisioner sets `dockerfilePath`
  directly rather than supplying an invalid builder enum.

Deployment IDs, final URL/DNS state, restart evidence and observed resource usage
are recorded in the [deployment evidence](../../docs/dated/evidence/) once verified.
