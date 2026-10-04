# Promotion Matrix

Use this reference to choose the right home for a learning.

## Destination Rules

| Learning type | Best destination | Use when | Avoid when |
| --- | --- | --- | --- |
| Shared project convention | `AGENTS.md` | Agents and developers should know it before editing | It can be enforced by a test |
| Claude-only behavior | `CLAUDE.md` | The rule is specific to Claude Code or Claude commands | The rule should apply to Codex/Cursor too |
| Path-specific behavior | folder agent rules | Only one package, app, or directory needs it | It affects the whole repo |
| Reproducible bug | test | The bad behavior can be asserted | The behavior is subjective or unstable |
| Missing verification | CI workflow | The check should block or warn on PRs | It is too slow/costly for every PR |
| Repeated manual step | script or task command | Humans/agents repeat the same validation | It is cheaper as a short checklist |
| Architecture decision | ADR | Future maintainers need the rationale | It is just an implementation detail |
| Setup knowledge | README/runbook | A person needs steps to run or deploy | It should be enforced automatically |
| Reusable agent behavior | existing skill | The pattern applies across many repos | It is project-specific |
| One-off judgment | no automation | The lesson is not likely to recur | Risk is high or recurrence is likely |

## Priority

- **P0**: Security, data loss, billing, compliance, production outage, destructive command, or irreversible deployment risk.
- **P1**: Repeated CI breakage, broken user flow, failed release, expensive review loop, or cross-team confusion.
- **P2**: Recurring manual work, unclear setup, common agent drift, or missing local validation.
- **P3**: Helpful documentation, examples, cleanup, or optional hardening.

## Decision Ladder

1. Can a machine reliably catch it?
2. If yes, should it run locally, in PR CI, nightly, or at release?
3. If no, who needs to know it before acting?
4. Is the rule shared across agents or specific to one host?
5. Is the cost lower as a template, script, doc, or checklist?
6. Is it actually worth automating?

## Output Requirement

Every recommendation should include:

- destination
- exact proposed change
- why that destination is better than alternatives
- cost or maintenance burden
- verification step
