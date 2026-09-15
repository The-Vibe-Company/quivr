# Quivr Search demo (THE-663)

React/TypeScript frontend adapted from [Quivr PR #3714](https://github.com/The-Vibe-Company/quivr/pull/3714), directory `quivr-search/` at commit `63d7fc52035190732b2c810c63645a295ece672e`. The original visual identity is retained; ingestion, search and source reading now use the real V2 API. No simulated Agent answers, corpus, scores, totals or pagination remain.

## Run locally

Prerequisites: the repository's Linux/Docker/Go/Python setup, Node 22+ and npm (see root README). From the repository root:

```sh
make demo
```

Open http://127.0.0.1:5183. The command builds the frontend, starts isolated real dependencies, migrates the database and serves the production bundle. First startup downloads the pinned E5 model (~958 MB). Ctrl+C stops services; texts persist across runs. `make demo-reset` deletes **only this demo's** volumes. Set `DEMO_PORT` to use another port. Optional `DEMO_PASSWORD` enables a shared demo password locally.

Click **Ajouter du texte**, paste a note, submit, follow search availability, then open the source or search for it. Hybrid search is the default; lexical and semantic modes are selectable. Inline text is limited to 256 KiB; whitespace and Unicode are preserved. Drafts and retry identity survive reloads within the same browser tab. Ambiguous network failures reuse the same ingestion identity.

## Frontend development and checks

With `make demo` running, `npm run dev --prefix quivr-search` serves Vite on 5182 and proxies to the facade on 5183. Change the proxy target in `vite.config.ts` if you changed `DEMO_PORT`.

```sh
npm run typecheck --prefix quivr-search
make verify-demo
```

`make verify` also runs these browser/HTTP tests against an isolated real core. Chromium system dependencies can be installed with `npx --yes --package=@playwright/test@1.63.0 playwright install-deps chromium`. Screenshots and reports live under `.scratch/quivr-demo-verify-*/`. Browser traces may contain demo passwords/texts and remain local; CI uploads only screenshots and reports. Test data are synthetic.

## Server configuration

`npm run build --prefix quivr-search` then `npm start --prefix quivr-search` uses:

| Variable | Meaning |
| --- | --- |
| `QUIVR_API_URL` | Private core URL, no trailing route |
| `QUIVR_API_KEY` | Server-only core credential with corpus read/write, content read/write and search permissions |
| `QUIVR_DEMO_CORPUS_ID` | Optional existing demo corpus; otherwise created idempotently |
| `DEMO_PASSWORD` | Shared demo password, required on public binds |
| `HOST`, `PORT` | Bind address/port; defaults `127.0.0.1:5183` |
| `DEMO_SECURE_COOKIE` | Set `true` behind HTTPS; cookie is always HttpOnly and SameSite=Strict |

Only the Node facade should be public. It serves the bundle, authenticates the shared demo session, and restricts core requests to the demo corpus and necessary routes. Core credentials never enter frontend build variables. Deploy behind HTTPS, with the original Host preserved, and use a dedicated core identity/organization for the demo. This is an evaluation space shared by everyone with its password, not individual user accounts.

Railway deployment and `quivr.thevibecompany.co` are tracked separately in [THE-664](https://linear.app/thevibecompany/issue/THE-664).
