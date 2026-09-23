# Repository setup

Read this for explicit setup or missing reusable project defaults. Setup is an internal mode of this skill, not another public skill.

Inspect existing agent instructions, tracker docs, package scripts, CI, design docs and runbooks first. Reuse them with links. Ask only for missing preferences: tracker/provider mapping and whether/how applications should be launched for final validation. A preference already stated in this session needs no second confirmation.

If the user wants persisted setup, create or update two small committed-in-intent Markdown files. Do not commit them automatically. Offer a minimal pointer from existing agent instructions when appropriate; preserve other instructions.

## `docs/agents/plan-pr.md`

```markdown
# Planning conventions

## Work sources
- Default tracker: <Linear / GitHub / GitLab / other / none>
- Issue patterns and projects: <unambiguous mapping or link to existing tracker doc>
- Access: <available connector or documented CLI; no credentials>
- Context: read issue body, relevant comments, parent, children and dependencies.

## Project conventions
- Architecture/domain: <existing doc paths>
- Design system and mockups: <shared references>
- Validation guide: docs/agents/plan-pr-validation.md
```

The skill routes are curated in the skill itself. Project configuration may constrain them through local rules; it does not discover, install or invent replacement skills.

## `docs/agents/plan-pr-validation.md`

```markdown
# Implementation validation

## Application access
- Launch policy: <enabled / disabled / unspecified>
- Services, commands and working directories: <verified instructions or existing runbook links>
- Readiness/health checks and local URL: <known values>
- Test data/auth setup: <non-secret instructions; references to local credential setup>
- Shutdown: stop only processes started for this validation.

## Technical checks
- Required checks: <existing CI/scripts/runbook links>
- Optional checks: <commands or links>

## Visual evidence
- Browser/device conventions: <project defaults, if any>
- Capture format/location and approved PR attachment mechanism: <existing workflow, if any>
- If launch is disabled or optional tooling unavailable: report the limitation.
```

`enabled` authorizes following the documented launch procedure. `disabled` means do not start services; an existing accessible preview may still be inspected unless forbidden. `unspecified` means follow applicable repo launch instructions when present; otherwise skip launching and explain. Do not guess service commands, provision infrastructure, or ask for secrets in chat.

This guide is for all contributors. Keep task-specific screens, client scenarios, acceptance fixtures and one-machine paths in the task plan or local untracked configuration. Store credential values outside the repo using the project's approved mechanism. A shared guide can reference environment-variable names and setup documentation.
