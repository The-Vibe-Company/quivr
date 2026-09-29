# Railway evaluation demo

## Local demo first

`make demo` runs the same web UI against a real local stack at http://127.0.0.1:5183.
It keeps its own data across restarts; `make demo-reset` deletes only that demo's data
and `make verify-demo` runs the browser checks. See
[`quivr-search/README.md`](../../quivr-search/README.md) for prerequisites, frontend
development and the optional shared password.

## Hosted deployment

This deployment runs the real [`quivr-search`](../../quivr-search/) UI and V2 core. One dedicated Railway project,
`quivr-v2-demo`, contains eight single-replica services. Only `web` is exposed publicly.

| Service | Runtime / responsibility | Persistence |
| --- | --- | --- |
| web | Node facade, static React bundle, shared-password session | Stateless |
| api | Go HTTP API; applies migrations before serving | PostgreSQL/S3 |
| worker | Go/Temporal processing and optional E5 enrichment | PostgreSQL/S3/Temporal |
| postgres | Pinned PostgreSQL 17 | `/data/pgdata` on `/data` volume |
| seaweed | Pinned SeaweedFS mini, authenticated S3 | `/data` volume |
| temporal | Pinned Temporal dev server, headless | SQLite `/data/temporal.db` volume |
| weaviate | Pinned standalone search projection | `/var/lib/weaviate` volume |
| tei | Pinned CPU E5 inference | Model baked into image; derived artifacts in S3 |

This is a single-node evaluation deployment, with the accepted Temporal dev server
and no high availability. Redeploying a volume-backed service can interrupt requests.
No Railway TCP proxies or public dependency domains are needed.

Webhook delivery and the RSS connector refuse private and internal addresses
(checked after DNS resolution, so Railway's private network is unreachable
through them). The generated configuration never sets
`delivery.allow_private_destinations` or `connector_rss_allow_private_addresses`;
those allowances exist for the local harness only.

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

## Keyword alerts and PDF text (optional)

The core image bakes in the first-party plugins [`alerts`](../../plugins/alerts/README.md),
which the **Alertes** tab needs, and [`pdf-text`](../../plugins/pdf-text/README.md).
Set `QUIVR_DEMO_PLUGINS=1` on api and worker and `QUIVR_DEMO_DESTINATION_ID=demo-alerts-sink`
on web, then redeploy api, worker and web. `core-entrypoint.py` then:

- pins both through the `plugins` list: pdf-text on `127.0.0.1:9900` (`application/pdf`),
  alerts on `127.0.0.1:9910`. Only the worker calls plugins, so only the worker runs
  them, as sidecar processes; the API reads the manifests and never contacts them. If
  any worker process exits, the container stops and Railway restarts it;
- offers [described alerts](../../docs/described-alerts.md) (the pin's `kinds`) only when
  `TYPESAFE_API_KEY` is set, with the same value on api and worker; only the alerts
  sidecar receives it. Without it, only keyword alerts can be created;
- gives the demo key `monitoring:read` and `monitoring:write`;
- declares the webhook destination `demo-alerts-sink`, which every Subscription needs.
  The web app reads Matches through the API, so it points at `http://alerts-sink.invalid/`,
  a reserved name that never resolves: deliveries fail inside the container, and the
  private-address refusal stays on. Its signing secret derives from `QUIVR_CURSOR_KEY`.

The worker logs `plugins pinned` with `pdf-text@0.1.0 [normalizer] alerts@0.2.0
[subscription]` and `evaluators=1`. Without a web `DEMO_STATE_FILE` volume, paused
alerts leave the list after a web restart; active ones are found again through the API.

## Provision and deploy

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
python3 deploy/railway/deploy.py --project-id YOUR_PROJECT_ID worker web
```

The provisioner sets each Dockerfile path. Deploy in order:

1. postgres, seaweed, temporal, weaviate and tei; inspect deployment status/logs.
2. api; its startup migration bootstraps schema, bucket and projection, then readiness.
3. worker and web; verify readiness before publishing.

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
