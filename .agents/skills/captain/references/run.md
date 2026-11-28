# Mode: `run` — Run Contract, Launch & Autonomous Supervision

Use this guide when OpenBoard has ready tasks and the user wants Captain to launch and supervise the Armada fleet. Rely directly on live OpenBoard (`ob`), Armada (`>= 14.x`), and GitHub (`gh`) — no shadow local database.

---

## Step 1: Inspect live capabilities & queue

- Armada `>= 14.x`: `<armada-cli> info --setup --org <org>`, `template list --org <org> --json`, `snapshot ls --provider <provider> --org <org> --json`, `account ls --org <org>`, `host ls --org <org>` (always pass `--org <org>` when targeting an org; **exception**: `snapshot show --build <jobId>` forbids `--org`).
- MCP preflight: `bash "$SKILL_DIR/scripts/armada-modal.sh" preflight-mcp --org <org>` — ensure disconnected `defaultForLaunch` MCPs never block launches (Launch Templates pin `connections: []`; raw `armada new` passes `--no-connections`).
- OpenBoard: `ob tasks counts <channel> --json` and `ob tasks <channel> --state ready --json`.
- GitHub: `gh pr list --repo <repo> --state open --limit 50` and `gh run list --repo <repo> --branch <base> --limit 3` (record the current merged PR count / timestamp as the Challenger baseline so historical merged PRs never false-trigger on tick #1).

---

## Step 2: Negotiate the Run Contract (one confirmation before autonomy)

Propose smart defaults from `.captain/config.json` and confirm **once**:
1. **Model routing** (from live Armada accounts/models):
   - **Builder** (core, gating, complex tasks): **one model OR a round-robin list** rotated across Builder slots to balance quotas.
   - **Scout / Light** *(optional)*: cheaper/high-throughput model for read-only discovery and `tier=leaf` tasks. **Auto-escalation:** if a Light worker stalls, fails once, or touches sensitive paths (`auth`, `migrations`, `postgres`/`database`, `tenant`, `security`, `secret`, `billing`, `.github/workflows`), stop it and relaunch a fresh Builder sandbox.
   - **Challenger** (periodic audits): strongest reasoning model available.
2. **Provider, concurrency & circuit breakers**:
   - Provider + snapshot, `max_inflight` (default `8`), `max_open_prs` ceiling (default `5`).
   - Hard caps: `max_ttl_extensions` (`3`), `max_ci_fail_rounds` (`3`), `max_rebase_rounds` (`3`), `max_nudges` (`2`).
3. **Merge policy**:
   - `auto-merge` *(requires CI checks present; release PRs NEVER auto-merge; `tier=core` requires `ship-pr-dev`, a CI review bot, or an isolated read-only reviewer sandbox — never author self-review alone)*.
   - `review-gate` *(workers open verified PRs; human merges)*.
   - `draft-only` *(draft PRs only)*.
4. **Wake-up cadence**: `2m` (`120s`) active sprints or `5m` (`300s`) calmer runs.
5. **Periodic Challenger**: every `10` or `20` newly merged PRs, or `off` (progressive: `ON_TRACK`/`DRIFTING` acts & continues; `OFF_THE_RAILS` pauses new launches & alerts).
6. **Optional post-merge hook**: off by default; if the user adds one later, always delegate it to a worker/subagent.

Update `.captain/config.json` with the confirmed settings. After this single confirmation, operate autonomously and report meaningful outcomes, milestones, or genuine blockers.

---

## Step 3: Launch workers cleanly (1 sandbox = 1 ticket = 1 PR)

Before launching a ready OpenBoard task:
- Check `.captain/rules.md` for active Challenger `STOP` rules or paused state.
- Check live `gh run list --branch <base>` — if base CI is RED, freeze all feature launches and merges until fixed.
- Check open PR count (`< max_open_prs`) and active sandboxes (`< max_inflight`).
- Verify no active sandbox touches overlapping non-shareable globs — and **never launch two concurrent tasks touching DB migrations (`migrations/**`, `prisma/migrations/**`, `drizzle/**`, `alembic/**`)**.
- Name/tag every sandbox deterministically (`captain-<channel>-t<id>-a<attempt>`) so orphan reconciliation distinguishes Captain sandboxes from user sandboxes.

Launch commands (Armada `>= 14.x`):
- **Modal with OpenBoard auth (recommended — uses Vault `BOARD_URL` + `OPENBOARD_SECRET` and pinned `connections: []`):**
  ```bash
  bash "$SKILL_DIR/scripts/armada-modal.sh" launch-template "<template-id-or-name>" \
    --org "<org>" --request-key "captain-<channel>-t<id>-a<attempt>-$(date +%s)" \
    --ttl 4h --prompt-file "<prompt-file>"
  ```
  *(Under the hood: `<armada-cli> template launch <template> --request-key <key> --org <org> --ttl 4h --prompt "<prompt>" --json`. Keep the same `--request-key` if replaying an uncertain launch.)*
- **Modal bare snapshot (only when no Vault secrets are needed — ALWAYS pass `--no-connections` or explicit connected `--connections` to avoid 409 on `needs_reconnect` default MCPs):**
  ```bash
  bash "$SKILL_DIR/scripts/armada-modal.sh" launch-raw "<snapshot>" \
    --org "<org>" --agent "<agent>" --model "<model>" --ttl 4h --prompt-file "<prompt-file>"
  ```
  *(Never pass `--secret` or `--variable` to `armada new --provider modal` — those flags are Docker-only.)*
- **Docker:** `<armada-cli> new --provider docker --host <host> --repo <repo> --agent <agent> --model <model> --variable BOARD_URL:<uuid>:<v> --secret OPENBOARD_SECRET:<uuid>:<v> --no-connections --org <org> --ttl 4h --idempotency-key "<key>" --json "<prompt>"` (or `template launch`).
- **Wait before `armada exec`:** a freshly launched Modal sandbox starts in `status: "starting"`, during which `armada exec` returns `not_found`. Before running any `armada exec`, always wait via `bash "$SKILL_DIR/scripts/armada-modal.sh" wait-ready <sid> --org <org>` (or `<armada-cli> info <sid> --wait --timeout 60 --org <org> --json`).

### Canonical Worker Brief (anti-slop + foreground discipline)

Include in every worker prompt:
- **OpenBoard auth & lifecycle:** connect first if not yet authenticated using the Vault-injected env vars (`ob whoami >/dev/null 2>&1 || ob connect "$BOARD_URL" --as "<worker-profile>"` — `ob connect` natively reads `OPENBOARD_SECRET` from env; never echo or print secrets), then `ob tasks claim <id>`, `ob read <id>`, `ob tasks renew <id>`, `ob reply <id> "<update + PR URL>"`, and `ob tasks complete <id> "<result>"` only after verification passes.
- **Branch & PR hygiene:** pull freshly from `<base>`, branch `captain/<id>-<slug>`, push a draft branch early on long tasks, and put `OpenBoard #<id>` in the PR body (never `Closes #<id>`).
- **Verify by using it & hard refusals:** write tests at public seams (never mock the module under test), run the real artifact/CLI/server/browser against every acceptance criterion (including empty/error states, plus final-SHA screenshots for `verify=ui`), and include `## Verification` + `## Seams Tested` in the PR body. Never add `.skip`/`.only`/`@ts-ignore`/`eslint-disable`/`# type: ignore`, loosen lint/CI rules, use `--no-verify`, or fake auth/permissions/billing/PII.
- **Dynamic split / block protocol:** if oversized, missing a prerequisite, or blocked on a human decision, place the hold yourself (`ob tasks block <id> "<reason>" --kind awaiting_approval`) and end with `BLOCKED: SPLIT_PROPOSED ...`, `BLOCKED: MISSING_PREREQ ...`, or `BLOCKED: <reason>`.
- **Foreground review rule:** run `<review_tool>` **in the foreground inside this sandbox**. Never leave background reviewers/subagents running when your turn ends (no callback wakes a stopped sandbox).
- **Active rules:** paste any active `STOP` rules from `.captain/rules.md`.
- **Sentinel:** end with `READY TO MERGE: branch <branch> head <full SHA>` or `BLOCKED: <reason>`.

---

## Step 4: Arm the wakeup loop (host-adaptive)

- **Host with native scheduler (`schedule` / `CronCreate` + `watch_pull_request`):** schedule a recurring 2m/5m wakeup that checks live state (`ob tasks counts`, `gh pr list`, `gh run list --branch <base>`, active `armada info/events`). Do **not** also run `watchdog.sh`.
- **Claude Code (`Monitor` + background shell):**
  ```bash
  bash "$SKILL_DIR/scripts/watchdog.sh" start \
    --config .captain/config.json --state-dir .captain --interval 120
  ```
  Always check `watchdog.sh status --state-dir .captain` first. Attach `Monitor` to `.captain/orch.log` (all timestamps UTC).

---

## Step 5: Act on every wakeup

On each tick, check live state and act cleanly:

- **Base branch RED (`gh run list --branch <base>` failed):** Freeze all new launches, rebases, and merges immediately. Launch one P0 Builder task (`fix: restore red <base>` or revert the offending PR).
- **Active sandboxes (`<armada-cli> info <sid> --org <org> --json` & `events <sid> --tail 200 --org <org> --json`):**
  - If remaining TTL `< 15 min` (and PR not already merged, and `< max_ttl_extensions`): extend via `<armada-cli> api sandbox/extend '{"id":"<sid>","ttlSeconds":3600,"org":"<org>"}'`.
  - If sandbox output shows expired credentials (`401` / `Bad credentials`): **never stop it first** — rescue commits via `<armada-cli> exec <sid> --org <org> -- "git format-patch origin/<base>..HEAD --stdout" > .captain/rescues/task-<id>.patch`.
  - If an active worker's OpenBoard task fell back to `ready` (lease expired): do **not** spawn a duplicate sandbox; prompt the worker (`<armada-cli> prompt --org <org> --json <sid> -- "..."`) to renew its lease.
  - If a **Light** worker fails or stalls once: stop it, reopen/release the OpenBoard task if needed, and launch a fresh **Builder** sandbox.
  - If a **Builder** worker stalls waiting on a background subagent: nudge it once (up to `max_nudges`) to run checks in the foreground.
  - Check `<armada-cli> list --org <org> --json` periodically for orphaned `captain-*` sandboxes whose PRs already merged and stop them cleanly.
- **Open PRs (`gh pr list --repo <repo> --state open --limit 100 --json number,title,headRefName,headRefOid,mergeable,mergeStateStatus,reviewDecision,isDraft,body,labels,statusCheckRollup`):**
  - **Release PR** (`chore(release)`, `release-please`, `Version Packages`, `release` label): **ALWAYS HOLD** for explicit human approval.
  - **Non-Captain PR** (teammates / Dependabot): ignore completely.
  - **Conflicting PR** (and base is green, `< max_rebase_rounds`): prompt its *existing* sandbox (`prompt --org <org> --json <sid> -- "..."`) to rebase onto `origin/<base>`, re-verify, and push. If its sandbox is already dead/terminal, launch a fresh Builder sandbox on that branch.
  - **CI failed** (`< max_ci_fail_rounds`): rerun infra flakes (`gh run rerun <id> --failed`), or relay exact failing lines back to the PR's existing sandbox.
  - **Behind base (`mergeStateStatus == BEHIND`):** run `gh pr update-branch` (or let Merge Queue handle it) and wait for green checks on the new head.
  - **Green & clean (`mergeStateStatus == CLEAN`, latest deduplicated checks all `SUCCESS`, 0 unresolved review threads, `OpenBoard #<id>` present and no `Closes #<id>`, no `.captain/rules.md` violation):**
    - Under `auto-merge`: verify `tier=core` PRs passed `ship-pr-dev`, a review bot, or an isolated read-only reviewer sandbox; squash-merge or enqueue in GitHub Merge Queue (one direct merge at a time).
    - After confirmed merge: confirm OpenBoard task `completed`, **stop the worker's sandbox**, increment your newly-merged PR count, and launch newly unblocked OpenBoard tasks.
- **Challenger threshold hit (10 or 20 newly merged PRs since baseline/last challenge):** run [references/challenger.md](challenger.md).
- **Wave complete:** stop `watchdog.sh` (or cancel native timer), stop finished sandboxes, fold repeated `STOP` rules into repo lints/`AGENTS.md`, and send a concise summary with clickable PR links.
