# Deploy a search measurement store

Run MLflow 3.16.1 with PostgreSQL and server-proxied artifacts. Evaluation workers
need only tracking credentials; storage credentials stay on the server.

## Prerequisites

Use a container host with HTTPS, two PostgreSQL databases (tracking and auth),
and a persistent volume mounted at `/data`. Enable database and volume backups.
The following deployment commands are examples requiring your host's credentials.

Build from the repository root:

```sh
docker build -f deploy/mlflow/Dockerfile -t search-results .
```

Set these environment variables through your host's secret manager:

| Variable | Meaning |
| --- | --- |
| `DATABASE_URL` | Tracking PostgreSQL connection URI |
| `MLFLOW_AUTH_DATABASE_URI` | Separate authentication PostgreSQL URI |
| `MLFLOW_AUTH_ADMIN_PASSWORD` | Initial administrator password, at least 12 characters |
| `MLFLOW_FLASK_SERVER_SECRET_KEY` | Stable secret shared by server workers |
| `MLFLOW_ALLOWED_HOSTS` | Hostname of your HTTPS service |
| `PORT` | Listening port; default `5000` |

Optional: `MLFLOW_AUTH_ADMIN_USERNAME` (default `admin`), `MLFLOW_WORKERS` (default
`2`), and `MLFLOW_CORS_ALLOWED_ORIGINS` for explicit browser origins.
`MLFLOW_ARTIFACTS_DESTINATION` defaults to `/data/artifacts`. Keep it on the volume.
The entrypoint disables telemetry and writes a mode-600 temporary auth config,
with `NO_PERMISSIONS` as the default and a 60-second auth cache.

## Grant access

Using the MLflow 3.16.1 authentication API, create distinct aggregate writers,
aggregate readers and private writers/readers. Grant writers `EDIT` and readers
`READ` on their experiments. See [MLflow authentication](https://mlflow.org/docs/latest/self-hosting/security/basic-http-auth/)
for the API. Use `grant_user_permission`, rather than the removed legacy permission API.

An owner must provision each `private/<aggregate-experiment>` before measurements.
Grant ordinary agents access only to the aggregate experiment. An experiment name
alone grants no privacy: its ACL and distinct credentials enforce it. The wrapper
never creates private experiments. Keep private per-query outboxes on machines
accessible only to authorized evaluators, outside shared workspaces.

## Check persistence and privacy

From two authorized machines, use the [results CLI](../../docs/eval-results.md)
to log distinct configurations into the same experiment. Both must appear in
`leaderboard`, with different machine lineage. Log an artifact, restart the
server, and read it back. Anonymous API/UI requests must return 401; aggregate
readers must receive 403 when requesting a private run or its artifact directly.

The wrapper's wire contract, migration and ACLs were checked against a local
MLflow 3.16.1 basic-auth server, including denied private artifact reads.
SQLite is useful for local checks; use PostgreSQL for concurrent shared writers.
When upgrading, back up both databases and artifacts, then run `mlflow db upgrade`
with the tracking database URI before starting the new image. MLflow is mutable;
retain accepted decisions separately from this experiment store.
