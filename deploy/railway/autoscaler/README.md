# Scale bulk workers on Railway

Run `quivr-autoscaler` as one small, always-on service. It reads waiting bulk documents from Quivr and adjusts only the bulk worker's replica count; the live worker stays at one replica.

## Prerequisites

You need a Railway project with the API and worker deployed from a release that provides `GET /v0/admin/queues`. You also need permission to create services and secrets, and an environment-scoped Railway **project token** from project settings. This token uses `Project-Access-Token`, as described in [Railway's API authentication](https://docs.railway.com/integrations/api).

Set `QUIVR_QUEUE_KEY` in the API service's Railway variables to a randomly generated secret (for example, from your password manager), distinct from the API and operator keys. Redeploy the API after adding it. The generated configuration grants this dedicated key only `queues:read` with all-corpus scope (`corpora: ["*"]`); the web app never receives it. Give the autoscaler the same queue key as a secret.

The steps below are operator examples, not run against a live Railway project. The Go policy, controller and HTTP adapters have offline tests with fake APIs; real throughput and capacity measurements belong to your deployment.

## Create the workers

1. For an existing installation, upgrade every API and worker that can write dispatch state to the queue-capable release. Keep the existing `worker` serving both queues: omit `QUIVR_WORKER_QUEUES` or set it to `live,bulk`. Older writers must stop before the drain can complete. Check both pending legacy dispatch tables with the SQL below, then query Temporal's workflow list for `TaskQueue = 'quivr-content-v0' AND ExecutionStatus = 'Running'`. Keep a mixed worker until both SQL counts and running executions remain zero after dispatch and Temporal visibility catch up; pending legacy rows can still start old mixed histories.
2. Run the existing [provisioning procedure](../README.md#provision-and-deploy), after that drain. `services.json` keeps the existing service named `worker` as the live worker and adds `worker-bulk`. Both start at one replica, use `core.Dockerfile`, and probe `/readyz`. The provisioner sets `QUIVR_WORKER_QUEUES=live` on `worker` and `bulk` on `worker-bulk`. On a fresh installation there are no older histories to drain.
3. Before deploying `worker-bulk`, copy the existing worker's additional plugin/provider variables and secrets to it. All workers must share database, Temporal namespace, storage, cursor/credential/signing keys, active plugin settings and embedding endpoints. Keep each service's queue selector. Do not add a volume: each replica runs its own loopback plugin sidecars and temporary files. Both slot counts default to four; override with `QUIVR_WORKER_LIVE_SLOTS` and `QUIVR_WORKER_BULK_SLOTS` (1–1024).
4. Deploy API, `worker` and `worker-bulk` with the existing deploy helper. Verify readiness and a live document becoming searchable while bulk work is pending. The API and workers must agree on plugin pins before scaling.

Run this read-only query against the deployment's PostgreSQL database before switching to separate workers. `ingestion_batches` rows disappear only after durable workflow acceptance; they have no `dispatched` column.

```sql
SELECT 'outbox' AS source, count(*) AS pending
FROM ingestion_outbox WHERE legacy_workflow AND NOT dispatched
UNION ALL
SELECT 'batches', count(*)
FROM ingestion_batches WHERE legacy_workflow;
```

## Create the autoscaler

1. Add a service named `autoscaler` from the same repository and branch, with repository root as the build context and `deploy/railway/autoscaler.Dockerfile` as its Dockerfile. Keep it at one replica, always running, with no cron, volume, public domain or HTTP healthcheck. Disable Railway serverless sleeping. Never run two controllers against the same target.
2. Set the required variables below in that service. Copy service and environment IDs from Railway's dashboard; `RAILWAY_BULK_SERVICE_ID` must identify `worker-bulk`. Store both keys as secrets, never in source control or command output. `RAILWAY_TOKEN` specifically means a project token here, rather than an account/workspace token.
3. Deploy the autoscaler. Watch its JSON logs for `autoscaler started` followed by `autoscaling decision`. Each poll logs `waiting`, observed `current`, recommended `target`, `applied` and `outcome` (`hold`, `cooldown`, `scaled`, or an error stage). `applied` is the acknowledged setting after an update and deploy request, or the last observed count on a hold/error; it does not mean every replica is running yet. An unknown count is `-1`. API failures log a sanitized error.

| Required variable | Value |
| --- | --- |
| `QUIVR_QUEUE_URL` | Full queue endpoint, e.g. `http://api.railway.internal:8080/v0/admin/queues`; use HTTPS outside the private network |
| `QUIVR_QUEUE_KEY` | Secret Quivr key with `queues:read` and all-corpus scope |
| `RAILWAY_TOKEN` | Secret project token scoped to the target environment |
| `RAILWAY_BULK_SERVICE_ID` | ID of the bulk worker service, never the live worker |
| `RAILWAY_ENVIRONMENT_ID` | ID of that service's environment |

| Optional variable | Default / meaning |
| --- | --- |
| `QUIVR_AUTOSCALER_ENABLED` | `true`; `false` exits without reading APIs or changing replicas |
| `QUIVR_AUTOSCALER_MIN` | `1`, so admitted work continues after waiting drains |
| `QUIVR_AUTOSCALER_MAX` | `8`; installation capacity ceiling |
| `QUIVR_AUTOSCALER_DOCUMENTS_PER_REPLICA` | `20000` waiting documents per replica |
| `QUIVR_AUTOSCALER_DOWNSCALE_WINDOW` | `5m`; highest recent recommendation limits scale-down; `0s` disables stabilization |
| `QUIVR_AUTOSCALER_MIN_SCALE_INTERVAL` | `2m` between successful scale actions; `0s` disables spacing |
| `QUIVR_AUTOSCALER_INTERVAL` | `30s` delay after each poll, at least `1s` |
| `QUIVR_AUTOSCALER_REQUEST_TIMEOUT` | `10s` per HTTP request, at least `1s` |

The target is `clamp(ceil(waiting / documents_per_replica), min, max)`. With defaults, 20,000 waiting documents need one replica, 20,001 need two, and 140,001 need eight. The first scale-up is immediate; subsequent actions wait for the minimum scale interval while backlog observations continue. Scale-down uses the highest recommendation from the last five minutes. Startup, failed observations/writes and external replica changes restart that window conservatively at the observed count. This suppresses oscillation around a threshold. The controller reads Railway's deployed configuration on every poll; it never assumes a failed write succeeded. Counts above the configured maximum are retained only until stabilization allows reduction.

The backend uses [Railway's service API](https://docs.railway.com/integrations/api/manage-services) with an explicit User-Agent and service/environment IDs. It reads the single region's count from `latestDeployment.meta.serviceManifest.deploy.multiRegionConfig`, then writes `input.multiRegionConfig` through `serviceInstanceUpdate` and requests `serviceInstanceDeploy`. Only when regional configuration is absent does it read/write plain `numReplicas` (preferring the deployment manifest's count when available); multiple regions or invalid regional counts cause an error without changes. A rejected deploy is retried from the deployed count on a later decision. Do not pin replicas in a Railway configuration file that overrides API changes or run another scaler. A normal poll makes one Railway read; a scale decision adds a fresh read, an update and a deploy request. Adjust polling for your token's rate limit.

Every scale action, including scale-up, deploys the service again and restarts existing replicas. Bulk work is durable and retried after interruption. Deployment completes asynchronously: verify Railway's deployment status and running instances after a `scaled` log. Increase the minimum scale interval to cover typical deployment duration and limit restart churn. Restarting the autoscaler resets that interval and its downscale window, permitting the next scale-up immediately but delaying scale-down by a full window.

## Check capacity and disable scaling

Create bulk work, watch `queues.bulk.waiting` rise and confirm `scaled` decisions update the deployment manifest's regional count and then the running bulk replicas. After draining, confirm the count falls to the minimum after stabilization. Keep the live service fixed at one and check its document freshness throughout. A `hold` log with a lower backlog can be the stabilization window; `cooldown` means the minimum scale interval has not elapsed; repeated error stages indicate credentials, connectivity, API limits or an unavailable queue snapshot. Missing/malformed data and 503 responses are errors, never zero backlog.

PostgreSQL, Weaviate, Temporal and the embedding provider are shared by every replica. Adding workers does not add GPU capacity. Measure waiting and in-progress documents, oldest waiting age, searchable throughput, provider latency/throttling and database/search CPU/connection pressure at each replica count. If throughput plateaus or live freshness worsens, lower the ceiling or per-process slots and address the measured dependency bottleneck. Configure your embedding service's container/concurrency ceiling separately. See [worker backlog metrics](../../../docs-site/run-quivr/deploy.mdx#scale-workers-on-backlog) and [observability configuration](../../../docs-site/reference/configuration.mdx#opentelemetry) for collection; measure live freshness from durable acceptance until the document appears in search.

To disable scaling, stop the autoscaler service or set `QUIVR_AUTOSCALER_ENABLED=false` and redeploy it with restart policy `ON_FAILURE`. Disabling preserves the last replica count; set `worker-bulk` manually to your chosen count (at least one). Stop the autoscaler before rerunning the provisioner: provisioning resets both workers to one replica. To return to one mixed worker, stop the autoscaler, restore `QUIVR_WORKER_QUEUES=live,bulk` on `worker`, verify it is ready, then stop `worker-bulk`.

## Next

Use the [KEDA deployment examples](../../../docs-site/run-quivr/deploy.mdx#scale-workers-on-backlog) on Kubernetes. They use the same defaults (one to eight workers, 20,000 waiting documents per replica, five-minute downscale stabilization); KEDA/HPA performs the scaling there, without this binary. HPA tolerance and polling can make its exact decisions differ near a threshold.
