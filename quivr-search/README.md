# Quivr Search demo (THE-663)

React/TypeScript frontend adapted from [Quivr PR #3714](https://github.com/The-Vibe-Company/quivr/pull/3714), directory `quivr-search/` at commit `63d7fc52035190732b2c810c63645a295ece672e`, now a monitoring dashboard (THE-765); ingestion, search and source reading now use the real V2 API. No simulated Agent answers, corpus, scores, totals or pagination remain.

## Run locally

Prerequisites: the repository's Linux/Docker/Go/Python setup, Node 22+ and npm (see root README). From the repository root:

```sh
make demo
```

Open http://127.0.0.1:5183. The command builds the frontend, starts isolated real dependencies, migrates the database and serves the production bundle. First startup downloads the pinned E5 model (~958 MB). Ctrl+C stops services; texts persist across runs. `make demo-reset` deletes **only this demo's** volumes. Set `DEMO_PORT` to use another port. Optional `DEMO_PASSWORD` enables a shared demo password locally.

The app is a monitoring dashboard with four tabs, **Fil**, **Alertes**, **Sources** and **Admin**, shown as icons in a slim rail on the left (each name is its tooltip), and a search box at the top (`/` or Ctrl+K). At the bottom of the rail sit the light/dark switch (kept in this browser), **Ajouter du texte** and the live dot that pauses arrivals. **Ajouter du texte** pastes a note: it is limited to 256 KiB, whitespace and Unicode are preserved, drafts and retry identity survive reloads within the same browser tab, and ambiguous network failures reuse the same ingestion identity.

### Fil

The **Fil** tab (default; `?view=veille` still works) lists everything entering the demo corpus as a timeline, newest first, in foldable moments: **Dernière heure**, **Aujourd’hui**, **Hier**, **7 derniers jours** and **Plus ancien**. Each article hangs on the line by its source's logo: the icon the site behind an RSS source declares, fetched by the server under the same private-address refusal as feed discovery, kept only when it is a raster image, cached a day and served from this origin (`/demo/sources/logo/{connector}`), with the source's initial when there is none. A row shows the title on one line (whole in a tooltip), the source, the time and the alerts that caught it; a click on an alert filters on it. The server builds the feed from the change feed and catalog (`feed.mjs`; needs `changes:read`), with the RSS item's link (http/https only) and the date of a new Version of an article it already had. Time is when Quivr accepted the item’s current Version (`accepted_at` on the Version read), so it survives a restart of the web server, which then rescans the catalog newest first (`order=accepted_at_desc`). The list holds the latest 300 articles; its end says older ones are a day away. New articles arrive live, marked unread by a dot; pausing arrivals (**En direct** / **En pause**) holds them until resumed. **Non lus** is kept in this browser only (localStorage). The bar on top filters by **Tout** / **Non lus**, a day, alerts and sources, several alerts or sources at once. The **Date** menu lists every day back to the first article, in the browser's time zone, with Quivr's counts (`GET /v0/records/count` through `/demo/feed/days`, cached a minute by the server); **Tout** is the collection's total, or the day's, when no source or alert is picked. A day picked is read from Quivr newest first, a page at a time as you scroll (`/demo/feed/page`: the date listing between that day's two midnights), with the other filters applied in the browser; a source can be hidden from the feed in this browser and stays in search results. On the right, the ten topics of the chosen period (a click searches one) and the articles per day over a week, from the same counts, or per hour once a day is chosen. An article opens in a panel over the right of the page; Escape, a click outside it or a second click on the article closes it.

Search is lexical, or hybrid with **Idées proches**, which marks articles found by meaning only as **Même sujet, autres mots**; **Créer une alerte** turns the query into a keyword alert. Sources picked in the **Sources** menu while searching are searched on their own: the engine ranks those sources' articles before keeping the top 50 (`filter.source_namespaces`, relayed unchanged by the facade). On a corpus the engine cannot filter yet (`source_filter_unavailable`, until it is rebuilt), the demo narrows the top 50 of every source instead. When the engine lists a `deep` profile served by a plugin (`GET /v0/search/profiles`, relayed by the facade), **Recherche approfondie** sends `profile: deep` with hybrid candidates; each result shows the re-ranker's probability that it answers, or **Non re-classé** when the re-ranker was unavailable, and a line gives the time and paid calls (`usage`). It starts off at every visit and is never kept in the address or in storage; [Re-rank with Jev](../docs-site/guides/rerank-with-jev.mdx) pins such a plugin. The reader shows the text as collected, why an alert caught the article, a correction notice that says what changed word by word and opens the earlier text, **Sur le même sujet** (a semantic search seeded by the article) and **Ouvrir l’original**. ↑ ↓ open the next article and Échap closes it.

