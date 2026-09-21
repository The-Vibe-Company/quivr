# Non-Technical Mode

Use this reference when the user is not clearly technical, asks in business language, or wants a decision rather than implementation details.

## Communication Rules

- Start with the practical consequence.
- Avoid unexplained terms like runner, matrix, artifact, lint, snapshot, or hook.
- Explain CI as "an automatic check that runs before changes are accepted."
- Explain tests as "small examples that prove the old mistake cannot return."
- Explain agent instructions as "the project rulebook assistants read before working."
- Explain cost tradeoffs plainly.

## Output Shape

```markdown
## Plain-English Verdict
<one paragraph>

## What I Would Add
- <guardrail> because <benefit>

## What I Would Not Add
- <thing> because <cost/risk>

## Technical Detail
<only as much as needed>
```

## Useful Phrases

- "This is worth automating because it is likely to happen again."
- "This is better as documentation because a computer cannot reliably check it."
- "This is better as a test because the expected behavior is clear."
- "This is not worth CI yet because it would add cost without catching much."
- "The safe setup is one shared rulebook and small adapters for each assistant."
