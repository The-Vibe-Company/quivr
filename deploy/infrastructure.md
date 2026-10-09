# Shared infrastructure settings

`deploy/infrastructure.json` is the source of truth for portable Compose and Railway
settings. It contains no credentials or deployment selectors. `small` is the
conservative default; `large` is an unbenchmarked operational starting point for
object-heavy imports, not a capacity promise.

## Profiles

Both profiles keep the pinned images and PostgreSQL tuning; bytes are decimal unless marked MiB or GiB, and the CPU column is vCPU.

| Service | small memory / CPU | large memory / CPU |
| --- | ---: | ---: |
| PostgreSQL | 1,073,741,824 / 1 | 32,000,000,000 / 32 |
| Weaviate | 2,147,483,648 / 2 | 32,000,000,000 / 32 |
| API | no shared limit (native host on Compose) | 32,000,000,000 / 32 |
| worker (live) | no shared limit (native host on Compose) | 32,000,000,000 / 32 |
| worker-bulk | no shared limit (native host on Compose) | 32,000,000,000 / 32 |
| Autoscaler | 134,217,728 (128 MiB) / 0.25 | 134,217,728 (128 MiB) / 0.25 |

Large PostgreSQL and Weaviate planning budgets are 500,000,000,000 bytes (500 GB decimal) each. PostgreSQL derives `QUIVR_POSTGRES_VOLUME_MB=476837` unless overridden;
the capacity report does not resize volumes or verify shared filesystem quotas.

The large profile sets Weaviate `ASYNC_INDEXING=true`, `PERSISTENCE_MEMTABLES_MAX_SIZE_MB=1024`, `GOMEMLIMIT=27GiB` and
`RAFT_BOOTSTRAP_TIMEOUT=3600`; these unbenchmarked settings may let object writes precede
vector visibility, and memtable memory multiplies across active buckets and shards.

`GOMEMLIMIT` is a soft Go runtime budget, not an RSS cap. Keep RQ-8, schema disabled and the `none` vectorizer; choose measured limits before increasing a profile.

## Declared settings

Every environment key below can be changed under `services.SERVICE.environment` in
an override file within resolver validation rules. Resource limits use `services.SERVICE.deploy.resources.limits`;
Weaviate image overrides require a digest. PostgreSQL and autoscaler image changes
use the declaration's build pins and generation; per-installation image overrides
for those built services are rejected. [PostgreSQL sizing](compose/README.md) covers derived memory/WAL values and their overrides.

| Service / setting | Default and rationale |
| --- | --- |
| PostgreSQL `QUIVR_POSTGRES_MAX_CONNECTIONS` | 256 small / 500 large; accommodate bounded API/worker pools with rollout headroom. |
| PostgreSQL large memory/WAL overrides | `QUIVR_POSTGRES_SHARED_BUFFERS=10GB`, `QUIVR_POSTGRES_EFFECTIVE_CACHE_SIZE=25GB`, `QUIVR_POSTGRES_MAINTENANCE_WORK_MEM=1GB`, `QUIVR_POSTGRES_WORK_MEM=16MB`, `QUIVR_POSTGRES_MAX_WAL_SIZE=32GB`, `QUIVR_POSTGRES_MIN_WAL_SIZE=4GB`; reserve memory for concurrent work and recovery. |
| `QUIVR_POSTGRES_WORK_MEM` | 4MB per operation; limit multiplied sort/hash memory. |
| `QUIVR_POSTGRES_RANDOM_PAGE_COST` | 1.1; model SSD random reads close to sequential reads. |
| `QUIVR_POSTGRES_EFFECTIVE_IO_CONCURRENCY` | 200; permit concurrent SSD prefetch. |
| `QUIVR_POSTGRES_CHECKPOINT_TIMEOUT` | 15min; reduce checkpoint write pressure at a recovery-time cost. |
| `QUIVR_POSTGRES_WAL_COMPRESSION` / `QUIVR_POSTGRES_STAT_STATEMENTS_TRACK` | lz4 / all; reduce full-page WAL bytes while retaining complete statement statistics for diagnosis. |
| `QUIVR_POSTGRES_JIT` / `QUIVR_POSTGRES_SYNCHRONOUS_COMMIT` | on / on; JIT supports planned compilation and may override to off, while only `on` is accepted for durable acknowledgements. |
| Weaviate `DEFAULT_QUANTIZATION` | rq-8; reduce vector memory, retaining explicit per-space configuration. |
| `ASYNC_INDEXING` | false in small, true in large; decouple large writes from ANN indexing with visibility lag. |
| `PERSISTENCE_MEMTABLES_MAX_SIZE_MB` | 200 in small, 1024 in large; fewer flushes consume more bucket/shard memory and recovery work. |
| `GOMEMLIMIT` | 1700MiB in small / 27GiB in large; leave headroom below the service limit. |
| `RAFT_BOOTSTRAP_TIMEOUT` | 600 seconds small / 3600 large; allow a populated index time to recover at startup. |
| `AUTOSCHEMA_ENABLED` / `DEFAULT_VECTORIZER_MODULE` | false / none; the engine owns schema and embeddings. |
| `PERSISTENCE_DATA_PATH` | /var/lib/weaviate; match the persistent volume mount. |
| `AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED` | true; private-network example access, never expose this port publicly. |
| Autoscaler `QUIVR_AUTOSCALER_QUEUE` / `QUIVR_AUTOSCALER_BACKEND` | bulk / railway in large; keep live work separate, or override the backend to kubernetes. |
| `QUIVR_AUTOSCALER_MIN` / `QUIVR_AUTOSCALER_MAX` | 1 / 8; retain a worker and bound dependency pressure. |
| `QUIVR_AUTOSCALER_DOCUMENTS_PER_REPLICA` | 20000; starting backlog target, tune from measured throughput. |
| `QUIVR_AUTOSCALER_INTERVAL` / `QUIVR_AUTOSCALER_REQUEST_TIMEOUT` | 30s / 10s; bound polling and API waits. |
| `QUIVR_AUTOSCALER_DOWNSCALE_WINDOW` / `QUIVR_AUTOSCALER_MIN_SCALE_INTERVAL` | 5m / 2m; avoid oscillation and repeated deployment restarts. |

