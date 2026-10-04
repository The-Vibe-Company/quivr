# Project Memory

Use this reference when the learning is durable but not best expressed as a test or CI check.

## Gold Standard

The repository should contain enough memory that a new contributor or agent can make good decisions without reading old conversations.

## Memory Destinations

| Need | Destination |
| --- | --- |
| How to work in this repo | `AGENTS.md` |
| Claude-specific adapter | `CLAUDE.md` |
| Setup steps | `README.md` or `docs/setup.md` |
| Deployment steps | `docs/deploy.md` or runbook |
| Architecture rationale | `docs/adr/NNN-title.md` |
| Repeated task | script plus docs |
| PR/release checklist | `.github/pull_request_template.md` or `docs/release.md` |
| Ownership/review expectations | CODEOWNERS, docs, or agent instructions |
| Domain vocabulary | `docs/glossary.md` or product docs |

## ADR Template

Use ADRs for choices that future maintainers may want to revisit.

```markdown
# ADR NNN: <Decision>

## Context
<why the choice was needed>

## Decision
<what we chose>

## Consequences
<tradeoffs, risks, follow-up>
```

## Documentation Quality Bar

- Write for the next person, not for the current thread.
- Include commands that can be run.
- Link to source files when useful.
- Do not store secrets.
- Keep project-specific rules in the project, not in a personal skill.
