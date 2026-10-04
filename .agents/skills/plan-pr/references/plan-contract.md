# Plan artifact and handoff

The main artifact is a human decision document that the same agent can execute. Keep decisions in one place. Reference existing repository procedures instead of copying their full checklists.

Use this structure proportionally; merge tiny sections and omit inapplicable ones:

```markdown
# <Outcome>
Status: proposed | approved | implementing | ready-for-delivery
Sources: <original prompt, ticket/spec links, scoped context>
Repository baseline: <branch/commit and relevant dirty state>

## Outcome and scope
<What users can do/observe afterwards; explicit non-goals>

## Approach and decisions
<Chosen mechanism and why it meets the need>
<Requirement vs verified fact vs assumption; material alternatives and tradeoffs>
<Approved decisions vs tunable implementation details>

## Acceptance and evidence
| Criterion | Implementation slice | Proof |
| ... | ... | test boundary/scenario or rendered check |

## Implementation
<Small ordered vertical slices; code areas; failures to test; actual dependencies>
<Only relevant skills: name, phase, assignment, availability>

## Validation
Shared guide: <path, existing repo instructions, or absent>
<Task-specific tests, UI states/viewports/data, mockup reference and comparison>
<Known unavailable/optional checks; required checks remain required>

## Approval and execution notes
<Remaining consequential questions, if any>
<After approval: revision approved, user decision, completed steps and deviations>

## Delivery
<Populated ship-pr-dev instruction from SKILL.md, including source/plan/evidence paths>
```

For API work, choose relevant successful, invalid-input, authorization and failure responses at the public contract; include data/migration implications when present. For UI, identify observable improvements and interactions; screenshots supplement functional proof. Tests that mirror rendering calculations cannot certify the final screen.

Put long code exploration, source detail and command feasibility notes in `evidence.md` only when useful. Separate inspected commands from actually executed checks. Record failures and limitations without claiming verification. No claim of test success, screenshot comparison or subagent review without corresponding observation.

Approval covers the named outcome, boundaries, approach and test interfaces. It is not blanket authorization for new scope, deployment or merging. A request to approve and implement does authorize implementation and the described PR handoff, subject to explicit user constraints.

At execution handoff, include original need, scoped ticket context, approved revision, acceptance criteria, completed slices, deviations and validation instructions. Ship PR checks criterion → delivered behavior → evidence in its existing review; this plan does not prescribe a second code-review system.
