# Agent Instructions

Use this reference when proposing or editing `AGENTS.md`, `CLAUDE.md`, `.claude/rules/`, `.cursor/rules/`, `.github/copilot-instructions.md`, or similar files.

## Gold Standard

Good agent instructions are:

- short enough to read at startup
- concrete enough to act on
- tied to repository files, commands, or ownership
- written for future humans and agents
- checked by tests/scripts/CI when possible
- free of secrets and private account assumptions

## Source Of Truth

Prefer one shared source of truth:

1. Use `AGENTS.md` for shared project instructions when Codex or multiple agents should read them.
2. Use `CLAUDE.md` as a Claude adapter when Claude Code is used.
3. If `CLAUDE.md` and `AGENTS.md` should be identical, either symlink one to the other or make `CLAUDE.md` import `AGENTS.md`, depending on the project's portability needs.
4. Keep host-specific details in host-specific adapters, not in duplicated copies of shared rules.

## Inspection Checklist

Before recommending edits, inspect:

- whether `AGENTS.md` exists
- whether `CLAUDE.md` exists
- whether either file is a symlink
- whether both files resolve to the same target
- whether `CLAUDE.md` imports `AGENTS.md`
- whether nested or path-specific rule files already exist
- whether the proposed rule duplicates an existing rule

## When `CLAUDE.md` Exists But `AGENTS.md` Does Not

Default proposal:

1. Create `AGENTS.md` with shared project rules.
2. Move shared rules from `CLAUDE.md` into `AGENTS.md`.
3. Keep `CLAUDE.md` as an adapter with an import such as `@AGENTS.md` plus Claude-only notes, if supported by the host.
4. If the environment prefers symlinks and no Claude-only notes are needed, propose symlinking `CLAUDE.md` to `AGENTS.md`.

Explain the tradeoff in plain English: "one shared rulebook, small adapters for each assistant."

## Rule Quality Bar

Use this shape:

```markdown
- When <condition>, run/check/read <specific thing> before <risky action>. This prevents <failure mode>.
```

Prefer:

```markdown
- When editing content frontmatter, run `npm run validate:content` before finishing. This catches schema drift before review.
```

Avoid:

```markdown
- Be careful with frontmatter.
```

## References

- OpenAI Codex `AGENTS.md` guide: https://developers.openai.com/codex/guides/agents-md
- Anthropic Claude Code memory and `CLAUDE.md`: https://docs.anthropic.com/en/docs/claude-code/memory
