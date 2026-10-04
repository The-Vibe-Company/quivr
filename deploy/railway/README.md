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
`observability:read`; the web app never gets the first two. Use it from inside the deployment
(`railway ssh --service api`, port 8080) to rebuild a Corpus projection, to follow documents through
their steps (`GET /v0/admin/documents`), or to register and activate plugins ([Switch plugins without restarting](https://docs.quivr.thevibecompany.co/plugins/switch-plugins-without-restarting)); a redeploy that changes the plugin pins applies them, even over an earlier activation of the same role.
`QUIVR_DEMO_ADMIN=1` on api gives the web app's key `observability:read`, which turns on the web app's read-only **Admin** tab (live flow of documents, timelines, throughput).

## Core plugins and the rebuild after THE-777

api and worker run the core.ingest plugin (`127.0.0.1:9950`, `CONNECTORS` in `core-entrypoint.py`),
which segments and embeds with TEI; the api alone runs core.retrieve (`127.0.0.1:9960`), which
ranks every search (THE-779). Neither starts without its plugin. After the first deploy with core.ingest,
rebuild each earlier Corpus once with the operator key (later ones start on core.ingest):
`POST /v0/corpora/{corpus_id}/rebuilds` with an `idempotency_key`, then poll the Operation.
Until then search works in every mode and new articles are searchable by keyword at once;
their vectors attach at the rebuild, which re-embeds with the same model, so results hold.

## Jev deep searches (optional)

For optional Cohere embeddings beside core.ingest, follow
[Switch hosted text embeddings with rollback](hosted-embeddings.md).

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
See [Re-rank with Jev](https://docs.quivr.thevibecompany.co/guides/rerank-with-jev)
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
Set `QUIVR_DEMO_PLUGINS=1` on api and worker and `QUIVR_DEMO_DESTINATION_ID=demo-alerts-sink` on web, then redeploy all three. `core-entrypoint.py`:

- pins pdf-text on `127.0.0.1:9900` (`application/pdf`) and alerts on `127.0.0.1:9910`; worker runs both, api also runs alerts for previews. A sidecar exit stops its container for Railway to restart;
- enables external [described alerts](https://docs.quivr.thevibecompany.co/guides/described-alerts) when `TYPESAFE_API_KEY` is the same on api and worker; alerts receives it, as does the API's Jev sidecar when enabled. Set `DEMO_DESCRIBED_ALERTS=true` on web to offer them;
- grants the demo key `monitoring:read` and `monitoring:write`;
- declares destination `demo-alerts-sink` at `http://alerts-sink.invalid/`: web reads Matches through the API, deliveries fail on the reserved name, and private-address refusal stays on. Its signing secret derives from `QUIVR_CURSOR_KEY`.

The worker logs its actual versions in `plugins pinned`. Without a web `DEMO_STATE_FILE` volume, paused alerts leave the list after a web restart; active ones are found through the API.

## Upgrade a bundled alert rule without losing alerts

Use an operator key with `plugins:admin` and the relevant Corpus grants. Call the admin API from inside the deployment; `web` never gets this permission.

1. Keep the exact old implementation A running at a reachable endpoint while starting B at another endpoint. A new image that only contains B cannot serve A: retain and run A's old code separately. Never relabel B as A.
2. [Register, certify and activate B](https://docs.quivr.thevibecompany.co/plugins/switch-plugins-without-restarting#upgrade-an-alert-rule). Dry-run `POST /v0/admin/subscriptions/evaluator-migrations`, inspect refusals, then migrate in pages. New Subscription Versions apply only to future changes; queued pins stay on A.
3. Keep A reachable until its registry `subscriptions` and `pinned_work` are both zero, including undispatched old triggers. Only then remove A's endpoint or image.

If A has already disappeared, its missing-evaluator retries continue unchanged. Restore A for zero-loss recovery, or intentionally abandon potential Matches with `POST /v0/admin/subscriptions/evaluation-retirements`.
Follow the [bounded retirement procedure](https://docs.quivr.thevibecompany.co/run-quivr/retire-alert-evaluations): inspect `GET /v0/admin/subscriptions/evaluation-backlog`, dry-run first, wait for leases and dispatch, then repeat real batches with new keys. Retirement records `evaluator_retired`, never `no_match`, and creates no Match or Delivery.

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
