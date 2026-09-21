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

## Verification
- [x] `<command>` - <result>
- [x] `<command>` - <result>
- [ ] <skipped check> - skipped because <reason>

## CI
- Latest commit: `<sha>`
- Status: <all visible non-skipped checks green / blocked>
- Checks: <short summary>

## Review Gate
- `review-code-dev`: <passed / findings fixed / blocked>
- Required lenses: <frontend/security/API/etc. / none>
- Artifacts: `<path or PR-safe summary>`

## Risk
- <deployment, migration, compatibility, UI, data, or security risk>
- <"None known" only when true after review>

## Human Review Notes
- <P3 follow-up, accepted risk, or reviewer focus area>
```

Do not paste local secrets, long logs, or noisy generated output into the PR body. Summarize and point to committed artifacts only when they are intentionally part of the repo. Local `plans/` artifacts should usually be referenced in the chat handoff, not in a public PR body.

## Final Chat Handoff

After PR creation or update, answer with:

```markdown
PR ready to merge by a human: <url>

Branch: <branch> -> <base>
Latest commit: <sha>
Verification: <short check summary>
CI: <all visible non-skipped checks green on latest commit>
Review gate: <review-code-dev summary and relevant lens coverage>
Learning loop: <completed / blocked>
Remaining human decisions: <none / list>
Artifacts: <local RUN_DIR>
```

If blocked:

```markdown
PR not ready yet.

Blocker: <specific issue>
What passed: <short evidence>
What remains: <next action>
Artifacts: <local RUN_DIR>
```
