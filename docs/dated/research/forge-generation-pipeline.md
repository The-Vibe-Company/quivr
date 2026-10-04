# Cloudflare Forge as the Quivr V2 generation pipeline

Date: 2026-09-29

Status: final research note.

_Research date: 28 September 2026. Forge was analysed at commit [`cfe397c`](https://github.com/cloudflare/forge/tree/cfe397c296a5e6d9fce01eb335ed805821e5547c), the only commit in the public repository, and exercised against the Quivr V2 client schema derived from `contracts/http/v0/openapi.yaml`. Statements marked **Observed** come from the source code, repository metadata or commands run for this note. Statements marked **Inference** are architectural judgements for Quivr, not facts about Forge._

## Decision-relevant summary

1. **Forge is Cloudflare's internal pipeline, published on the day of this research.** The repository was created on 21 September 2026 and contains one squashed commit, dated 28 September 2026, authored by a Cloudflare bot. It has no npm packages, no semver releases and no stability statement. Its only GitHub releases publish Cloudflare's own OpenAPI document. The Apache-2.0 licence is fine; maturity is the blocker.
2. **The SDKs come from Fern.** Every SDK path runs the [Fern CLI](https://github.com/fern-api/fern) (`fern-api` 5.112.0, Apache-2.0) and the official `fernapi/fern-*-sdk` Docker images, with Cloudflare patches applied on top. The announcement does not mention Fern. The "transformer" API (`init → transform → finalize`) is a small file-emitting hook, and no transformer implementation ships in the repository.
3. **Hands-on, the generated TS, Python and Go SDKs were good for our 38-operation contract. Most of that quality comes from Fern generators, not from Forge.** The TS SDK passed `tsc --strict` and worked against a mock server. Python round-tripped 18 of 18 response examples and 8 of 8 request examples from `examples.json`. The Go SDK built and passed `go vet`. All three are branded Cloudflare (`CloudflareApiClient`, package `cloudflare`, `CloudflareApiError`), and the TS runtime always installs a Cloudflare response-envelope unwrapper.
4. **There is no MCP generator in the repository.** The blog lists MCP as something Forge can be used for, not as a shipped output. The core API is thin enough that an intention-first MCP generator in Forge would be almost entirely our own code. A prototype showed that one spec can feed several distinct tool catalogues, but Forge's parameter model is shaped for a CLI and loses nested JSON Schema.
5. **The docs stack (`astro-fern`, `fern-forge`, `docs-site`) looks the most promising, but it is not ready for us to take on.** It generated an API reference, per-operation agent Markdown and `llms.txt` for our spec. However, the packages are private and unpublished, the site is server-rendered for Cloudflare Workers, every operation needs OpenAPI tags, the site is hard-wired to Cloudflare (base URL, `cf` CLI snippets, branding), and a hand-written Markdown guide returned 404 until the site's routing changes.
6. **Recommendation: do not adopt Forge now.** Keep the OpenAPI contract as the source of truth. Run a short separate spike on Fern OSS generators used directly, since that is where the SDK quality came from. Write the MCP servers by hand on the official MCP SDKs with a small intention catalogue. Look again at `astro-fern` once it is published. The conditions that would change this are listed at the end.

## What Forge is (observed)

### Repository facts

| Item | Observation |
| --- | --- |
| Repository | [`cloudflare/forge`](https://github.com/cloudflare/forge), created 2026-09-21, first public push 2026-09-28 |
| History | 1 commit, "Initial public release of Forge", author `workers-devprod@cloudflare.com` |
| Contributors | 1 (the bot account); 306 stars and 15 forks on the research date |
| Activity | 4 PRs on day one: a Dependabot bump (closed) and three open community PRs (docs, ASCII chart fix, Fern availability parsing) |
| Releases | 3 tags `openapi@<sha>`, each publishing only `openapi.forge.json` (Cloudflare's API). Code expects `openapi.vYYYYMMDD.N` tags ([`public-openapi.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/forge/public-openapi.ts)), which no release matches |
| npm | `@cloudflare/forge`, `@cloudflare/forge-transformer-sdk-ts`, `@cloudflare/forge-sdk-ts`, `astro-fern` and `fern-forge` all return 404, even though the README says to run `pnpm add @cloudflare/forge` |
| Versions | Every package is `0.1.0`; `astro-fern`, `fern-forge`, `docs-site` and the language wrappers are `"private": true` |
| Licence | Apache-2.0 ([`LICENSE`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/LICENSE)). Key dependencies: `fern-api` Apache-2.0, `astro` MIT, `openapi-format` MIT, `yaml` ISC, `tar` BlueOak-1.0.0. All permissive |
| Tests | 347 tests, all passing locally (`pnpm test`): forge 36, astro-fern 166, fern-forge 16, docs-site 110, sdk-ts 19. About 9k lines of tests for about 19k lines of non-generated TS |
| Stability promise | None in the repository. The blog calls Forge "early in its life" |

### Package layout

- [`packages/forge`](https://github.com/cloudflare/forge/tree/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/forge): OpenAPI resolver, overlay engine, `Forge` class, and a CLI-shaped `Schema` model (commands, groups, args, completion sources, confirmation prompts). Ships TypeScript source (`exports: ./index.ts`), not compiled JS.
- [`packages/cloudflare-forge-transformer-sdk-ts`](https://github.com/cloudflare/forge/tree/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/cloudflare-forge-transformer-sdk-ts): the `forge` CLI. It writes a Fern workspace, builds a patched `fernapi/fern-typescript-sdk:3.80.1` image, generates in three shards, merges them, installs a Cloudflare custom runtime and emits `sdk-map.json` ([`generate-from-openapi.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/cloudflare-forge-transformer-sdk-ts/scripts/generate-from-openapi.ts)). The organisation is hard-coded to `cloudflare` (line 117).
- [`packages/cloudflare-fern-config`](https://github.com/cloudflare/forge/tree/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/cloudflare-fern-config): a Fern [`generators.yml`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/cloudflare-fern-config/fern/generators.yml) that pins nine Fern generator images (TS 3.80.1, Python 5.18.1, Go 1.47.2, Java, PHP, C#, Ruby, Swift, Rust).
- `packages/cloudflare-forge-sdk-{python,go,…}`: each contains only a `package.json` that calls the Fern config script. In Forge's own CI, all eight non-TS languages have `"auto": false` ([`.github/sdk-languages.json`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/.github/sdk-languages.json)), so they are not generated on pull requests.
- [`packages/astro-fern`](https://github.com/cloudflare/forge/tree/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/astro-fern), [`fern-forge`](https://github.com/cloudflare/forge/tree/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/fern-forge) and [`docs-site`](https://github.com/cloudflare/forge/tree/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/docs-site): an Astro integration for OpenAPI reference docs, a Forge-metadata extension for it, and Cloudflare's Starlight site.

### The plugin/transformer API

Observed in [`forge.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/forge/forge.ts):

- `type TransformerFn = (forge: Forge) => Promise<void>` (line 12). A transformer calls `forge.emit(path, content)` (line 180).
- `forge.transform(fn)` runs one transformer and drains the emit buffer into `SourceFile[]` (line 192). `forge.finalize(dir, files, { clean })` writes them, with a path-traversal guard (line 204).
- `init(openapi, overlays)` applies overlays and builds `forge.commands` ([`init.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/forge/init.ts)).

"Plugin lifecycle" therefore means these three calls. There is no registry, config file, discovery, dependency ordering or transformer chaining beyond calling functions in sequence. No `TransformerFn` implementation exists anywhere in the repository (a grep for `.emit(`/`TransformerFn` outside `forge.ts` finds nothing). The resolver keeps its OpenAPI document and operation map in module-level variables ([`openapi-resolver.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/forge/openapi-resolver.ts) lines 8 and 382), so two specs cannot be resolved independently in one process.

Operations only become commands when they carry `x-fern-sdk-group-name` ([`init-from-openapi.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/forge/init-from-openapi.ts) line 148). Fern's extension vocabulary is Forge's data model.

### Overlays

Forge accepts documents shaped like [OpenAPI Overlay 1.0](https://spec.openapis.org/overlay/v1.0.0), but the implementation is narrower than the spec ([`overlay-source.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/forge/overlay-source.ts)):

- Only two targets validate: `$`, and `$.paths.*[?@.operationId=="<id>"]` (lines 746–800). Any other JSONPath is rejected as "unsupported overlay target".
- For operation targets, only `update.description` is read (lines 575–586). Other keys are **silently dropped**. In the hands-on run, `x-forge-require-confirmation` set that way never reached the overlaid spec.
- `remove` is unsupported (line 846).
- `$` updates are deep-merged into the document (line 814). This is the practical way to patch arbitrary operation fields, keyed by path and verb rather than by JSONPath. The hands-on run used it to add `tags` and `x-fern-pagination`.
- The overlay must declare an `x-forge-commands` catalogue: command → methods with `operationId`, `x-fern-sdk-method-name` and `x-fern-availability`. This catalogue becomes the SDK namespace tree.
- By default `resolveApiOverlays` writes artifacts into the package's own directory (`import.meta.dirname/overlays/_generated`, line 86) unless `writeArtifacts: false` is set.
- `openapi-format` is declared as a dependency but is not imported by any Forge source file.

## Hands-on evaluation

All work was done in `.scratch/` (gitignored). Environment: macOS arm64, Node 24.21, pnpm 10.27, Docker 29.3, Go 1.27.1, Python 3 venv.

### Setup

```bash
git clone https://github.com/cloudflare/forge .scratch/forge
cd .scratch/forge && pnpm install --frozen-lockfile   # 9 s, 510 packages, 866 MB node_modules
pnpm test                                             # 347/347 pass

python3 -m venv .scratch/forge-eval/venv
.scratch/forge-eval/venv/bin/pip install -r contracts/http/v0/checks/requirements.txt
.scratch/forge-eval/venv/bin/python contracts/http/v0/client_schema.py > .scratch/forge-eval/client-openapi.yaml
# converted to JSON: OpenAPI 3.1.0, 38 operations, no tags, bearer ApiKey scheme
```

### 1. Core engine and overlays

- `init(spec)` on the raw client schema: works on OpenAPI 3.1, reports 38 operation IDs and no missing descriptions, and builds **0 commands**, because there is no Fern group metadata.
- With an overlay catalogue mapping the 38 operations into 8 commands (`records`, `uploads`, `operations`, `corpora`, `changes`, `monitoring`, `connectors`, `search`), `init` builds the command tree, and `applyForgeOverlays` writes an overlaid spec with `x-fern-sdk-group-name`/`x-fern-sdk-method-name` on each operation.
- Running overlay application and transformers twice gave byte-identical output.

### 2. TypeScript SDK (`forge` CLI)

The package is not on npm, so it was built from source. Its build script unconditionally reads Cloudflare's spec from `packages/cloudflare-fern-config/fern/openapi.json` and `openapi.json` at the repository root. Both are absent from the repository, so the first `pnpm run build` failed with `ENOENT`. Copying our spec into both paths let the build finish.

```bash
node packages/cloudflare-forge-transformer-sdk-ts/dist/cli.js openapi.overlaid.json --out ts-sdk-overlaid
```

| Run | Result |
| --- | --- |
| Overlaid spec (groups present) | Success. 38 operations, 260 files, 1.2 MB. About 28 s cold, including the image patch, then about 10 s warm |
| Raw spec (no groups) | **Failed**: shard merge `ENOENT … sdk/api/resources/index.ts`. Group metadata is required in practice |
| Second run, same input | Byte-identical output tree |
| Overlaid spec + `x-fern-pagination` via a `$` overlay | `records.list()` becomes `core.Page<Record_, RecordPage>` (auto-pagination) |

Side effects observed: the CLI pulls about 1.35 GB (`fernapi/fern-typescript-sdk:3.80.1`), retags a patched image over the official name, and rewrites `node_modules/.pnpm/fern-api@5.112.0/.../cli.cjs` in place. It also searches the macOS keychain for certificates named "Cloudflare" to inject into the image ([`build-generator-image.sh`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/cloudflare-forge-sdk-ts/scripts/build-generator-image.sh)).

Code quality (observed):

- Types: discriminated unions are rendered correctly (`IngestCommandContent = Text | Blob | Manifest` keyed on `kind`), OpenAPI descriptions become TSDoc, and request/response types are named after our schemas.
- Namespaces: nested groups are flattened to `client.recordsVersions`, `client.monitoringSavedQueriesVersions` rather than `client.records.versions`.
- Auth: bearer token is supported (`new CloudflareApiClient({ environment, token })`), and `Authorization: Bearer` is sent.
- Errors: one `CloudflareApiError` with `statusCode` and an untyped `body`. This matches our spec, which declares a single `default` error response rather than per-status errors.
- Streaming: `changes.stream()` returns `core.Stream<string>` using `responseType: "sse"`. Events are untyped because the spec types the stream as `string`.
- Retries (2), timeouts (60 s) and abort signals are built in. Response validation is disabled (`skipResponseValidation: true`).
- Async/receipt patterns: none. `ingest` returns `Receipt`, and polling `getReceipt`/`getOperation` is left to the caller. OpenAPI cannot express this, so any generator would need hand-written helpers.
- Cloudflare-specific behaviour: `core/fetcher/unwrapCloudflareEnvelope.ts` is always installed. It replaces any JSON body that has both `success` and `result` keys with `result`, and throws when `success === false`. Quivr bodies do not use those keys today, but this is hidden semantics we would have to patch out.
- Verification: `tsc --strict` (TS 6.0.3) on the SDK plus a smoke test passed. Against a local mock server, `records.list`, `records.ingest` and an error path behaved correctly.

### 3. Python and Go SDKs (Fern config script)

```bash
cd packages/cloudflare-fern-config
FORGE_OPENAPI_SPEC=…/openapi.overlaid.json pnpm run generate python go   # ~26 s
```

- Output was written into `packages/cloudflare-forge-sdk-{python,go}`, and Fern **deleted those wrappers' `package.json` files**. The script also overwrites `fern/openapi.json` in place. Images: Python 1.14 GB, Go 888 MB.
- **Python** (167 files, sync `CloudflareApi` and `AsyncCloudflareApi`, pydantic v2, httpx): package and README are named `cloudflare` ("pip install cloudflare"). Using `httpx.MockTransport` and our [`examples.json`](../../../contracts/http/v0/examples.json):
  - 18 of 18 response examples round-trip exactly through the SDK models.
  - The 8 request examples (3 `IngestCommand`, `BatchRequest`, `SearchRequest`, `ConnectorCreate`, `SavedQueryCreate`, `SubscriptionCreate`) serialise byte-for-byte when passed as keyword arguments.
  - Request bodies are flattened into method kwargs, so there is no exported `IngestCommand` model.
- **Go** (130 files, `context.Context` first, functional options, `WithRawResponse` variants): the module path is `sdk`. `go build ./...` and `go vet ./...` pass. Generated wire tests expect a WireMock server on `:8080` and fail without it.
- Determinism: a second run was identical except `.fern/metadata.json`, which records the host repository's git commit and `originGitCommitIsDirty`. That file must be excluded before any `cmp`.

### 4. MCP (custom transformer prototype)

There is no MCP transformer to run. A 70-line `TransformerFn` was written to test whether Forge can host an intention-first generator (kept in the gitignored `.scratch/forge-eval/core/`, not committed). A hand-written intention catalogue maps tools to one or more operations: `search → searchRecords`, `get_evidence → getRecord + getVersion`, `ingest → ingestRecord + getReceipt`. The transformer emitted three distinct servers from one spec: `quivr-retrieval` (read-only), `quivr-ingest`, and `quivr-all-readonly`, which filters out mutating tools. A naive mirror produced 38 tools for comparison.

What Forge contributed: operation lookup, verb/path, descriptions, and flattened parameters from `resolveOperation`. What it did not contribute:

- Parameter names are CLI flags (`corpus-ids`, not `corpus_ids`), and types are reduced to `string | number | boolean | array` with no nested JSON Schema. An MCP `inputSchema` would have to be rebuilt from the raw OpenAPI.
- Nothing models multi-step tools, provenance shaping, output schemas, read-only annotations or server transport. That is all our code.

### 5. Docs (`docs-site` with `astro-fern`)

The site's source provider was switched from Cloudflare's GitHub spec to our overlaid spec, a Markdown guide was added under `src/content/docs/guides/`, and `astro build` was run.

- The first build **failed**: `astro-fern: operation "listRecords" has Fern SDK metadata but no OpenAPI tag` ([`content/build.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/astro-fern/content/build.ts) line 336). Our contract has no tags. Forge's operation-level overlays cannot add them, so tags were added by a pre-processing script (a `$` deep-merge overlay also works).
- With tags, the build took about 3 s. `astro dev` served:
  - a product index with 8 products;
  - per-product pages;
  - `/api/llms.txt` and per-product `llms.txt`;
  - agent-oriented Markdown per operation (`/api/search/methods/query.md`), with request fields, enums, defaults and a curl snippet.
- Cloudflare coupling in the app: curl snippets target `https://api.cloudflare.com/client/v4` with `$CLOUDFLARE_API_TOKEN`, a `cf CLI` execution target and a "Developer Tooling" section of `cf` commands are listed, and the pages carry Cloudflare branding.
- The hand-written guide at `/api/guides/ingest-and-search/` returned **404**. Starlight's `docsLoader` collection is registered ([`content.config.ts`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/docs-site/src/content.config.ts) line 33), but the site routes pages through its own `[product]/[...slug].astro`. Guides therefore need routing work in the app.
- The site is `output: 'server'` on the Cloudflare adapter ([`astro.config.mjs`](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/docs-site/astro.config.mjs) line 25). `astro-fern` offers `getFernStaticPaths()` but notes that prerendering still needs an artifact transport at build time ([README](https://github.com/cloudflare/forge/blob/cfe397c296a5e6d9fce01eb335ed805821e5547c/packages/astro-fern/README.md), around line 514).

## Answers to the specific questions

### Can Forge host a custom intention-first MCP generator? Multiple servers from one spec?

**Observed:** yes, in the trivial sense. A `TransformerFn` can emit any files, and `forge.transform()` can run once per server from one `Forge` instance. The prototype produced three distinct catalogues from one spec.

**Inference:** it should not. Forge supplies an operation index and a CLI-shaped parameter model. An intention-first MCP server needs:

- composite tools across several operations;
- faithful JSON Schema for inputs and outputs;
- provenance-preserving result shaping;
- `readOnlyHint`/`destructiveHint` annotations and a read-only mode;
- auth pass-through and transport.

None of those come from Forge. Generating those servers would also mean regenerating code whenever the intentions change, while the intentions are a product decision that changes rarely and deliberately.

The better shape is a hand-written server per audience on the official MCP SDK, calling a generated SDK, with a small declarative catalogue per server. The candidates are the [TypeScript SDK](https://www.npmjs.com/package/@modelcontextprotocol/sdk) (1.30.1, MIT) and the [Go SDK](https://github.com/modelcontextprotocol/go-sdk) (v1.8.0, 2026-09-14, licence transitioning from MIT to Apache-2.0, both permissive). Catalogue files would list tools, the operations each composes, and whether each is read-only. That gives several distinct servers without a generator. A contract test can check that every operation a catalogue references still exists in `openapi.yaml`.

### Overlays, hand-written content, functional guides and plugin-author docs

**Observed:**

- Overlays feed the docs site through the overlaid OpenAPI document. Descriptions and `x-forge-commands` group descriptions become page text.
- `astro-fern` extensions can add per-operation data (the README's "Add an extension" section, around line 424).
- Operation-level overlays only accept `description`. Other enrichment needs `$` deep merges or a pre-processing step.
- Hand-written Markdown can live in the same Astro/Starlight project, since the `docs` collection is registered, but Cloudflare's `docs-site` routes do not serve it without changes.

**Inference:** functional guides and plugin-author docs could share one Astro site with an `astro-fern` API reference. That requires owning an Astro application (routing, theme, snippets, base URL, static or server output) built on unpublished packages that Cloudflare shapes for its own site. Plugin-author docs describe the plugin SDK and the `quivr-plugin.yaml` manifest ([architecture overview](../design/quivr-v2-architecture-overview.md), plugin system section). Forge only helps there if the plugin API is itself an OpenAPI document; otherwise those docs are ordinary Markdown.

### Toolchain weight, CI, determinism

**Observed:**

- Quivr already needs Node 22 and Docker for `make contracts` (`scripts/contracts.sh` runs openapi-generator in Docker with `--network none`, plus `npm` for the TS round trip).
- Adopting Forge adds a pnpm workspace (866 MB of `node_modules` for the monorepo). It also adds one Fern generator image per language (0.9 to 1.35 GB each), which is pulled from Docker Hub and, for TS, rebuilt locally.
- Forge's CA-injection comment says the host CA is added "so go mod / pip / cargo can verify TLS" inside the generator containers, which implies some generators fetch packages during generation and may not work under `--network none`.
- The Fern CLI has telemetry, which `FERN_DISABLE_TELEMETRY` switches off.
- Determinism: TS output was byte-identical across two runs. Python and Go were identical apart from `.fern/metadata.json`.

**Inference:**

- A `cmp`-style check like `transport.gen.go` is feasible if images are pinned by digest (Forge pins by tag only) and `.fern/` is excluded.
- A check that pulls several gigabytes does not belong in the default `make verify`. It would sit better in a separate CI job, the way Forge itself disables non-TS languages by default.

### Alternatives

| Option | Licence | Fit for Quivr (inference) |
| --- | --- | --- |
| **openapi-generator** (current, [OpenAPITools](https://github.com/OpenAPITools/openapi-generator)) | Apache-2.0 | Already wired, network-isolated and round-trip tested; generic output, weaker ergonomics; fine for throwaway checks, weaker as a published SDK |
| **oapi-codegen client mode** | Apache-2.0 | Same tool as our Go server; a Go client comes nearly free and is deterministic in `make verify`; Go only |
| **Fern OSS generators used directly** ([fern-api/fern](https://github.com/fern-api/fern)) | Apache-2.0 | What produced the good SDKs above, without Forge's Cloudflare wrapper, sharding or envelope runtime; needs `x-fern-*` extensions, Docker images and telemetry opt-out. Fern's hosted docs product is not needed for this (inference: it is a separate commercial offering) |
| **Kiota** ([microsoft/kiota](https://github.com/microsoft/kiota)) | MIT | Single binary, many languages, request-builder style (`client.v0.records.get()`) that is less idiomatic for RAG users; no docs or MCP |
| **Forge** | Apache-2.0 | Fern plus Cloudflare patches plus an unpublished docs engine; valuable at Cloudflare's scale (3,500+ operations, 18k-file TS SDK), little marginal value at 38 operations |
| **Official MCP SDKs** (TS, Go) | MIT, moving to Apache-2.0 | The right base for intention-first MCP servers written by hand |
| Stainless, Speakeasy | Commercial | Context only: hosted SDK and MCP generation; excluded by the permissive-OSS and self-hosted preference; not evaluated |

Not evaluated here: Starlight-based OpenAPI plugins or other static reference generators as a lighter alternative to `astro-fern`.

## Recommendation

**Do not adopt Forge as Quivr V2's generation pipeline at this commit.**

Options, in order of preference:

1. **Recommended: contract-first, with separate tools per surface.**
   - Keep `openapi.yaml` authoritative and keep the current contract checks.
   - Run a time-boxed spike on Fern OSS generators used directly for the published TS and Python SDKs. Add `x-fern-sdk-group-name`, `x-fern-sdk-method-name` and `x-fern-pagination` in `client_schema.py`, not in the authoritative contract. Pin images by digest and add hand-written receipt/operation polling helpers.
   - Write the MCP servers by hand on the official MCP SDK with one intention catalogue per server.
   - Choose the docs engine later, once guides and plugin-author docs exist to test it against.
2. **Adopt only `astro-fern` for docs**, vendored or pinned to a commit, with our own Astro app. Consider this only if it gets published and its static-output story works in practice.
3. **Adopt Forge fully** (overlay catalogue, `forge` CLI, `fern-config`, `docs-site`). Today this means forking and de-branding four Cloudflare-specific packages and tracking an upstream with no releases.

Conditions that would flip the recommendation towards Forge:

- Packages are published to npm with semver, a changelog, and several months of releases with non-Cloudflare contributors.
- The SDK transformer becomes generic: client and error names, organisation and package names are configurable, and the envelope unwrap is opt-in.
- Operation-level overlays honour arbitrary fields, or full JSONPath and `remove` are supported.
- An MCP transformer ships that supports curated, multi-operation tool catalogues with JSON Schema inputs. That would make the intention-first servers mostly configuration.
- `astro-fern` is published with a supported static build, and the reference site serves Markdown guides alongside it.
- Quivr's surface grows to hundreds of operations or several products, where overlay catalogues, sharding and `sdk-map.json` start to pay off.

## Open risks and uncertainties

- **Upstream churn.** The single-commit history hides how often interfaces change. The mismatch between release tags and the `openapi.vYYYYMMDD.N` pattern suggests that the published state was still moving on day one.
- **Cloudflare coupling in generated runtime code.** The envelope unwrap, `CloudflareApiError` and package names would ship in our SDKs unless we fork.
- **Supply chain and reproducibility.** Multi-gigabyte generator images are pinned by tag only. The TS path patches `node_modules` and retags official images locally. Some generators may need network access during generation.
- **Fern generator quality holds for this contract only.** We did not test multipart uploads, binary blob download, SSE event typing, or behaviour when the contract changes (diff noise between versions).
- **MCP Go SDK licence.** The licence is mid-transition. Both licences are permissive, but it should be re-checked when a dependency is added.
- **Evidence scope.** The hands-on results come from one machine and one day, with 38 operations. Timings are indicative only.
