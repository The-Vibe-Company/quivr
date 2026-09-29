# Quivr Search demo (THE-663)

React/TypeScript frontend adapted from [Quivr PR #3714](https://github.com/The-Vibe-Company/quivr/pull/3714), directory `quivr-search/` at commit `63d7fc52035190732b2c810c63645a295ece672e`. The original visual identity is retained; ingestion, search and source reading now use the real V2 API. No simulated Agent answers, corpus, scores, totals or pagination remain.

## Run locally

Prerequisites: the repository's Linux/Docker/Go/Python setup, Node 22+ and npm (see root README). From the repository root:

```sh
make demo
```

Open http://127.0.0.1:5183. The command builds the frontend, starts isolated real dependencies, migrates the database and serves the production bundle. First startup downloads the pinned E5 model (~958 MB). Ctrl+C stops services; texts persist across runs. `make demo-reset` deletes **only this demo's** volumes. Set `DEMO_PORT` to use another port. Optional `DEMO_PASSWORD` enables a shared demo password locally.

Click **Ajouter du texte**, paste a note, submit, follow search availability, then open the source or search for it. Hybrid search is the default; lexical and semantic modes are selectable. Inline text is limited to 256 KiB; whitespace and Unicode are preserved. Drafts and retry identity survive reloads within the same browser tab. Ambiguous network failures reuse the same ingestion identity.

### Sources

The **Sources** tab (`?view=sources`) collects news sites into the demo corpus (see
[From the web interface](../docs/connectors/README.md#from-the-web-interface)):

1. Paste a site address or a feed address and press **Ajouter**. The server fetches
   the address, recognises a feed or finds the feeds the page advertises
   (`<link rel="alternate" type="application/rss+xml">`, Atom or JSON Feed), and
   refuses private, loopback and link-local addresses.
2. If the page advertises several feeds, pick one. The name (the feed title) and
   the polling interval come prefilled.
3. **Commencer la collecte** creates an `rss` Connector Instance whose Source
   Namespace is that name. Its articles become searchable after the first poll.

Suggested feeds, set by the deployment, add in one click. Each source shows its
health (active, silent, failing, paused), its last article and its interval, and
can be paused, resumed or removed. Pausing disables the instance. Resuming creates
a new instance on the same Source Namespace, because the core cannot re-enable one;
articles already collected keep their identity. Removing disables every instance
of the source and hides them; collected articles stay searchable.

`make demo` also enables the test `fixture` kind, under **Ajouter un connecteur
d’un autre type**. Its token field accepts any value, except values starting with
`fixture-revoked`, which produce an access error. The demo uses its own key and
Organization, so texts added before this change are not shown. The local stack lets
the core reach private addresses; the web server still refuses them, so private
feeds get the same error as in production.

### Veille

The **Veille** tab (`?view=veille`) lists everything entering the demo corpus, newest
first, with source (namespace, or **Ajouté à la main**), time, title and excerpt. New
items arrive live, chips filter by source and an item opens in the document view. The
server builds it from the change feed and catalog (`feed.mjs`; needs `changes:read`).
Time is when the server saw an item arrive; older items show their RSS date or **Déjà présent**.

### Alertes

The **Alertes** tab (`?view=alerts`) sets [keyword alerts](../docs/keyword-alerts.md) on new
articles, from RSS or added by hand. Type a query such as `orage AND (grêle OR vent) NOT
football` and the page shows how it reads it. Each alert lists what it caught, live, with the
matched words and where; it can be paused, resumed, edited and deleted. `src/lib/notation.ts`
ports the plugin's parser; `alerts.mjs` explains how alerts are stored and read.

## Frontend development and checks

With `make demo` running, `npm run dev --prefix quivr-search` serves Vite on 5182 and proxies to the facade on 5183. Change the proxy target in `vite.config.ts` if you changed `DEMO_PORT`. Design tokens (colour, spacing, type, radii, motion) sit at the top of `src/styles.css`; every page builds its header, live badge and loading, empty and error states from `src/components/ui.tsx`.

```sh
npm run typecheck --prefix quivr-search
make verify-demo
```

`make verify` also runs these browser/HTTP tests against an isolated real core. Feeds come from a local test site (`scripts/fake_feeds.py`), so no test reaches the internet. Chromium system dependencies can be installed with `npx --yes --package=@playwright/test@1.63.0 playwright install-deps chromium`. Screenshots and reports live under `.scratch/quivr-demo-verify-*/`. Browser traces may contain demo passwords/texts and remain local; CI uploads only screenshots and reports. Test data are synthetic.

## Server configuration

`npm run build --prefix quivr-search` then `npm start --prefix quivr-search` uses:

| Variable | Meaning |
| --- | --- |
| `QUIVR_API_URL` | Private core URL, no trailing route |
| `QUIVR_API_KEY` | Server-only core credential with corpus read/write, content read/write and search permissions. Add `connectors:read`, `connectors:write` and `changes:read` to enable the Sources tab; the Veille tab needs `changes:read` |
| `QUIVR_DEMO_CORPUS_ID` | Optional existing demo corpus; otherwise created idempotently |
| `DEMO_PASSWORD` | Shared demo password, required on public binds |
| `HOST`, `PORT` | Bind address/port; defaults `127.0.0.1:5183` |
| `DEMO_SECURE_COOKIE` | Set `true` behind HTTPS; cookie is always HttpOnly and SameSite=Strict |
| `DEMO_FEED_SUGGESTIONS` | Optional one-click feeds on the Sources tab: a JSON array of `{"title", "url"}` (at most 12; invalid entries are skipped with a warning). Empty by default |
| `DEMO_STATE_FILE` | Optional file keeping removed sources and paused alerts across restarts. `make demo` keeps it in the stack directory |
| `QUIVR_DEMO_DESTINATION_ID` | Webhook destination of the demo's Organization; enables the Alertes tab with the `monitoring:read` and `monitoring:write` permissions |
| `QUIVR_DEMO_ALERTS_EVALUATOR` | Keyword alerts evaluator, `plugin@version`; default `alerts@0.2.0` |
| `DEMO_FEED_PRIVATE_ORIGINS` | Tests only: comma-separated exact origins (`http://127.0.0.1:8080`) exempt from the private-address refusal, for a local test feed server. Never set it in production |

Example, with placeholder addresses to replace with the feeds you want to offer:

```sh
DEMO_FEED_SUGGESTIONS='[{"title":"Example News — World","url":"https://news.example.org/world/rss.xml"},{"title":"Example Tech","url":"https://tech.example.com/feed"}]'
```

Keep the real list in the deployment's settings, not in this repository.

Only the Node facade should be public. It serves the bundle, authenticates the shared demo session, and restricts core requests to the demo corpus and necessary routes. Core credentials never enter frontend build variables. Deploy behind HTTPS, with the original Host preserved, and use a dedicated core identity/organization for the demo. This is an evaluation space shared by everyone with its password, not individual user accounts.

Railway deployment and `quivr.thevibecompany.co` are tracked separately in [THE-664](https://linear.app/thevibecompany/issue/THE-664).