### Sources

The **Sources** tab (`?view=sources`) collects news sites into the demo corpus (see
[From the web interface](#sources)):

1. Paste a site address or a feed address. Once you pause typing (or press Entrée), the
   server fetches it, recognises a feed or finds the feeds the page advertises
   (`<link rel="alternate" type="application/rss+xml">`, Atom or JSON Feed), and
   refuses private, loopback and link-local addresses.
2. If the page advertises several feeds, pick one. The name comes from the feed title;
   choose how often to check it (5 min to 1 h, never below the deployment's minimum).
3. **Commencer la collecte** creates an `rss` Connector Instance whose Source
   Namespace is that name. Its articles become searchable after the first poll.

Suggested feeds, set by the deployment, add in one click from the last card of the page. Each source is a card: its logo, its health in plain words, its last article, its interval, its articles per day, in the feed and caught by an alert, and its last seven days. A switch pauses or resumes it; its **…** menu renames or removes it. A name given to a source is kept by the web server (`POST /demo/sources/rename`, in `DEMO_STATE_FILE`) and shown everywhere in the app; the Source Namespace, which Records and alerts use, does not change, and an empty name gives it back. Pausing disables the instance. Resuming creates a new instance on the same Source Namespace, because the core cannot re-enable one; articles already collected keep their identity. Removing disables every instance of the source and hides them; collected articles stay searchable. **Réessayer** on a failing source checks it now (`POST /v0/connectors/{id}/runs`) and shows the result.

`make demo` also enables the test `fixture` kind, under **Ajouter un connecteur
d’un autre type**. Its token field accepts any value, except values starting with
`fixture-revoked`, which produce an access error. The demo uses its own key and
Organization, so texts added before this change are not shown. The local stack lets
the core reach private addresses; the web server still refuses them, so private
feeds get the same error as in production.

### Alertes

The **Alertes** tab (`?view=alerts`) sets alerts on new articles, from RSS or added by hand. A table lists them (their sources' logos, last seven days, count with the unread ones, latest catch, creation date, state). The selected alert's sheet shows its words or description, what it caught in total and over seven days against the seven before, how often, its share of the articles received, a trend chart, its sources and arrival hours; beside it, the articles it caught, live, each opening in the reader over the page. The facade notes when it creates an alert, since the core dates neither Subscriptions nor Matches; other times come from the feed. The sheet's switch pauses or resumes the alert and its **…** menu deletes it.

**Nouvelle alerte** and the pencil open the form in a panel, as one sentence whose name fills from the words until it is written. A [keyword alert](https://docs.quivr.thevibecompany.co/guides/keyword-alerts) is written as words to watch (any or all of them), words to ignore (`NOT`) and the sources to watch (`source:` filters, picked in the feed's source menu); any other query, such as `(port OR quai) AND grève`, goes in **Écrire une requête avancée**, and the form shows how Quivr reads it (`src/lib/alertForm.ts`; `src/lib/notation.ts` ports the plugin's parser). With `DEMO_DESCRIBED_ALERTS`, **Un sujet décrit** offers [described alerts](https://docs.quivr.thevibecompany.co/guides/described-alerts): a sentence judged by Jev, whose note says article text goes to TypeSafe and a catch can take a minute; they can watch chosen sources too. Without it, a line says why the choice is missing. The form previews what the alert would have caught among the newest articles and about how often, saving nothing: as typed, or on a click for described alerts, since each article is a classifier call. Each caught article shows the matched words or the score. `alerts.mjs` explains how alerts are stored and read.

### Admin

The **Admin** tab (`?view=admin`, read-only) shows what goes through Quivr right now, on one screen: one line says whether all is well with the hour's received-to-searchable p95, documents per minute, documents waiting and errors; the pipeline follows, one card per step with the plugins running it; below, **Ce qui passe dans le tuyau** (switchable to **Plugins** or **Utilisation**, scrolling inside) beside the day at a glance (documents received per source, searches per mode). The live flow lists the demo corpus's 50 latest documents with one cell per step (received, cut, searchable, vectors, alerts): its duration, or how long it has been running. A step slower than its own p95 over the day turns amber. A row opens the document's timeline, one bar per step on a shared time axis with the plugin that ran it.

`admin.mjs` reads `GET /v0/admin/documents` and the timelines with the server key, refreshes on each change the Fil's stream reports (every 5 s otherwise) and relays the step rollups (`/v0/admin/stats/*`) to the sections, which share `AdminSection` and `useAdminStats`. It needs `observability:read` on a key for all Corpora; without it the tab says the view is off.

The pipeline (**Goulots par étape**), over 1 h, 24 h or 7 days, gives each step its p50 and p95, its p95 as dots on one scale, the documents waiting at it and the state of its plugins, and names the step whose recent p95 doubled against its earlier buckets, else a queue waiting over a minute and twice its p95. **Plugins** lists the plugins of the engine's active plan (`admin-plugins.mjs` relays `GET /v0/admin/active-plugins`) with calls per minute, p95, errors over time, last error, their sources for connectors, and a state from their latest minutes of calls: *En panne*, *Dégradé*, *Opérationnel* or *Au repos*. `src/lib/health.ts` holds these rules; `tests/health.spec.ts` checks them.

**Utilisation** covers the last 24 hours or 7 days: documents received per source (five stacked, the rest folded into *autres sources*), searches per mode with their p50 and p95, articles caught by alerts, and the most frequent queries with their trend. Top queries need the core's `observability.record_query_text`, on in `make demo` and on Railway; without it the block says how to turn it on.

## Frontend development and checks

With `make demo` running, `npm run dev --prefix quivr-search` serves Vite on 5182 and proxies to the facade on 5183. Change the proxy target in `vite.config.ts` if you changed `DEMO_PORT`. Design tokens sit at the top of `src/styles.css` and `src/dashboard.css` (the dashboard's layout); the Geist font is bundled from `src/fonts/` (SIL Open Font License, `src/fonts/OFL.txt`), so the page loads no font from another origin; loading, empty and error states come from `src/components/ui.tsx`. `tests/dashboard.spec.ts` runs the dashboard's flows against a fake engine behind the facade routes (`tests/fake-engine.ts`).

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
| `QUIVR_API_KEY` | Server-only core credential with corpus read/write, content read/write and search permissions. Add `connectors:read`, `connectors:write` and `changes:read` to enable the Sources tab; the Veille tab needs `changes:read`, the Admin tab `observability:read` |
| `QUIVR_DEMO_CORPUS_ID` | Optional existing demo corpus; otherwise created idempotently |
| `DEMO_PASSWORD` | Shared demo password, required on public binds |
| `HOST`, `PORT` | Bind address/port; defaults `127.0.0.1:5183` |
| `DEMO_SECURE_COOKIE` | Set `true` behind HTTPS; cookie is always HttpOnly and SameSite=Strict |
| `DEMO_FEED_SUGGESTIONS` | Optional one-click feeds on the Sources tab: a JSON array of `{"title", "url"}` (at most 12; invalid entries are skipped with a warning). Empty by default |
| `DEMO_STATE_FILE` | Optional file keeping removed sources, source names, paused alerts and alert creation dates across restarts. `make demo` keeps it in the stack directory |
| `QUIVR_DEMO_DESTINATION_ID` | Webhook destination of the demo's Organization; enables the Alertes tab with the `monitoring:read` and `monitoring:write` permissions |
| `QUIVR_DEMO_ALERTS_EVALUATOR` | Alerts evaluator, `plugin@version`; default `alerts@0.2.0` |
| `DEMO_DESCRIBED_ALERTS` | `true` offers described alerts in the Alertes tab. Set it only where the core's `alerts` pin accepts the `described` kind (a classifier key is configured); otherwise the core refuses them at creation |
| `DEMO_FEED_PRIVATE_ORIGINS` | Tests only: comma-separated exact origins (`http://127.0.0.1:8080`) exempt from the private-address refusal, for a local test feed server. Never set it in production |

Example, with placeholder addresses to replace with the feeds you want to offer:

```sh
DEMO_FEED_SUGGESTIONS='[{"title":"Example News — World","url":"https://news.example.org/world/rss.xml"},{"title":"Example Tech","url":"https://tech.example.com/feed"}]'
```

Keep the real list in the deployment's settings, not in this repository.

Only the Node facade should be public. It serves the bundle, authenticates the shared demo session, and restricts core requests to the demo corpus and necessary routes. Core credentials never enter frontend build variables. Deploy behind HTTPS, with the original Host preserved, and use a dedicated core identity/organization for the demo. This is an evaluation space shared by everyone with its password, not individual user accounts.

Railway deployment and `quivr.thevibecompany.co` are tracked separately in [THE-664](https://linear.app/thevibecompany/issue/THE-664).
