# Output Contract

## Required Artifacts

Write artifacts under:

```text
plans/review-code-dev/runs/<YYYYMMDD-HHMMSS>-<repo-slug>/
```

Required files:

- `run-metadata.json`
- `recon.md`
- `context.json`
- `coverage.md`
- `review.md`
- `review.json`

Optional files:

- `delegation-brief.md`
- `agent-budget.json`
- `phase-timing.json`
- `subagents.md`
- `scope-audit.md`
- `rejected-findings.md`
- `candidate-findings.json`
- `loop-state.json`
- `artifact-validation.md`
- `adversarial-review.md`
- `verification.md`
- `fix-report.md`
- `notes.md`
- `pr-link.md`

`delegation-brief.md`, `agent-budget.json`, `phase-timing.json`, and `subagents.md` are required when the main context launches an isolated primary reviewer.

## Non-Committable Artifacts

`plans/review-code-dev/` is a temporary review-artifact root, not repository source. Before any
artifact is written inside a Git worktree, run `scripts/prepare_review_run.py` and use the `run_dir`
it returns. The script must:

- add `/plans/review-code-dev/` to `.git/info/exclude` if the local Git exclude file does not
  already ignore it
- verify the ignore rule with `git check-ignore`
- refuse to continue if any path under `plans/review-code-dev/` is already tracked in Git
- write `run-metadata.json` into the run directory with `non_committable: true`

If any of those checks fail, stop the review and report the artifact-path problem instead of writing
review files that could be staged or committed.

## subagents.md

Create `subagents.md` whenever you use a primary reviewer, focused sub-reviewers, or inline focused checks.

Use this structure:

```markdown
# Sub-Reviewer Log

## Plan
| ID | Adapter | Requested model/effort | Effective model/effort | Focus | Scope | Reason |
| --- | --- | --- | --- | --- | --- | --- |
| primary-reviewer | helper | gpt-5.6-luna/max | unknown | full review | context.json | isolate review context |
| security-1 | helper | gpt-5.6-luna/max | unknown | auth/input validation | src/api/* | security-sensitive diff |

## Results
| ID | Status | Candidate findings | Accepted findings | Notes |
| --- | --- | --- | --- | --- |
| primary-reviewer | complete | 2 | 2 | artifacts written |
| security-1 | complete | 1 | 0 | candidate rejected; existing middleware blocks it |

## Raw Summaries
### primary-reviewer
<short pasted summary of artifact status and accepted finding count>

### security-1
<short pasted summary, not hidden reasoning>
```

`Adapter` is the mechanism used: `helper`, `child-context`, `agent-process`, `inline`, or a clearer runtime-specific label when useful. Use `unknown` for unreported effective routing and `null` for unavailable usage. Do not include hidden reasoning or secret values.

## candidate-findings.json

Create `candidate-findings.json` whenever any candidate finding was considered, including candidates from the primary reviewer, focused sub-reviewers, or inline focused checks.

Use this shape:

```json
{
  "candidates": [
    {
      "id": "security-1-001",
      "source": "security-1",
      "confirming_sources": ["primary-direct"],
      "priority": "P1",
      "category": "security",
      "confidence": 8,
      "fingerprint": "src/auth.ts:42:security:auth-middleware-order",
      "file": "src/auth.ts",
      "line": 42,
      "title": "Short concrete title",
      "description": "Candidate impact description.",
      "evidence": [
        {
          "file": "src/auth.ts",
          "line": 42,
          "quote": "Short sanitized source excerpt or symbol name.",
          "why_it_matters": "This is the line that makes the failure path reachable."
        }
      ],
      "failure_path": "Unauthenticated request reaches route before middleware rejects it.",
      "introduced_risk": "The diff moved the route registration ahead of the auth guard.",
      "false_positive_check": "Checked adjacent middleware and route caller; no later guard rejects this path.",
      "status": "accepted",
      "status_reason": "Verified against middleware and route caller.",
      "root_cause_key": "auth-middleware-order"
    }
  ]
}
```

Allowed `status` values: `accepted`, `downgraded`, `duplicate`, `rejected`, `unverified`.

`confidence` is 1-10. Only `accepted` or `downgraded` findings that remain real issues after lead-reviewer verification may appear in `review.md`. Published findings normally require confidence `>= 7`; lower-confidence findings belong in `rejected-findings.md`, `subagents.md`, or `notes.md` unless they are concrete P0/P1 risks with uncertainty stated in the impact.

Every candidate needs a stable `fingerprint`:

```text
<file>:<line-or-contract>:<category>:<root-cause-key>
```

Use `confirming_sources` when multiple helpers report the same root cause. Keep duplicates in `candidate-findings.json` with `status: "duplicate"`; publish the root cause once.

## scope-audit.md

Create `scope-audit.md` for standard/deep reviews when intent or scope materially affects the review.

Use this structure:

```markdown
# Scope Audit

| Item | Result | Notes |
| --- | --- | --- |
| Intent source | delegation brief, PR description | User asked for auth cleanup and export fixes |
| Scope classification | clean | Diff matches stated intent |
| Missing requirements | none | No stated requirement was absent |
| Scope drift | none | No unrelated behavior found |
| Unverifiable areas | external billing webhook | Consumer repo unavailable |
```

Allowed scope classifications: `clean`, `scope_drift`, `missing_requirement`, `unverifiable`.

Do not put scope notes in `review.md` unless the drift or missing requirement creates a concrete P0-P3 issue.

## adversarial-review.md

Create `adversarial-review.md` when a red-team or independent challenge pass is used.

Use this structure:

