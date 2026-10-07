# Scale bulk workers on Railway

Run `quivr-autoscaler` as one small, always-on service. It reads waiting bulk documents from Quivr and adjusts only the bulk worker's replica count; the live worker stays at one replica.

## Prerequisites

You need a Railway project with the API and worker deployed from a release that provides `GET /v0/admin/queues`. You also need permission to create services and secrets, and an environment-scoped Railway **project token** from project settings. This token uses `Project-Access-Token`, as described in [Railway's API authentication](https://docs.railway.com/integrations/api).

Set `QUIVR_OPERATOR_KEY` in the API service's Railway variables to a randomly generated secret (for example, from your password manager), or reuse its existing value. Redeploy the API after adding it. The generated configuration grants that key `queues:read` and all-corpus scope (`corpora: ["*"]`); the web app never receives it. A separately configured Quivr installation can use a dedicated key with only that grant. Give the autoscaler the same key as a secret.

The steps below are operator examples, not run against a live Railway project. The Go policy, controller and HTTP adapters have offline tests with fake APIs; real throughput and capacity measurements belong to your deployment.

## Create the workers

1. For an existing installation, first deploy the queue-capable core image with the existing `worker` serving both queues: omit `QUIVR_WORKER_QUEUES` or set it to `live,bulk`. In Temporal's workflow list, query `TaskQueue = 'quivr-content-v0' AND ExecutionStatus = 'Running'` and wait until no executions remain before switching to live-only. Those older histories mix live and bulk work. Keep a mixed worker until they drain.
2. Run the existing [provisioning procedure](../README.md#provision-and-deploy), after that drain. `services.json` keeps the existing service named `worker` as the live worker and adds `worker-bulk`. Both start at one replica, use `core.Dockerfile`, and probe `/readyz`. The provisioner sets `QUIVR_WORKER_QUEUES=live` on `worker` and `bulk` on `worker-bulk`. On a fresh installation there are no older histories to drain.
3. Before deploying `worker-bulk`, copy the existing worker's additional plugin/provider variables and secrets to it. All workers must share database, Temporal namespace, storage, cursor/credential/signing keys, active plugin settings and embedding endpoints. Keep each service's queue selector. Do not add a volume: each replica runs its own loopback plugin sidecars and temporary files. Both slot counts default to four; override with `QUIVR_WORKER_LIVE_SLOTS` and `QUIVR_WORKER_BULK_SLOTS` (1–1024).
4. Deploy API, `worker` and `worker-bulk` with the existing deploy helper. Verify readiness and a live document becoming searchable while bulk work is pending. The API and workers must agree on plugin pins before scaling.

## Create the autoscaler

1. Add a service named `autoscaler` from the same repository and branch, with repository root as the build context and `deploy/railway/autoscaler.Dockerfile` as its Dockerfile. Keep it at one replica, always running, with no cron, volume, public domain or HTTP healthcheck. Disable Railway serverless sleeping. Never run two controllers against the same target.
2. Set the required variables below in that service. Copy service and environment IDs from Railway's dashboard; `RAILWAY_BULK_SERVICE_ID` must identify `worker-bulk`. Store both keys as secrets, never in source control or command output. `RAILWAY_TOKEN` specifically means a project token here, rather than an account/workspace token.
3. Deploy the autoscaler. Watch its JSON logs for `autoscaler started` followed by `autoscaling decision`. Each poll logs `waiting`, observed `current`, recommended `target` and `outcome` (`hold`, `scaled`, or an error stage). An unknown count is `-1`. Failed API reads or writes leave observed capacity unchanged and log a sanitized error.

| Required variable | Value |
| --- | --- |
| `QUIVR_QUEUE_URL` | Full queue endpoint, e.g. `http://api.railway.internal:8080/v0/admin/queues`; use HTTPS outside the private network |
| `QUIVR_OPERATOR_KEY` | Secret Quivr key with `queues:read` and all-corpus scope |
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
| `QUIVR_AUTOSCALER_INTERVAL` | `30s` delay after each poll, at least `1s` |
| `QUIVR_AUTOSCALER_REQUEST_TIMEOUT` | `10s` per HTTP request, at least `1s` |

The target is `clamp(ceil(waiting / documents_per_replica), min, max)`. With defaults, 20,000 waiting documents need one replica, 20,001 need two, and 140,001 need eight. Scale-up is immediate. Scale-down uses the highest recommendation from the last five minutes. Startup, failed observations/writes and external replica changes restart that window conservatively at the observed count. This suppresses oscillation around a threshold. The controller reads Railway's configured count on every poll; it never assumes a failed write succeeded. Counts above the configured maximum are retained only until stabilization allows reduction.

The backend uses [Railway's service API](https://docs.railway.com/integrations/api/manage-services): `serviceInstance.numReplicas` reads the count and `serviceInstanceUpdate` writes `input.numReplicas` for explicit service/environment IDs. It targets a single-region service; do not combine it with region-specific replica configuration or another scaler. Do not pin replicas in a Railway configuration file that overrides API changes. Adjust polling for your token's API rate limit; the normal poll makes one Railway read and only writes when the decision changes. Restarting the autoscaler resets its in-memory window, delaying scale-down by a full window.

## Check capacity and disable scaling

Create bulk work, watch `queues.bulk.waiting` rise and confirm `scaled` decisions increase Railway's configured replicas. After draining, confirm the count falls to the minimum after stabilization. Keep the live service fixed at one and check its document freshness throughout. A `hold` log with a lower backlog can be the stabilization window; repeated error stages indicate credentials, connectivity, API limits or an unavailable queue snapshot. Missing/malformed data and 503 responses are errors, never zero backlog.

PostgreSQL, Weaviate, Temporal and the embedding provider are shared by every replica. Adding workers does not add GPU capacity. Measure waiting and in-progress documents, oldest waiting age, searchable throughput, provider latency/throttling and database/search CPU/connection pressure at each replica count. If throughput plateaus or live freshness worsens, lower the ceiling or per-process slots and address the measured dependency bottleneck. Configure your embedding service's container/concurrency ceiling separately. See [worker backlog metrics](../../../docs-site/run-quivr/deploy.mdx#scale-workers-on-backlog) and [observability configuration](../../../docs-site/reference/configuration.mdx#opentelemetry) for collection; measure live freshness from durable acceptance until the document appears in search.

To disable scaling, stop the autoscaler service or set `QUIVR_AUTOSCALER_ENABLED=false` and redeploy it with restart policy `ON_FAILURE`. Disabling preserves the last replica count; set `worker-bulk` manually to your chosen count (at least one). Stop the autoscaler before rerunning the provisioner: provisioning resets both workers to one replica. To return to one mixed worker, stop the autoscaler, restore `QUIVR_WORKER_QUEUES=live,bulk` on `worker`, verify it is ready, then stop `worker-bulk`.

## Next

Use the [KEDA deployment examples](../../../docs-site/run-quivr/deploy.mdx#scale-workers-on-backlog) on Kubernetes. They use the same defaults (one to eight workers, 20,000 waiting documents per replica, five-minute downscale stabilization); KEDA/HPA performs the scaling there, without this binary. HPA tolerance and polling can make its exact decisions differ near a threshold.
