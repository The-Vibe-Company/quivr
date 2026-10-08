# Quivr Search demo (THE-663)

React/TypeScript frontend adapted from [Quivr PR #3714](https://github.com/The-Vibe-Company/quivr/pull/3714), directory `quivr-search/` at commit `63d7fc52035190732b2c810c63645a295ece672e`, now a monitoring dashboard (THE-765); ingestion, search and source reading now use the real V2 API. No simulated Agent answers, corpus, scores, totals or pagination remain.

## Run locally

Prerequisites: the repository's Linux/Docker/Go/Python setup, Node 22.18+ (its tests load TypeScript directly) and npm (see root README). From the repository root:

```sh
make demo
```

Open http://127.0.0.1:5183. The command builds the frontend, starts isolated real dependencies, migrates the database and serves the production bundle. First startup downloads the pinned E5 model (~958 MB). Ctrl+C stops services; texts persist across runs. `make demo-reset` deletes **only this demo's** volumes. Set `DEMO_PORT` to use another port. Optional `DEMO_PASSWORD` enables a shared demo password locally.

The app is a monitoring dashboard with four tabs, **Fil**, **Alertes**, **Sources** and **Admin**, shown as icons in a slim rail on the left (each name is its tooltip), and a search box at the top (`/` or Ctrl+K). At the bottom of the rail sit the light/dark switch (kept in this browser), **Ajouter du texte** and the live dot that pauses arrivals. **Ajouter du texte** pastes a note: it is limited to 256 KiB, whitespace and Unicode are preserved, drafts and retry identity survive reloads within the same browser tab, and ambiguous network failures reuse the same ingestion identity.

The **Explorer** (`?view=explorer`), a test page with no tab that opens from its address only, is a reading room for the corpora the demo reads (`QUIVR_DEMO_CORPORA`). In the top bar, a search field looks within the corpus picked (`POST /v0/search` under the same filters, through `/demo/explore?q=`, the best 50 documents), and a switcher at the right picks one corpus or **Tous**, each with its document count (`GET /v0/records/count`). Below it, a timeline counts documents per day, month or year by `metadata.published_at` (`/demo/explore/facets`, `POST /v0/facets`): the caption reads the bar under the pointer, dragging across it picks a range of whole periods, which filters the list, and **Zoomer sur la période** narrows the span shown (`window=`), the step following the span. The range is counted outside itself so the documents around it stay drawn. Active filters show as pills next to the live count: the corpus total without filters, the dated documents every filter keeps with them, or a search's hits ("premiers résultats" once its 50 passages are reached). Three columns follow. Facets on the left: Langue, Sujets, Pays, a corpus's own fields (its typed mappings with the `filter` role) when it is picked alone, then the other common fields. Each value shows its count and a bar against the largest, up to 100 values, a field's own filter left out of its counts. A notice names the corpora a filter excluded, from the engine's `excluded_corpora`. In the centre, the documents (`GET /v0/records` with `corpus_ids`, `order=accepted_at_desc` and `metadata` predicates, 25 at a time, the next page loading as the list scrolls). The engine lists by arrival, so the rows loaded are shown by publication date (arrival when undated), under a day header that stays on top with the sort; a search sorted by relevance keeps its rank instead, in one group with each row dated. Each row has its time, language, version when above v1, headline and subjects. ↑/↓ move the selection and Enter opens the document. On the right, a preview: language, date, **corrigée** when it has several Versions, headline, lede, place, subjects, word count, each Version with what changed from the one before, and the source on demand. The address keeps the corpora, search, filters (`f=field:value`), range, span and selected document. Below 1100 px the page is one column, the switcher scrolls sideways and a row opens its document. A document's own page (`&record=`) shows its text, every metadata field, a word-by-word diff and **Source brute**. The engine lists no Record's Versions and serves no source file's bytes yet, so versions are those the demo read since it started, and the source shows what the engine keeps of it (extensions, provenance, files' type, size and SHA-256). Corpora indexed before metadata filtering need a rebuild before their values can be counted.

### Fil

