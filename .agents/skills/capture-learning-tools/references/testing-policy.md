# Testing Policy

Use this reference before recommending tests.

## Gold Standard

Add the narrowest test that would have failed before the fix and will keep failing if the bug returns.

## Test Choice

| Failure mode | Recommended test |
| --- | --- |
| Pure function or business rule | unit test |
| API contract or schema | contract/schema test |
| Database migration or seed issue | migration/integration test |
| Content/frontmatter/config drift | fixture/schema validation |
| UI interaction bug | component or browser e2e test |
| Accessibility regression | accessibility check |
| Visual layout regression | visual/screenshot test when stable |
| Build/type drift | typecheck/build command |
| Deployment packaging issue | smoke test or release check |

## When Not To Add A Test

- The behavior is exploratory or intentionally changing.
- The issue is a one-off setup misunderstanding.
- The test would be flaky without a stable harness.
- A static validator or typecheck catches it more directly.
- The cost of the test is higher than the recurrence risk.

## Regression Test Bar

A good regression recommendation states:

- where the test belongs
- what fixture or scenario it uses
- the assertion
- the command that runs it
- whether it should run locally, in PR CI, nightly, or release

## Coverage

Do not recommend coverage percentage as the primary fix unless the problem was genuinely missing breadth across a stable surface. Prefer targeted tests tied to the failure mode.

## Example

Instead of:

```markdown
- Add more tests around content.
```

Recommend:

```markdown
- Add a fixture test that loads every Markdown content file, validates required frontmatter keys against the schema, and fails on unknown or missing fields. Run it in `npm run validate:content` and PR CI for changes under `content/**`.
```
