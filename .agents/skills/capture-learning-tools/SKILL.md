---
name: capture-learning-tools
description: "Turn technical conversations, bugs, CI failures, review comments,
  and repeated agent mistakes into durable project improvements. Use when the
  user asks what should be automated, documented, tested, added to
  AGENTS.md/CLAUDE.md, wired into CI, or changed so the same issue does not
  happen again. This skill is for both non-technical and technical users: it
  explains the learning in plain English, inspects the repository, chooses the
  right prevention layer, proposes changes first, and only edits project files
  when explicitly asked."
metadata: {}
allowed-tools: Bash Read Write Edit Grep Glob Agent
---

# Engineering Learning Loop

This skill is the prevention layer after technical work. It analyzes the conversation and the repository, then promotes useful lessons into the project itself: agent instructions, tests, CI, scripts, templates, docs, or architecture decisions.

The project is the source of truth. Do not depend on private memory systems, personal vaults, or one agent host. A good outcome makes the repository easier for the next human or agent to work in.

## Protected Invariants

1. Propose before editing. Apply changes only when the user explicitly asks or the task clearly requests implementation.
2. Prefer enforceable safeguards over prose. If a test, script, or CI check can catch the issue reliably, recommend that before adding a reminder.
3. Keep non-technical users oriented. Start with a plain-English verdict before technical details.
4. Keep instructions portable. Do not hard-code private paths, organizations, account names, secrets, or tool-only assumptions.
5. Do not turn every mistake into a rule. Some learnings are one-off judgment calls and should be left manual.
6. Never stage, commit, push, create a PR, or change billing-sensitive CI behavior unless explicitly asked.

## Reference Routing

Read only what the task needs:

- `references/conversation-analysis.md` when extracting lessons from a conversation, transcript, review, bug, or user correction.
- `references/promotion-matrix.md` for deciding whether the lesson belongs in instructions, tests, CI, scripts, docs, ADRs, or nowhere.
- `references/agent-instructions.md` when changing or proposing `AGENTS.md`, `CLAUDE.md`, `.claude/rules/`, Cursor rules, or other agent guidance.
- `references/ci-policy.md` before recommending CI, especially for private repositories, paid runners, long checks, or open-source projects.
- `references/testing-policy.md` before recommending regression tests or coverage changes.
- `references/project-memory.md` when the repository needs a durable place for decisions, runbooks, templates, or recurring project knowledge.
- `references/cross-agent-linking.md` when multiple agent hosts need the same instructions.
- `references/non-technical-mode.md` when the user is not clearly technical or asks for a simple explanation.
- `references/examples.md` for concrete before/after patterns.

Use scripts when helpful:

- `scripts/inspect_project_guidance.py --cwd <repo>` finds `AGENTS.md`, `CLAUDE.md`, symlinks, imports, and adjacent agent rule files.
- `scripts/inspect_ci_surface.py --cwd <repo>` summarizes workflows, scripts, and CI cost signals.
- `scripts/classify_learning.py --text "<lesson>"` gives a first-pass destination for a lesson.

## Workflow

### 1. Capture The Learning

Read the current conversation or supplied transcript. Identify:

- the user's instruction, correction, or frustration
- the technical event: bug, CI failure, review comment, missing test, wrong assumption, repeated manual work, unclear setup, or project convention
- the failure mode that should not repeat
- who needs the next safeguard: a non-technical user, a developer, a reviewer, an agent, CI, or deployment

If the request is ambiguous, continue with best judgment and state assumptions. Ask a question only when the missing detail changes the recommended safeguard.

### 2. Inspect The Project

Map the repository before recommending changes:

- project instructions: `AGENTS.md`, `CLAUDE.md`, `.claude/CLAUDE.md`, `.claude/rules/`, `.cursor/rules/`, `.github/copilot-instructions.md`
- verification commands: package scripts, Makefile, task runner config, README, CI workflows
- tests and schemas: unit, integration, e2e, fixtures, migrations, content validators, type checks
- project memory: `docs/`, ADRs, runbooks, templates, changelog, issue/PR templates

When `CLAUDE.md` exists but `AGENTS.md` does not, treat that as a portability gap. Propose a shared `AGENTS.md` plus a `CLAUDE.md` adapter or symlink unless there is a good reason to keep Claude-only instructions.

### 3. Choose The Prevention Layer

Use the promotion matrix:

- reusable project rule -> `AGENTS.md` or equivalent shared project instructions
- Claude-specific instruction -> `CLAUDE.md` adapter after shared instructions
- path-specific behavior -> folder-scoped agent rules
- reproducible bug -> regression test
- existing test not run -> CI wiring
- repeated manual validation -> script or task command
- expensive check -> scheduled, release, or opt-in CI
- architecture/product decision -> ADR or decision record
- setup knowledge -> README or runbook
- generic agent workflow -> existing skill or reusable package
- one-off preference -> do not automate

### 4. Account For CI Cost And Audience

Differentiate:

- public open-source repositories, where standard hosted CI is commonly acceptable
- private repositories, where CI minutes, paid runners, and long checks can create cost
- prototypes, where a documented local preflight may be better than a full CI gate
- production or security-sensitive projects, where slower checks may be justified

If the user is non-technical, explain CI choices in cost/risk terms, not runner jargon.

### 5. Report First

Use this output contract by default:

```markdown
# Engineering Learning Loop Review

## Plain-English Verdict
<what should change and why, in non-technical language>

## Technical Diagnosis
- Conversation signal:
- Project gap:
- Earlier detection point:
- Best prevention layer:

## Recommended Changes
| Priority | Destination | Change | Why | Cost |
| --- | --- | --- | --- | --- |

## Proposed Instruction Text
<exact AGENTS.md/CLAUDE.md/rule text, or "None">

## Proposed Test Or CI
<specific test/check/command and where it should run, or "None">

## Documentation Or Decision Record
<doc/runbook/ADR/template update, or "None">

## Not Worth Automating
<items deliberately left manual and why>

## Apply Plan
1. <smallest safe patch step>
2. <verification step>
```

### 6. Apply Mode

When asked to apply:

1. Re-read target files immediately before editing.
2. Keep patches narrow and reversible.
3. For `AGENTS.md` and `CLAUDE.md`, inspect whether one imports or symlinks the other before editing.
4. Prefer one shared source of truth plus host-specific adapters over duplicated rules.
5. Add or update tests before prose when the regression is machine-checkable.
6. Avoid paid or slow CI expansion unless the user accepts the tradeoff.
7. Run the smallest relevant validation and report what passed or could not be run.

## Gold Standard

A successful run produces fewer future interruptions. The next person or agent can discover the rule, run the check, understand the decision, and avoid repeating the same class of mistake without needing this conversation.
