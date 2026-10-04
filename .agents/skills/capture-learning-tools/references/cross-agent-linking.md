# Cross-Agent Linking

Use this reference when a project has multiple coding assistants or instruction files.

## Goal

Keep one shared rulebook and small host adapters. Avoid copying the same rules into many files.

## Common Files

| Host or tool | Common instruction file |
| --- | --- |
| Codex | `AGENTS.md` |
| Claude Code | `CLAUDE.md` |
| Cursor | `.cursor/rules/` |
| GitHub Copilot | `.github/copilot-instructions.md` |
| Other agents | project-specific docs or adapter files |

## Recommended Layout

```text
AGENTS.md                         # shared project instructions
CLAUDE.md                         # imports or links to AGENTS.md, plus Claude-only notes
.cursor/rules/project.mdc         # references shared rules, plus Cursor-only notes
.github/copilot-instructions.md   # concise adapter for Copilot
docs/adr/                         # durable decisions
```

## Link Strategies

### Import

Use when the host supports file imports and the repository should stay portable across operating systems.

Example Claude adapter:

```markdown
@AGENTS.md

## Claude-only notes
- <only if needed>
```

### Symlink

Use when the team is comfortable with symlinks and the files should be identical.

Tradeoff: simple, but some tools, platforms, or editors may handle symlinks differently.

### Duplicate

Avoid except for tiny host-specific adapters. Duplicated shared rules drift.

## Recommendation Rule

If `CLAUDE.md` exists and `AGENTS.md` does not, propose creating `AGENTS.md` as the shared source and converting `CLAUDE.md` into an adapter unless the project is intentionally Claude-only.
