# Finding Rubric

## Severity

### P0 - Critical

Must fix before commit or merge:

- exploitable security vulnerability
- auth bypass, privilege escalation, or data leak
- destructive migration or data corruption risk
- crash or outage on a common production path
- broken release/deploy path with high confidence

### P1 - High

Should fix before merge:

- real logic bug on an important path
- incomplete refactor or API contract break
- missing error handling around important external or persistence boundary
- resource leak, race condition, or retry bug with likely impact
- billing, permission, privacy, or export behavior that can affect users

### P2 - Medium

Fix soon:

- edge case with plausible user impact
- validation gap, weak type boundary, or unchecked null/empty state
- performance issue with realistic scale impact
- maintainability issue likely to slow future work or hide bugs
- test gap for risky behavior introduced by the diff
- commit message or commit scope that materially misleads reviewers, release notes, or future rollback work

### P3 - Low

Nice to have:

- small clarity or structure improvement with concrete future value
- low-risk test/readability improvement

Do not use P3 for generic style preferences.

## Categories

- correctness
- security
- privacy/data handling
- performance
- concurrency/state
- API/contract compatibility
- migrations/persistence
- tests
- maintainability/design quality
- tooling/deployment
- documentation when docs materially mislead implementation or operations
- commit quality when reviewing a commit/ref

## Do Not Report

- formatting, import order, or cosmetic style
- missing features outside the requested behavior
- speculative issues without a concrete failure path
- pre-existing issues unless the current diff makes them newly risky or harder to reason about
- test gaps on trivial code unless the changed behavior is risky
- "could be cleaner" without a specific simpler structure and impact

## Design Quality Findings

Report design-quality findings only when the diff creates a concrete future cost or hides likely bugs. Good candidates include:

- ad-hoc conditionals, scattered special cases, one-off flags, nullable modes, or branches inserted into unrelated flows
- busy functions/modules that now need a dedicated helper, typed model, state machine, policy object, or owned abstraction
- thin wrappers, identity abstractions, pass-through helpers, or generic magic that add indirection without reducing real complexity
- unnecessary optionality, casts, `any`, `unknown`, or loosely shaped objects where an explicit contract would clarify invariants
- feature logic leaking into shared paths, implementation details leaking through APIs, near-duplicates, or logic added outside the canonical layer
- changed files crossing roughly 1000 lines when the new code can be decomposed into focused helpers, modules, or components
- unnecessary sequential orchestration or non-atomic partial updates where independent work or related updates have an obvious cleaner structure

Prefer findings that point to a concrete simplification. Do not report cosmetic nits as design findings.

## Finding Quality Bar

Each finding must answer:

- What changed?
- Why is it wrong or risky?
- Where is the evidence?
- What user/system impact can happen?
- Why is the severity appropriate?

Reject or downgrade findings when:

- the cited line is wrong
- existing code already handles the case
- the behavior is documented as intentional
- the issue only exists under impossible inputs
- the fix would be riskier than the problem
