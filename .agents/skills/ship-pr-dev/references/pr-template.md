# PR Template

Use this structure when creating or updating the PR body. Keep it factual and easy for a human reviewer to scan.

## Title

Use commitzen style:

`feat(scope): concise outcome`

Examples:

- `feat(auth): add session refresh guard`
- `fix(exports): preserve tenant filter in CSV jobs`
- `chore(ship-pr-dev): add PR readiness workflow`

## Body

```markdown
## Summary
- <user-visible or maintainer-visible change>
- <supporting implementation detail>

## How to test
- <2-3 concrete steps a human can perform>

## Verification
- [x] `<command>` - <result>
- [x] `<command>` - <result>
- [ ] <skipped check> - skipped because <reason>

## CI
- Latest commit: `<sha>`
- Status: <all visible non-skipped checks green / blocked>
- Checks: <short summary>

## Review Gate
- `review-code-dev` v2 / Alibaba delegation: <passed / findings fixed / blocked>
- Required focus areas: <frontend/security/API/etc. / none>
- Original need and approved plan, if supplied: <criteria met / deviations and disposition>
- Coverage: <reviewed/excluded/skipped counts; scope matches latest source>
- Findings: <critical/high/medium/low counts and disposition>
- Artifacts: `<path or PR-safe summary>`

## Risk
- <deployment, migration, compatibility, UI, data, or security risk>
- <"None known" only when true after review>

## Screenshots (UI changes only)
- <shareable PR attachment for each changed screen/state, when captured; otherwise why unavailable>

## Human Review Notes
- <P3 follow-up, accepted risk, or reviewer focus area>
```

Do not paste local secrets, long logs, noisy generated output, or machine-local screenshot paths into the PR body. Summarize and point to committed artifacts only when they are intentionally part of the repo. Local `plans/` artifacts should usually be referenced in the chat handoff, not in a public PR body. If screenshots cannot be shared through an available authorized mechanism, state that limitation without inventing links.

## Final Chat Handoff

After PR creation or update, answer with:

```markdown
PR ready to merge by a human: <url>

Changed: <one sentence about delivered behavior>
To test: <2-3 concrete human steps>
Verified: <local checks, independent review and latest-SHA CI>
Screenshots / limits: <actual evidence or relevant limitation; omit if irrelevant>
Details: <local RUN_DIR with branch/base, SHA, coverage, CI and learning report>
```

If blocked:

```markdown
PR not ready yet.

Blocker: <specific issue>
What passed: <short evidence>
What remains: <next action>
Artifacts: <local RUN_DIR>
```