The **Fil** tab (default; `?view=veille` still works) lists everything entering the demo corpus as a timeline, or the corpora picked in its **Corpus** menu (shown when the demo reads several; `&corpora=`, a row of another corpus names it), newest first, in foldable moments: **Dernière heure**, **Aujourd’hui**, **Hier**, **7 derniers jours** and **Plus ancien**. Each article hangs on the line by its source's logo: the icon the site behind an RSS source declares (its web app manifest's first, then its page's; a home page that refuses the server is replaced by the feed's first article), fetched by the server under the same private-address refusal as feed discovery, kept when it is a raster image of at least 32 px (a smaller one blurs once enlarged) or an SVG served as one (refused when it carries scripts), cached a day and served from this origin under a sandbox policy that lets it run nothing (`/demo/sources/logo/{connector}`), with the source's initial when there is none. A row shows the title on one line (whole in a tooltip), the source, the time and the alerts that caught it; a click on an alert filters on it. The server builds the feed from the change feed and catalog (`feed.mjs`; needs `changes:read`), with the RSS item's link (http/https only) and the date of a new Version of an article it already had. Time is when Quivr accepted the item’s current Version (`accepted_at` on the Version read), so it survives a restart of the web server, which then rescans the catalog newest first (`order=accepted_at_desc`). The list holds the latest 300 articles; its end says older ones are a day away. New articles arrive live, marked unread by a dot; pausing arrivals (**En direct** / **En pause**) holds them until resumed. **Non lus** is kept in this browser only (localStorage). The bar on top filters by **Tout** / **Non lus**, a day, alerts and sources, several alerts or sources at once. The **Date** menu lists every day back to the first article (up to about a year), in the browser's time zone, with Quivr's counts (`GET /v0/records/count` through `/demo/feed/days`, cached a minute by the server); **Tout** is the collection's total, or the day's, when no source or alert is picked. A day picked is read from Quivr newest first, a page at a time as you scroll (`/demo/feed/page`: the date listing between that day's two midnights), with the other filters applied in the browser; a source can be hidden from the feed in this browser and stays in search results. Every other number (**Non lus**, the counts of the alert and source menus, the bars once a filter is set) is counted by the server over the articles it has indexed, not over the loaded ones (`POST /demo/feed/stats`). `catalog.mjs` indexes each Record's source, current Version and acceptance hour from the date listing: one hour at a time, newest day first, at most four calls to the core at a time, starting with the first request after the feed's own catalog scan. Numbers are partial (`building`) until it is done, and stay partial (`partial`) when more than 200,000 articles, or articles older than a year, are left out. The change stream keeps the index current, and it is rebuilt every six hours. In a time zone with a half-hour offset, an article near midnight can count on the next day. On the right, the ten topics of the last seven days or of the day picked, over the newest 20,000 titles of the period and of the sources and alerts picked (`/demo/feed/topics`; titles are cached by Version, up to 30,000), and the articles per day over a week, or per hour once a day is chosen. An article opens in a panel over the right of the page; Escape, a click outside it or a second click on the article closes it.

Search covers the corpora the Fil spans. It is lexical, or hybrid with **Idées proches**, which marks articles found by meaning only as **Même sujet, autres mots**; **Créer une alerte** turns the query into a keyword alert. Sources picked in the **Sources** menu while searching are searched on their own: the engine ranks those sources' articles before keeping the top 50 (`filter.source_namespaces`, relayed unchanged by the facade). On a corpus the engine cannot filter yet (`source_filter_unavailable`, until it is rebuilt), the demo narrows the top 50 of every source instead. When the engine lists a `deep` profile served by a plugin (`GET /v0/search/profiles`, relayed by the facade), **Recherche approfondie** sends `profile: deep` with hybrid candidates; each result shows the re-ranker's probability that it answers, or **Non re-classé** when the re-ranker was unavailable, and a line gives the time and paid calls (`usage`). It starts off at every visit and is never kept in the address or in storage; [Re-rank with Jev](../docs-site/run-quivr/rerank-with-jev.mdx) pins such a plugin. The reader shows the text as collected, why an alert caught the article, a correction notice that says what changed word by word and opens the earlier text, **Sur le même sujet** (a semantic search seeded by the article) and **Ouvrir l’original**. ↑ ↓ open the next article and Échap closes it.

### Sources

The **Sources** tab (`?view=sources`) collects news sites into the demo corpus (see
[From the web interface](#sources)). **Ajouter une source**, at the top right (a **+** on a phone) or on the
last card, opens a dialog:

1. Paste a site address or a feed address. Once you pause typing (or press Entrée), the
   server fetches it, recognises a feed or finds the feeds the page advertises
   (`<link rel="alternate" type="application/rss+xml">`, Atom or JSON Feed), and
   refuses private, loopback and link-local addresses.
2. If the page advertises several feeds, pick one. The name comes from the feed title;
   choose how often to check it (5 min to 1 h, never below the deployment's minimum).
3. **Commencer la collecte** creates an `rss` Connector Instance whose Source
   Namespace is that name. Its articles become searchable after the first poll.

Suggested feeds, set by the deployment, add in one click from the same dialog, which closes on the new card. Each source is a card: its logo, its health or its last article in plain words (on hover, its interval while active, its pause date while paused), its articles per day, in the feed and caught by an alert, and its last seven days, counted by the server over its indexed articles (`/demo/sources/stats`). A switch pauses or resumes it; its **…** menu renames it, opens its settings or removes it. **Réglages**, also opened by its name, changes its name and how often it is checked, saved together, shows its feed address and its credential status (a credential is replaced, never shown), and removes it. A name given to a source is kept by the web server (`POST /demo/sources/rename`, in `DEMO_STATE_FILE`) and shown everywhere in the app; the Source Namespace, which Records and alerts use, does not change, and an empty name gives it back. Pausing disables the instance. Resuming creates a new instance on the same Source Namespace, because the core cannot re-enable one; articles already collected keep their identity. Removing disables every instance of the source and hides them; collected articles stay searchable. **Réessayer** on a failing source checks it now (`POST /v0/connectors/{id}/runs`) and shows the result.

`make demo` also enables the test `fixture` kind, under **Ajouter un connecteur
d’un autre type**. Its token field accepts any value, except values starting with
`fixture-revoked`, which produce an access error. The demo uses its own key and
Organization, so texts added before this change are not shown. The local stack lets
the core reach private addresses; the web server still refuses them, so private
feeds get the same error as in production.

### Alertes

The **Alertes** tab (`?view=alerts`) sets alerts on new articles, from RSS or added by hand. A table lists them (their sources' logos, last seven days, count with the unread ones, latest catch, creation date, state). The selected alert's sheet shows its words or description, what it caught in total and over seven days against the seven before, how often, its share of the articles received, a trend chart, its sources and arrival hours; beside it, the articles it caught, live, each opening in the reader over the page. The facade notes when it creates an alert, since the core dates neither Subscriptions nor Matches; other times come from the server's index, which dates what each alert caught. The sheet's switch pauses or resumes the alert and its **…** menu deletes it.

**Nouvelle alerte** and the pencil open the form in a panel, whose name fills from the words until it is written. A [keyword alert](https://docs.quivr.thevibecompany.co/guides/keyword-alerts) is built like a search engine's advanced search: **Tous ces mots**, **Cette phrase exacte**, **Au moins un de ces mots**, **Aucun de ces mots** (`NOT`) and the sources to watch (`source:` filters, picked in the feed's source menu); the query and a sentence saying what it will catch show live under the fields (`src/lib/queryBuilder.ts`, `src/lib/explain.ts`). There is no proximity field: the alerts plugin has no such operator. **Écrire une requête avancée** edits the query by hand, with buttons that write the syntax, examples, and a mistake marked where it is (`src/lib/notation.ts` ports the plugin's parser); **Revenir au formulaire guidé** takes it back while the fields can show it, and otherwise a note says why and the query stays as written. With `DEMO_DESCRIBED_ALERTS`, **Un sujet décrit** offers [described alerts](https://docs.quivr.thevibecompany.co/guides/described-alerts): a sentence judged by Jev, whose note says article text goes to TypeSafe and a catch can take a minute; they can watch chosen sources too. Without it, a line says why the choice is missing. The form previews what the alert would have caught among the newest articles and about how often, saving nothing: as typed, or on a click for described alerts, since each article is a classifier call. Each caught article shows the matched words or the score. `alerts.mjs` explains how alerts are stored and read.

### Admin

The **Admin** tab (`?view=admin`, read-only) shows what goes through Quivr right now, on one screen: one line says whether all is well with the hour's received-to-searchable p95, documents per minute, documents waiting and errors; the pipeline follows, one card per step with the plugins running it; below, **Ce qui passe dans le tuyau** (switchable to **Plugins**, **Utilisation** or **En base**, scrolling inside) beside the day at a glance (documents received per source, searches per mode). The live flow lists the demo corpus's 50 latest documents with one cell per step (received, cut, searchable, vectors, alerts): its duration, or how long it has been running. A step slower than its own p95 over the day turns amber. A row opens the document's timeline, one bar per step on a shared time axis with the plugin that ran it. **En base** shows the documents stored in the demo corpus: the total, today's count and one column per day since the first document (a year at most), empty days at zero, with the table by day; withdrawn documents count, and each document counts on the day its current version was accepted, in the browser's time zone.

`admin.mjs` reads `GET /v0/admin/documents` and the timelines with the server key, refreshes on each change the Fil's stream reports (every 5 s otherwise) and relays the step rollups (`/v0/admin/stats/*`) to the sections, which share `AdminSection` and `useAdminStats`. `/demo/admin/history?tz=` counts the documents per local day with `GET /v0/records/count` (one read per day, at most a year, today cached 30 s and past days 10 min). It needs `observability:read` on a key for all Corpora; without it the tab says the view is off.

The pipeline (**Goulots par étape**), over 1 h, 24 h or 7 days, gives each step its p50 and p95, its p95 as dots on one scale, the documents waiting at it and the state of its plugins, and names the step whose recent p95 doubled against its earlier buckets, else a queue waiting over a minute and twice its p95. **Plugins** lists the plugins of the engine's active plan (`admin-plugins.mjs` relays `GET /v0/admin/active-plugins`) with calls per minute, p95, errors over time, last error, their sources for connectors, and a state from their latest minutes of calls: *En panne*, *Dégradé*, *Opérationnel* or *Au repos*. `src/lib/health.ts` holds these rules; `tests/health.spec.ts` checks them.

**Utilisation** covers the last 24 hours or 7 days: documents received per source (five stacked, the rest folded into *autres sources*), searches per mode with their p50 and p95, articles caught by alerts, and the most frequent queries with their trend. Top queries need the core's `observability.record_query_text`, on in `make demo` and on Railway; without it the block says how to turn it on.

## Frontend development and checks

With `make demo` running, `npm run dev --prefix quivr-search` serves Vite on 5182 and proxies to the facade on 5183. Change the proxy target in `vite.config.ts` if you changed `DEMO_PORT`. Design tokens sit at the top of `src/styles.css` and `src/dashboard.css` (the dashboard's layout); the Geist font is bundled from `src/fonts/` (SIL Open Font License, `src/fonts/OFL.txt`), so the page loads no font from another origin; loading, empty and error states come from `src/components/ui.tsx`. `tests/dashboard.spec.ts` runs the dashboard's flows against a fake engine behind the facade routes (`tests/fake-engine.ts`). `node scripts/fake-core.mjs` serves a synthetic core (12,000 articles over ten days) to run and measure the facade locally; the facade tests use it too.

```sh
npm run typecheck --prefix quivr-search
make verify-demo
```

`make verify` also runs these browser/HTTP tests against an isolated real core. Feeds come from a local test site (`scripts/fake_feeds.py`), so no test reaches the internet. Chromium system dependencies can be installed with `npx --yes --package=@playwright/test@1.63.0 playwright install-deps chromium`. Screenshots and reports live under `.scratch/quivr-demo-verify-*/`. Browser traces may contain demo passwords/texts and remain local; CI uploads only screenshots and reports. Test data are synthetic.

### Speed budgets

`perf/budgets.json` sets them: every page's largest paint (LCP) within 2 s and layout shift (CLS) at most 0.1 on a mid laptop over a 4G-class link, each scripted interaction (open, step through and close an article, filter on an alert’s tag and clear it, switch tabs, type and clear a search) answered within 200 ms (INP), the JavaScript a first visit loads, and the p95 of the facade's main endpoints. `npm run build` checks the bundle sizes, so CI does too. The rest is measured on a machine, never in CI:

```sh
make demo-perf                                   # seeds a stack of its own, measures it, fails on a broken budget
QUIVR_DEMO_URL=https://… QUIVR_DEMO_PASSWORD=… npm run perf --prefix quivr-search   # any deployment; by default adds nothing but its searches
```

`make demo-perf` adds 12 synthetic sources of 100 articles and six alerts, then also times how long an alert takes to catch a new text (`PERF_ALERT_LAG=1`, which writes an alert and five texts to the demo it measures); its report is `.scratch/quivr-demo-perf-*/perf.json`. A remote deployment's endpoint times include the network: they are reported, not checked. Its searches (`port grève`, 20 per mode) show in Admin's top queries where the core records query text. The facade compresses its answers (brotli or gzip), revalidates reads with an ETag, and names in `Server-Timing` the time and calls each one spent in the core. The Fil ships with the page; the other tabs load when the browser is idle.

## Server configuration

`npm run build --prefix quivr-search` then `npm start --prefix quivr-search` uses:

| Variable | Meaning |
| --- | --- |
| `QUIVR_API_URL` | Private core URL, no trailing route |
| `QUIVR_API_KEY` | Server-only core credential with corpus read/write, content read/write and search permissions. Add `connectors:read`, `connectors:write` and `changes:read` to enable the Sources tab; the Veille tab needs `changes:read`, the Admin tab `observability:read` |
| `QUIVR_DEMO_CORPUS_ID` | Optional existing demo corpus; otherwise created idempotently |
| `QUIVR_DEMO_CORPORA` | Optional comma-separated IDs of other corpora the demo reads: the Fil, search and the Explorer can span them (at most 16 at a time); sources, alerts and added texts stay on the demo corpus. Every other corpus is refused. The key needs read, search and `changes:read` on them |
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
