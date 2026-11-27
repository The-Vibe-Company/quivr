# Mode: `challenge` — Objective Independent Challenger (Every 10 / 20 PRs)

Long autonomous runs drift quietly: micro-PRs pile up without converging, mock-heavy tests pass CI while hiding bugs, or the same files thrash across PRs. Every `10` or `20` newly merged PRs (or on demand), run an **independent read-only Challenger** grounded in objective metrics — never vibes or coordinator narration.

---

## Step 1: Collect objective batch metrics (3 fast read-only commands)

Before spawning the Challenger, gather hard numbers over the merged batch (since the last Challenger checkpoint commit/timestamp) and save them to `.captain/challenges/metrics-<YYYYMMDD-HHMMSS>.md`:

1. **Convergence, reverts, hot-churn & LOC:**
   ```bash
   git log <last-sha>..HEAD --pretty=format:"%h %s"
   git log <last-sha>..HEAD --numstat --pretty=format:"" | awk 'NF==3 {files[$3]++; add+=$1; del+=$2} END {printf "LOC: +%d -%d (net %+d)\nHot files (>=3 touches):\n", add, del, add-del; for (f in files) if (files[f]>=3) print " ", files[f], f}'
   ```
2. **Slop & mock scan on added (`+`) lines:**
   ```bash
   git diff <last-sha>..HEAD --unified=0 | grep -E '^\+[^+]' | grep -cE '\.(skip|only)\b|@ts-ignore|eslint-disable|#\s*type:\s*ignore|--no-verify' || true
   ```
3. **OpenBoard progress & recent merged PRs:**
   ```bash
   ob tasks counts <channel> --json
   gh pr list --repo <repo> --state merged --limit 30 --json number,title,additions,deletions,changedFiles,mergedAt
   ```

### Mechanical verdict floor (anti-sycophancy)

Compute the **`minimum_verdict` floor** before invoking the Challenger (the Challenger may escalate *upward*, never downgrade below this floor):
- **`OFF_THE_RAILS` floor:** base branch CI is red, revert rate `>= 25%` (`>= 2` reverts), OR **2 consecutive previous Challengers returned `DRIFTING`** without resolving the same hot-churn/rule.
- **`DRIFTING` floor:** any added `.skip`/`.only`/`--no-verify`, revert rate `>= 10%`, any hot file touched `>= 3` times in one batch, `> 300` prod LOC with `0` test LOC, or Light→Builder escalation rate `> 25%`.
- Otherwise **`ON_TRACK` floor**.

---

## Step 2: Launch an isolated, anti-contaminated Challenger

1. Always use an **independent read-only session** on the strongest reasoning model (`config.routing.challenger_model`).
2. **Anti-contamination:** Give the Challenger ONLY:
   - The original milestone/program objective.
   - The objective metrics snapshot collected in Step 1 + `minimum_verdict=<FLOOR>`.
   - Pointers to inspect the **actual `git diff`** of the top 3–5 highest-risk/largest/flagged PRs and current OpenBoard backlog.
   - Current `.captain/rules.md`.
   - **Never** pass coordinator status narration or author PR marketing summaries as truth.
3. For large batches (`>= 20` PRs or monorepos), fan out 2–4 parallel read-only sub-audits (core bugs, architecture/dead code, test honesty, UX/API consistency) and synthesize one verdict.

### Challenger brief template

```text
You are an independent read-only Challenger auditing the last <N> merged PRs on <repo>.
Ground every finding in the objective metrics and raw git diffs — never rubber-stamp progress.
Respect minimum_verdict=<FLOOR> (you may escalate upward, never downgrade).

Inspect:
1. Goal convergence: are we converging on "<objective>" or churning in micro-PRs?
2. Real P0/P1/P2 bugs, regressions, security/tenancy/migration hazards in the flagged diffs.
3. Anti-slop & test honesty: mocked-out tests, dead code, copied workarounds, unverified seams.
4. Routing & process health: Light escalation rate, hot-churn files, repeat failures.

Return EXACTLY:
- VERDICT: ON_TRACK | DRIFTING | OFF_THE_RAILS
- WHY: 3-5 evidence-backed bullets citing exact PR numbers, metrics, and file:line paths
- STOP_RULES: concrete enforceable rules with expiry (`until: <task_id | next_challenge>`), e.g. freeze a path, cap PR size, disable Light model on an area, or ban a pattern (or "none")
- NEW_TASKS: concrete OpenBoard bug/hygiene/harness-hardening tasks (title, priority, root cause, owned globs, verify plan)
- NEXT_DIRECTION: what to prioritize, freeze, or codify into permanent repo lints/CI
```

---

## Step 3: Apply the verdict progressively

Save the report to `.captain/challenges/challenge-<YYYYMMDD-HHMMSS>.md`, record the new checkpoint SHA/PR count, and act:

| Verdict | Coordinator action |
| --- | --- |
| **`ON_TRACK`** | Add any small P1/P2 hygiene tasks to OpenBoard, update `.captain/rules.md` if useful, and **continue autonomously**. |
| **`DRIFTING`** | Write all `STOP_RULES` to `.captain/rules.md` (and inject them into every subsequent worker brief + pre-launch check), create priority corrective/lint-hardening OpenBoard tasks ahead of features, escalate problem areas off Light models, and **continue autonomously** with a concise note to the user. |
| **`OFF_THE_RAILS`** | **Pause all new sandbox launches immediately** (record `PAUSED: OFF_THE_RAILS` at the top of `.captain/rules.md`), stop broken loops, draft the corrective OpenBoard plan, and **alert the user** with the evidence-backed diagnosis. Do not resume new launches until the user gives the go. |
