# Quivr Search demo (THE-663)

React/TypeScript frontend adapted from [Quivr PR #3714](https://github.com/The-Vibe-Company/quivr/pull/3714), directory `quivr-search/` at commit `63d7fc52035190732b2c810c63645a295ece672e`, now a monitoring dashboard (THE-765); ingestion, search and source reading now use the real V2 API. No simulated Agent answers, corpus, scores, totals or pagination remain.

## Run locally

Prerequisites: the repository's Linux/Docker/Go/Python setup, Node 22+ and npm (see root README). From the repository root:

```sh
make demo
```

Open http://127.0.0.1:5183. The command builds the frontend, starts isolated real dependencies, migrates the database and serves the production bundle. First startup downloads the pinned E5 model (~958 MB). Ctrl+C stops services; texts persist across runs. `make demo-reset` deletes **only this demo's** volumes. Set `DEMO_PORT` to use another port. Optional `DEMO_PASSWORD` enables a shared demo password locally.

The app is a monitoring dashboard with four tabs, **Fil**, **Alertes**, **Sources** and **Admin**, and a search box in the top bar (`/` or Ctrl+K). **Ajouter du texte** pastes a note: it is limited to 256 KiB, whitespace and Unicode are preserved, drafts and retry identity survive reloads within the same browser tab, and ambiguous network failures reuse the same ingestion identity.

### Fil

The **Fil** tab (default; `?view=veille` still works) lists everything entering the demo corpus, newest first, with source (namespace, or **Ajouté à la main**), time, title and the alerts that caught it. The server builds it from the change feed and catalog (`feed.mjs`; needs `changes:read`), with the RSS item's link (http/https only) and the date of a new Version of an article it already had. Time is when Quivr accepted the item’s current Version (`accepted_at` on the Version read), so it survives a restart of the web server. New articles arrive live; while an article is open, the list is scrolled or arrivals are paused (**En direct** / **En pause**), they wait behind a **N nouveaux articles** button. **Non lus** is kept in this browser only (localStorage). Chips filter the unread and caught articles; each alert and source on the right filters the feed too.

Search is lexical, or hybrid with **Idées proches**, which marks articles found by meaning only as **Même sujet, autres mots**; **Créer une alerte** turns the query into a keyword alert. A source picked on the right while searching is searched on its own: the engine ranks that source's articles before keeping the top 50 (`filter.source_namespaces`, relayed unchanged by the facade). On a corpus the engine cannot filter yet (`source_filter_unavailable`, until it is rebuilt), the demo narrows the top 50 of every source instead. The reader shows the text as collected, why an alert caught the article, a correction notice that says what changed word by word and opens the earlier text, **Sur le même sujet** (a semantic search seeded by the article) and **Ouvrir l’original**. ↑ ↓ open the next article and Échap closes it.

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

Suggested feeds, set by the deployment, add in one click. Each source shows its
health in plain words, its last article, its interval and its counts in the feed, and
can be paused, resumed or removed. Pausing disables the instance. Resuming creates
a new instance on the same Source Namespace, because the core cannot re-enable one;
articles already collected keep their identity. Removing disables every instance
of the source and hides them; collected articles stay searchable. **Réessayer** on a
failing source checks it now (`POST /v0/connectors/{id}/runs`) and shows the result.

`make demo` also enables the test `fixture` kind, under **Ajouter un connecteur
d’un autre type**. Its token field accepts any value, except values starting with
`fixture-revoked`, which produce an access error. The demo uses its own key and
Organization, so texts added before this change are not shown. The local stack lets
the core reach private addresses; the web server still refuses them, so private
feeds get the same error as in production.

### Alertes

The **Alertes** tab (`?view=alerts`) sets alerts on new articles, from RSS or added by hand.
A [keyword alert](https://docs.quivr.thevibecompany.co/guides/keyword-alerts) is written as words to watch (any or all of
them), words to ignore (`NOT`) and the sources to watch (`source:` filters); any other query,
such as `(port OR quai) AND grève`, goes in **Écrire une requête avancée**, and the form shows
how Quivr reads it (`src/lib/alertForm.ts`; `src/lib/notation.ts` ports the plugin's parser).
With `DEMO_DESCRIBED_ALERTS`, **Un sujet décrit** offers [described alerts](https://docs.quivr.thevibecompany.co/guides/described-alerts):
a sentence judged by Jev, whose note says article text goes to TypeSafe and a catch can take a
minute; they can watch chosen sources too. Without it, a line says why the choice is missing.
The form previews what the alert would have caught among the newest articles (saving nothing):
as typed, or on a click for described alerts, since each article is a classifier call. Each alert
lists what it caught, live, with the matched words or the score; it can be paused, resumed,
edited, renamed and deleted. `alerts.mjs` explains how alerts are stored and read.

### Admin

The **Admin** tab (`?view=admin`, read-only) shows what goes through Quivr right now. A sentence says whether all is well, above documents per minute, the received-to-searchable p95, the documents waiting and the errors of the hour, and documents per hour over 24 h. The live flow lists the demo corpus's 50 latest documents with one cell per step (received, cut, searchable, vectors, alerts): its duration, or how long it has been running. A step slower than its own p95 over the day turns amber. A row opens the document's timeline, one bar per step on a shared time axis with the plugin that ran it.

`admin.mjs` reads `GET /v0/admin/documents` and the timelines with the server key, refreshes on each change the Fil's stream reports (every 5 s otherwise) and relays the step rollups (`/v0/admin/stats/*`) to the sections, which share `AdminSection` and `useAdminStats`. It needs `observability:read` on a key for all Corpora; without it the tab says the view is off.

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
| `DEMO_STATE_FILE` | Optional file keeping removed sources and paused alerts across restarts. `make demo` keeps it in the stack directory |
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
