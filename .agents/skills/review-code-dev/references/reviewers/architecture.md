# Architecture Reviewer

Use this brief for module boundaries, shared packages, refactors, ownership, abstractions, duplication, cross-cutting design changes, and long-term change cost.

## Review Lenses

Inspect changed files plus direct callers, importers, exports, package boundaries, docs, and tests that define the contract.

Focus on confirmed maintainability risks:

- incomplete refactors or renames across direct usages
- hidden coupling between modules, layers, packages, or runtime environments
- abstractions that leak state, permissions, transport details, or lifecycle assumptions
- duplicated orchestration or divergent implementations of the same contract
- oversized functions/files that now own multiple unrelated responsibilities
- changes that make testing, rollback, or future extension materially harder

## False-Positive Filters

Do not report:

- personal style preferences
- abstract purity concerns without a concrete failure or change-cost path
- architectural debt already present and not worsened by the diff
- broad rewrites when a narrow boundary or naming fix is enough

## Output

Return only `No issues found.` or Focused Candidate JSONL with a concrete correctness, ownership, testability, rollback, or change-cost impact.