PostgreSQL keeps mmap dynamic shared memory, preloaded statistics, I/O timing and JIT;
`synchronous_commit=on` preserves acknowledged-write durability. Its initialization
creates `pg_stat_statements`; monitoring setup for existing volumes uses the same
idempotent script. Autoscaler credentials, URLs and target IDs stay external. Declaration
`x-quivr.build_images` pins its build/runtime bases and keeps generated artifacts in sync.

## Override and render

An override is a JSON object with only a `services` object. Put environment values, resource limits or the per-service storage budget under the service they affect:

```json
{"services":{"postgres":{"x-quivr-storage-budget-bytes":"500000000000"},"weaviate":{"x-quivr-storage-budget-bytes":"500000000000","environment":{"PERSISTENCE_MEMTABLES_MAX_SIZE_MB":"512"}}}}
```

The resolver validates names, values, limits and immutable image digests. Use the same
resolved profile and override for launch and checking. Render an ignored Compose overlay:

```sh
python3 deploy/infrastructure.py render --profile large \
  --output .scratch/infrastructure.json
```

Add `--overrides FILE` when needed. `make dev` reads `QUIVR_INFRASTRUCTURE_PROFILE` and
`QUIVR_INFRASTRUCTURE_OVERRIDES`, then resolves an ignored overlay on Linux and macOS.
The raw Compose file extends declaration defaults directly; append the rendered overlay
last for a large profile or override. Run `make infrastructure-check` after declaration changes to keep
generated PostgreSQL and autoscaler artifacts in sync.

## Apply and verify

The Compose adapter exposes `preview`, `apply`, `check`, and `initialize-monitoring`; it
manages PostgreSQL and Weaviate only, while API and worker processes stay native hosts.
`apply` writes its resolved overlay to `.scratch/PROJECT/infrastructure.json`:

```sh
python3 deploy/compose/infrastructure.py preview \
  --project PROJECT --profile small
python3 deploy/compose/infrastructure.py apply \
  --project PROJECT --profile small
python3 deploy/compose/infrastructure.py initialize-monitoring \
  --project PROJECT --profile small
python3 deploy/compose/infrastructure.py check \
  --project PROJECT --profile small
```

For Railway, the infrastructure adapter requires an environment ID and accepts the same
profile and override. A fresh project can first use `provision.py`; otherwise preview the
adapter plan, then apply variables and limits without changing existing replicas. New
services start at one. Deploy changed services (include API, worker and worker-bulk for
large), initialize monitoring, then check the active deployment:

For PostgreSQL, apply/provision selects `deploy/railway/postgres.Dockerfile` and clears Railway's image source in that environment;
it only stages the switch. For an existing installation, run `railway up --project ID --environment ID --service POSTGRES_SERVICE_ID --detach`
from the repository root; a newly provisioned installation can use the existing helper. This explicit selection changes no replica count.
On restart, startup command-line settings take precedence over existing `ALTER SYSTEM` values in `postgresql.auto.conf`.

```sh
python3 deploy/railway/infrastructure.py preview \
  --project-id ID --environment-id ID --profile large
python3 deploy/railway/infrastructure.py apply \
  --project-id ID --environment-id ID --profile large
railway up --project ID --environment ID \
  --service POSTGRES_SERVICE_ID --detach
python3 deploy/railway/infrastructure.py initialize-monitoring \
  --project-id ID --environment-id ID --profile large
python3 deploy/railway/infrastructure.py check \
  --project-id ID --environment-id ID --profile large
```

The Railway adapter targets PostgreSQL and Weaviate by default; large adds API, worker and
worker-bulk with their 32,000,000,000-byte/32-vCPU limits. Without `--service`, an existing
uniquely named `autoscaler` is included automatically, never created or replica-reset.
Repeated `--service ROLE=NAME_OR_ID` is an exact replacement for defaults. `apply` does not
deploy or reset scaler-owned replicas.
`initialize-monitoring` runs the deployment-owned PostgreSQL script; fresh databases already run `init.sql`, while existing ones receive
the statistics extension. It is separate from the Quivr core migration.

Railway `check` probes each running replica and compares configured values with active
startup values, PostgreSQL effective SQL, managed image source, resources and filesystem
capacity against each storage budget. A capacity report does not resize volumes or verify
shared filesystem quotas. Railway cannot verify a built PostgreSQL base image from its runtime API; the generated Dockerfile pins it. Its distroless autoscaler has no shell, so
an unavailable probe is unknown; inspect its effective-policy startup log. A pending
deployment, missing setting or failed probe is also `unknown`, never a match. Exit status
is 0 for `match`, 1 for `drift`, and 2 for `unknown` or an error.