```markdown
# Adversarial Review

| Adapter | Scope | Candidates | Accepted | Notes |
| --- | --- | --- | --- | --- |
| helper | changed auth/export paths | 2 | 1 | one candidate duplicated security-1 |

## Challenge Prompt
<short non-secret summary of the prompt or focus>

## Outcome
<short summary of what the primary reviewer accepted, rejected, or downgraded>
```

The adversarial pass is advisory. Its candidates still need lead-reviewer verification in `candidate-findings.json`.

## loop-state.json

Create `loop-state.json` for standard and deep reviews, and for quick reviews when any focused helper or artifact repair is used.

Use this shape:

```json
{
  "effort": "standard",
  "rounds_completed": 1,
  "max_rounds": 2,
  "artifact_repairs_used": 0,
  "max_artifact_repairs": 1,
  "checks_planned": ["primary-direct", "frontend-1", "tests-1"],
  "checks_run": ["primary-direct", "frontend-1"],
  "checks_skipped": [
    {
      "id": "tests-1",
      "reason": "helper budget used by higher-risk frontend review"
    }
  ],
  "stop_reason": "coverage_complete_and_findings_classified"
}
```

Use stable check IDs such as `frontend-1`, `backend-1`, `tests-1`, `security-1`, and `architecture-1`. Do not rerun the same check ID in the same round.

## artifact-validation.md

Create `artifact-validation.md` after writing final review artifacts.

Use this structure:

```markdown
# Artifact Validation

| Check | Status | Notes |
| --- | --- | --- |
| review.md parseable | pass | review.json generated |
| changed-file coverage | pass | 6/6 files covered |
| candidate classification | pass | all candidates classified |
| confidence and evidence | pass | accepted findings have confidence, fingerprint, evidence, failure path, introduced risk, and false-positive check |
| subagent log | pass | 2 helpers recorded |

Stop reason: coverage_complete_and_findings_classified
```

If validation fails, perform at most one artifact-only repair. If it still fails, keep the partial artifacts and mark the review incomplete in `artifact-validation.md`.

## delegation-brief.md

Create `delegation-brief.md` whenever the main context launches an isolated primary reviewer. This file is the complete context packet for the primary reviewer; do not include the full conversation or hidden reasoning.

Use this structure:

```markdown
# Delegation Brief

## User request
<one or two sentences>

## Work summary
- <facts known from the request or local context>

## Explicit user-requested changes
- <requested changes only>

## Agent implementation choices already made
- <agent choices when relevant>

## Review scope
- Repository root: <absolute path>
- Mode: <uncommitted|base|commit|custom>
- Effort: <quick|standard|deep>
- Context file: <RUN_DIR>/context.json
- Artifacts directory: <RUN_DIR>
- Changed files: <list or "see context.json">
```

## context.json

The collector emits a common envelope for every git-backed mode. `changed_files` is the reviewed scope. `worktree_changed_files`, `status_porcelain`, `staged_diff`, `untracked_files`, and `untracked_file_previews` describe the current local worktree so branch and commit reviews can still notice relevant local changes.

```json
{
  "repo_root": "/path/to/repo",
  "current_branch": "feature/example",
  "github_repo": {
    "owner": "owner",
    "repo": "repo"
  },
  "requested_mode": "auto",
  "mode": "base",
  "changed_files": ["src/file.ts"],
  "worktree_changed_files": ["src/file.ts", "src/new.ts"],
  "status_porcelain": " M src/file.ts\n?? src/new.ts\n",
  "diff": {
    "text": "...",
    "truncated": false,
    "byte_length": 1234
  },
  "staged_diff": {
    "text": "...",
    "truncated": false,
    "byte_length": 123
  },
  "untracked_files": ["src/new.ts"],
  "untracked_file_previews": [
    {
      "path": "src/new.ts",
      "text": "...",
      "truncated": false,
      "byte_length": 456
    }
  ]
}
```

Mode-specific additions:

- `uncommitted`: includes unstaged `diff`, `staged_diff`, local status, untracked files, and untracked previews.
- `base`: includes `base_branch`, `diff_ref`, branch `diff_stat`, and branch `diff` against the selected ref.
- `commit`: includes `commit.hash`, `commit.short_hash`, `commit.subject`, commit `diff_stat`, and commit patch `diff`.
- `custom`: includes local status fields and any user prompt in `custom_prompt`.

Untracked previews must not copy secret values. Secret-like filenames are skipped, and secret-looking values in text previews are redacted.

When git is unavailable, create a minimal equivalent manually and state the limitation in `recon.md` or `notes.md`.

## review.md

`review.md` is the parser input. It must contain only confirmed findings, or exactly `No issues found.` Do not append coverage, assumptions, summaries, recommendations, or artifact lists to `review.md`; put those in the other artifacts.

Use this exact finding shape:

```markdown
**[P1] src/file.ts:42 - Short concrete title**
One or two sentences explaining the issue, why this diff introduced it, and the likely impact.
```

If no issues are found, write:

```markdown
No issues found.
```

## review.json

The parser emits:

```json
{
  "issues": [
    {
      "priority": "P1",
      "file": "src/file.ts",
      "line": 42,
      "title": "Short concrete title",
      "description": "Impact description"
    }
  ]
}
```

No issues:

```json
{
  "issues": []
}
```

## User Response

Lead with findings, not process. Use this shape:

```markdown
Found 2 issues.

**[P1] src/file.ts:42 - Short concrete title**
Impact sentence.

**[P2] src/other.ts:10 - Short concrete title**
Impact sentence.

Artifacts: plans/review-code-dev/runs/<run>/
```

If no issues:

```markdown
No issues found.

Reviewed N changed files. Artifacts: plans/review-code-dev/runs/<run>/
```
