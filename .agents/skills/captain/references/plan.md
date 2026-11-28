# Mode: `plan` — Grill, Investigate, and Ticketize into OpenBoard

Keep `plan` in one clean context window until the OpenBoard tasks are created so every task carries the exact settled seams and decisions.

---

## Step 1: Delegate codebase discovery first

- Read only high-level orientation files (`README.md`, `AGENTS.md`, `.captain/config.json`, `ob tasks counts <channel> --json`).
- Verify write permissions early: `ob grants explain <channel>`.
- Delegate deep codebase reading (seams, contracts, DB migrations, tests, CI) to a local read-only subagent by default (or an Armada Scout sandbox only if Armada is already configured).

---

## Step 2: Grill consequential decisions

Invoke **`grilling`** (or run frontier-round questions directly if `grilling` is not installed):
1. State the decision branch and completion condition.
2. Ask the unsettled frontier in numbered rounds with a recommended answer on every question (`❓ **Q1** ...` / `➡️ ...`).
3. Stop once consequential choices are settled; record non-blocking assumptions explicitly.

*(If the problem itself is still foggy or needs multi-angle SOTA research before grilling, invoke **`deep-dive`** first.)*

---

## Step 3: Slice into disjoint, verifiable OpenBoard tasks

- **One producer owns every shared contract.** Put shared API types, schemas, or DB migrations in a small prerequisite task (`key: "contract-..."`) and wire consumers via `needs: [{"key": "contract-..."}]`.
- **Serialize DB migrations strictly.** Chain any tasks touching `migrations/**`, `prisma/migrations/**`, `drizzle/**`, `alembic/**`, or `db/migrate/**` sequentially via `needs` (max 1 in flight at a time — disjoint migration filenames still corrupt migration ordering with zero git conflict).
- **Keep write scopes disjoint.** List exact `Owned globs` and explicit `Out of scope` exclusions in every task. Shared append points (manifests, lockfiles, barrel exports) do not block launches.
- **Classify risk & verification cleanly:**
  - `"tier": "core"` — gating prerequisites, shared contracts, migrations, auth, tenancy, billing, CI, security, concurrency, or complex logic → **Builder**.
  - `"tier": "leaf"` — isolated leaf task with zero dependents, small blast radius, no sensitive areas → eligible for **Scout / Light** when enabled.
  - `"verify": "unit"` (tests at public seams + real binary/endpoint run), `"verify": "ui"` (real browser check + final-SHA screenshots), or `"verify": "live"` (real-provider evidence or post-deploy check).
- **Root-cause & anti-slop rules on every task:**
  - Bug tasks require naming the root cause with evidence, auditing sibling call sites, and adding a regression test (never "smallest spot fix").
  - Every task names concrete public seams and an observable "verify by using it" check.

### Task body template

```markdown
## Goal
<One concrete, observable outcome>

## Context & Decisions
- <Settled decision from grilling>

## Owned Scope
- Owned globs: `<glob1>`, `<glob2>`
- Exclusive lane: `<none | migrations>`
- Out of scope: `<explicit exclusions>`

## Seams & Verification Plan
- Public seam: `<CLI / HTTP route / UI route / API>`
- Observable check (verify by using it): `<exact command or user flow + empty/error case>`
- [ ] <Acceptance criterion 1>
- [ ] <Acceptance criterion 2>
- [ ] Tests and repo CI checks pass locally

## Worker Rules
- Follow `## OpenBoard` (`ob whoami`, `ob tasks claim <id>`, `ob read <id>`, `ob tasks renew <id>`, `ob reply <id>`, `ob tasks complete <id>`).
- Reference `OpenBoard #<id>` in the PR body (never `Closes #<id>`).
- If blocked on a human decision or missing prerequisite, place the hold yourself (`ob tasks block <id> "<reason>" --kind awaiting_approval`) before ending with `BLOCKED:`.
- Hard refusals: never skip/disable tests, add `@ts-ignore`/`eslint-disable`/`# type: ignore`, loosen lint/CI rules, use `--no-verify`, or fake auth/permissions/billing/PII.
```

---

## Step 4: Publish atomically to OpenBoard

Write the ordered batch to `.captain/plans/<YYYYMMDD-HHMMSS>-<slug>.json`.
**OpenBoard contract:** `labels` MUST be a JSON **object** (`{"tier": "core", "area": "api"}`), **never** a string array!

```json
[
  {
    "key": "schema-contract",
    "text": "<task markdown>",
    "labels": { "tier": "core", "area": "api", "verify": "unit" }
  },
  {
    "key": "ui-search-bar",
    "text": "<task markdown>",
    "labels": { "tier": "leaf", "area": "web", "verify": "ui" },
    "needs": [{ "key": "schema-contract" }]
  }
]
```

Show a concise summary table (`key`, title, `needs`, `tier`, `area`, `verify`, owned globs), confirm once if not already authorized, and publish with a unique timestamped idempotency key:

```bash
ob tasks create <channel> \
  --file .captain/plans/<YYYYMMDD-HHMMSS>-<slug>.json \
  --idempotency-key "captain-<slug>-<YYYYMMDD-HHMM>"
```

Verify with `ob tasks counts <channel> --json`, then move into [references/run.md](run.md).
