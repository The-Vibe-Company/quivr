# Mode: `resume` — Zero-Context Resumption & Portable Handoff

Captain never relies on chat history: live OpenBoard, live Armada, live GitHub, and `.captain/` (`config.json`, `rules.md`, `HANDOFF.md`) hold everything needed to resume in a fresh session or different agent harness.

---

## Resuming an existing fleet

1. **Preflight CLI auth first** (especially on a new machine/harness):
   - Verify `ob whoami`, `<armada-cli> --version` (`>= 14.x`) + `<armada-cli> info --setup --org <org>`, `template list --org <org> --json`, and `gh auth status`.
   - Before running `armada exec` on any recently launched sandbox (`status == "starting"`), wait with `bash "$SKILL_DIR/scripts/armada-modal.sh" wait-ready <sid> --org <org>` (otherwise `armada exec` returns `not_found`).
2. **Read local context:**
   - `.captain/config.json`, `.captain/rules.md` (check for `PAUSED: OFF_THE_RAILS` or active `STOP` rules), and `.captain/HANDOFF.md` if present.
3. **Reconcile against live reality:**
   - `ob tasks counts <channel> --json` and `ob tasks <channel> --state open --json`
   - `<armada-cli> list --org <org> --json` (distinguish `captain-*` tagged sandboxes from user-owned sandboxes; flag any orphan `captain-*` sandbox whose PR already merged)
   - `gh pr list --repo <repo> --state open --limit 50` and `gh run list --repo <repo> --branch <base> --limit 3`
   - Check `bash "$SKILL_DIR/scripts/watchdog.sh" status --state-dir .captain` before starting any watchdog (never run duplicate loops).
4. **Report a one-screen live picture** in the user's language:
   - OpenBoard counts (`ready` / `waiting` / `claimed` / `blocked` / `completed`)
   - Live Captain sandboxes & open PRs (with clickable links)
   - Base-branch CI health, active `STOP` rules, and paused state
   - Immediate next actions.
5. **Confirm authority when needed:**
   - If `PAUSED: OFF_THE_RAILS` is set in `.captain/rules.md`, always wait for explicit user approval before launching new sandboxes.
   - If `merge_mode` is `auto-merge` and this fresh session did not explicitly re-authorize auto-merging, ask a one-line confirmation before merging.
6. Re-arm the host wakeup adapter and continue [references/run.md](run.md).

---

## Generating a portable handoff (`.captain/HANDOFF.md`)

When context grows heavy or the user switches sessions/agents, write `.captain/HANDOFF.md` directly from live command outputs (zero secrets):

```markdown
# Captain Handoff

Generated: `<UTC timestamp>` — zero secrets.

## Resume Prompt
Use the captain skill in resume mode on `<owner/repo>` using `.captain/HANDOFF.md` and `.captain/config.json`.

## Live Snapshot
- **OpenBoard (`<channel>`):** ready=`<N>`, waiting=`<N>`, claimed=`<N>`, blocked=`<N>`, completed=`<N>`
- **Base branch (`<base>`):** `<green | RED>` · **Challenger checkpoint:** `<last-sha>` (`<N>` merged since last challenge)
- **Active Captain sandboxes:** table of `<Task # | Sandbox ID | Model | Tier | Branch | PR # | Status>`
- **Active STOP rules:** copy from `.captain/rules.md` (including `PAUSED` if set)
- **Coordinator Notes:** 3–5 concise bullets (recent decisions, pending human approvals, known flakes)
```

Verify zero secret-like values appear in `.captain/HANDOFF.md`, then share the file link and the one-line resume prompt.
