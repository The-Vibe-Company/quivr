# THE-664 deployment evidence

Live evaluation URL: https://web-production-7a373.up.railway.app

Railway project `quivr-v2-demo`, ID `74b1e669-ead7-48c0-9f39-e807bb662826`, production environment `16ca09a5-197d-4611-ba69-1fe070306a08`. Runtime packaging was reviewed independently for Standards and Spec with no remaining findings. Deployment identities and sanitized public evidence are in `the-664-deployment.json`; credentials are deliberately absent.

## Verified

- Eight services deployed: public web facade, private API/worker, PostgreSQL, SeaweedFS, Temporal, Weaviate and CPU E5. Four separate persistent volumes are attached at the documented paths.
- Core and web images built locally. Seven browser scenarios and the facade HTTP test passed against the containerized API/worker/web with real dependencies. The pinned model image started and answered its health check under `--network none`.
- The public HTTPS deployment passed seven browser scenarios in 14.9 s: real Unicode ingestion/source round trip, hybrid search, session/scope restrictions, mobile dark/reduced-motion/keyboard, draft recovery, empty/network retry, ambiguous ingestion replay, unavailable storage and blocked-enrichment presentation. Some scenarios combine related checks; boundary fixtures are identified in tests.
- Public retention probe saved a Record/Version and verified the exact source plus lexical, semantic and hybrid retrieval. All eight services were restarted. The after probe read the **same** identifiers without reingestion; source SHA-256 and all retrieval checks still matched. A fresh real browser ingestion/search/source tracer then passed in 5.2 s.
- The shared session cookie is Secure and HttpOnly on the public HTTPS route. Core credentials remain private runtime variables.

## Useful failures and decisions

- Initial container compilation omitted embedded contracts/migrations; the Dockerfile now copies both packages.
- Railway's live GraphQL Builder enum has no DOCKERFILE value. Set `dockerfilePath` directly.
- Railway rejected the Weaviate `.well-known` health path. `/v1/meta` checks its process; actual projection availability is checked by core bootstrap and public search, not inferred from metadata alone.
- `environment edit` reported no image-source changes. `service source connect --image` successfully launched the pinned PostgreSQL and Weaviate images; the deployment helper uses that command.
- The first worker boot raced API migrations and exhausted its startup retries. Redeployment after API readiness succeeded and processed the pending accepted ingestion. Follow the documented dependency → API readiness → worker/web order.
- `railway restart` restarted PostgreSQL but its CLI watcher hung. Provider logs confirmed shutdown/restart at 12:20:55–56 UTC; only the hanging client was stopped. The other seven restarts were acknowledged through the inspected `deploymentRestart` GraphQL mutation. The public after probe then passed.
- An initial all-service metrics query failed while services were still initializing; the subsequent snapshot succeeded.

## Resource observation

Railway's post-startup snapshot reports approximately 1,490 MB RAM and 0.031 vCPU across the eight services; E5 accounts for about 1,123 MB. This is a short evaluation snapshot, not a load test or monthly invoice. At [Railway's published resource rates](https://docs.railway.com/pricing/plans), holding that snapshot constant would be roughly $15/month for memory and CPU, excluding storage, egress and plan/credit effects. Real cost changes with uptime and workload. Detailed per-service values and capture window are in the JSON evidence.

## Remaining external dependency: custom domain

`quivr.thevibecompany.co` is registered on the web service, port 3000. Railway still reports missing CNAME and pending ownership/TLS. Authoritative DNS is Cloudflare (`trey.ns.cloudflare.com`, `emerie.ns.cloudflare.com`); the connected Vercel account does not control the authoritative zone. No Cloudflare credential was available during this iteration.

Create these records in `thevibecompany.co` (CNAME as DNS-only while verifying):

| Type | Name | Value |
| --- | --- | --- |
| CNAME | `quivr` | `fiifkuin.up.railway.app` |
| TXT | `_railway-verify.quivr` | `railway-verify=498511b88c633df7faffaf5219728790eaeb19d04018cfa4aa71ce571b34ce87` |

After publishing, inspect `railway domain status quivr.thevibecompany.co --service web --json`, then run the public probe on the requested HTTPS hostname. THE-664 remains open until this final DNS/TLS verification succeeds. The working Railway URL is available meanwhile.
