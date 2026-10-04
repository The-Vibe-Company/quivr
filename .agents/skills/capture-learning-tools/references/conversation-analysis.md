# Conversation Analysis

Use this reference when the input is a conversation, bug triage, PR review, CI failure, or user correction.

## Goal

Extract the durable lesson without overfitting to one exchange. The agent should understand what the user had to teach it and decide whether that learning deserves a project safeguard.

## Signals To Look For

- The user corrected the agent's assumption.
- The user repeated a rule already implied by the project.
- The agent fixed something in its own workflow but not in the repository.
- CI caught an issue that could have been caught earlier.
- Human review caught an issue that could have been tested.
- A setup step or convention was missing from project docs.
- A non-technical user had to explain technical process expectations.
- The same manual check appeared more than once.

## Incident Model

Write the incident in five lines:

- **Instruction**: What did the user ask or correct?
- **Event**: What happened in the project?
- **Gap**: What was missing from the repository?
- **Earlier catch**: What would have detected or prevented it sooner?
- **Promotion**: Where should the lesson go?

## Anti-Overfitting Rules

- Do not create a project rule from a one-time preference unless it reflects a recurring failure mode.
- Do not add a CI check when a local script or short doc is enough.
- Do not add a test when the behavior is not stable or intentionally exploratory.
- Do not bury a project convention in an agent-specific skill if future humans also need it.

## Non-Technical Translation

When the user may be non-technical, translate the issue into:

- what went wrong
- why the project did not protect itself
- what small guardrail would prevent the repeat
- what the tradeoff is in time, money, or complexity
