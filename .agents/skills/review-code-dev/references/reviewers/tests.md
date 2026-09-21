# Tests Reviewer

Use this brief when the diff changes behavior, test utilities, fixtures, mocks, coverage patterns, or regression-prone code.

## Review Lenses

Inspect changed files plus nearby tests and established test patterns.

Focus on material test gaps:

- behavior introduced or changed by the diff has no assertion in the closest existing test layer
- edge cases that the implementation explicitly handles are not asserted and could regress silently
- mocks or fixtures diverge from real schemas, permissions, API responses, or time/state behavior
- tests assert implementation details while missing the user/system outcome
- renamed/refactored behavior leaves stale tests that still pass but no longer protect the contract

## False-Positive Filters

Do not report:

- generic coverage percentage complaints
- missing tests for trivial copy, comments, config-only changes, or pure dead-code deletion
- tests that would be nice but do not materially reduce risk
- gaps already covered by a stronger integration or end-to-end test nearby

## Output

Return only `No issues found.` or Focused Candidate JSONL. Each test candidate must cite the changed production file line and mention the closest existing test pattern when visible.
