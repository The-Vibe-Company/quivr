# Operations

Environments that deploy from `main` on every merge. The runbooks are `deploy/railway/README.md` and `docs/agents/documentation.md`; this page lists the coordinator's checks and the traps.

## Railway demo

- Services `api`, `worker` and `web` build from `main`; each has watch paths, so a merge that touches none of its paths shows `SKIPPED`. Check with `railway deployment list --service <name> --json` from a directory linked to the demo project (`railway link`).
- Logs: `railway logs --service worker` should show the pinned plugins (`plugins pinned …`) and each sidecar's `serving …` line. An `api` start ends with `process ready`.
- Smoke check inside the network: the images have no `curl`, but they have `python3`. Write a short `urllib` script locally, base64 it, and run it with `railway ssh --service api -- sh -c "echo <b64> | base64 -d | python3"`. Use `$QUIVR_API_KEY` for search (`POST /v0/search` answers `{items, retrieval_profile}`) and `$QUIVR_OPERATOR_KEY` for rebuilds and admin routes; the operator key has no search right. Never print either key.
- Rebuild a Corpus after an ingestion change: `POST /v0/corpora/{id}/rebuilds` with body `{"idempotency_key": "…"}`, then poll `GET /v0/operations/{id}` until `state` is `succeeded`, and read `GET /v0/corpora/{id}/vector-spaces`.
- A deploy that fails: read the build or deploy logs, fix forward with a ticket, and redeploy the previous deployment from the Railway dashboard if the demo is down.

## Docs site

Mintlify builds from `main`, subdirectory `/docs-site`, on its own after each merge; the "Mintlify Deployment" check on the merge commit reports the result. Confirm a few pages answer 200 on the public domain.

## Search quality lane

`measure-search.yml` runs nightly (about 90 minutes) and on demand with `gh workflow run measure-search.yml`. Read the `search-quality` artifact (`eval/out/report.md`), compare with the previous reference run named on the evaluation ticket, and post the table on the ticket that changed ranking.
