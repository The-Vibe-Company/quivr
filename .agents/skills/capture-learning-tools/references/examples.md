# Examples

Use these examples as patterns. Do not copy project-specific details blindly.

## Missing Frontmatter Field

Conversation signal: A content file failed after a schema change, and the fix was added to one agent's personal workflow.

Recommendation:

- Add a content schema validation script.
- Run it locally before finishing content changes.
- Wire it into PR CI only for content/schema changes.
- Add a short project instruction telling agents to use the validator.

Rule:

```markdown
- When editing content frontmatter, run the content validator before finishing. Do not rely on agent-local defaults unless the schema lives in this repository.
```

## Claude File Exists But No AGENTS File

Conversation signal: The repo has `CLAUDE.md`, but Codex or another agent misses the rules.

Recommendation:

- Create `AGENTS.md` as the shared source of truth.
- Convert `CLAUDE.md` into an adapter that imports `AGENTS.md` plus Claude-only notes.
- Do not duplicate the same rules in both files.

## Private Repo With Slow Tests

Conversation signal: A CI failure was missed locally, but the full test suite is slow and private CI is costly.

Recommendation:

- Add a local preflight command for the affected area.
- Add a fast PR check for the high-signal subset.
- Keep full e2e as manual, nightly, or release-gated.
- Explain the cost tradeoff to the user before adding paid CI.

## Review Comment Repeats Twice

Conversation signal: Reviewers repeatedly ask for the same style or architecture change.

Recommendation:

- If mechanical, add lint/formatter/check.
- If architectural, add a short `AGENTS.md` rule and maybe an ADR.
- If subjective, add a PR template checklist or design note instead of a hard rule.

## Not Worth Automating

Conversation signal: The user made a one-time product taste decision.

Recommendation:

- Do not add CI.
- Do not add a rule unless it defines future product direction.
- Capture as PR context or release note if needed.
