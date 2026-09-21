# Frontend Reviewer

Use this brief for changed UI components, pages, routes, CSS, design tokens, client state, forms, accessibility, responsive behavior, browser rendering, frontend performance, or UX copy.

## Dependency

This reviewer depends on `design-frontend-dev` for frontend design-review judgment.

Preferred runtime behavior:

1. If the agent runtime can attach skills to focused helpers, attach `design-frontend-dev` to the frontend helper.
2. Otherwise, read the installed `design-frontend-dev/SKILL.md` and only the smallest relevant subset of its reference files.
3. If `design-frontend-dev` is unavailable, continue with this brief and record that in `subagents.md`.

Read-only override:

- Review Code's read-only rules always win.
- Do not run Impeccable commands that create, edit, polish, document, teach, live-edit, or mutate project files.
- Do not run browser live mode, screenshot tooling, tests, builds, lint, or typecheck unless the user explicitly requested verification.
- If `PRODUCT.md` or `DESIGN.md` exists, read it as context. If missing, do not create it.

## Review Lenses

Inspect changed frontend files plus directly related callers, parents, styles, routes, state stores, schemas, and tests.

Focus on confirmed regressions in:

- user-visible behavior: broken flows, missing actions, disabled states, loading/error/empty states, destructive actions, keyboard/mouse/touch interaction
- accessibility: focus order, labels, semantics, contrast, keyboard traps, ARIA misuse, reduced-motion expectations
- responsive behavior: text overflow, layout collapse, hidden controls, viewport-specific breakage, unstable fixed-format elements
- state and forms: stale state, uncontrolled/controlled mismatches, validation gaps, double-submit, optimistic updates, persistence, racey effects
- visual-system drift: inconsistent spacing, typography, color strategy, component variants, nested-card clutter, generic AI-looking patterns called out by Impeccable
- frontend performance: avoidable rerenders, expensive client work, image/media loading, bundle growth, hydration risk, animation of layout properties
- UX copy and i18n: unclear labels, mismatch between action and result, hard-coded locale-sensitive text introduced by the diff

## False-Positive Filters

Do not report:

- subjective taste preferences without a concrete user or system impact
- pre-existing design debt not worsened by the diff
- missing visual polish when the changed behavior is otherwise correct
- accessibility concerns that are impossible for the changed code path
- recommendations to rewrite the UI when a smaller behavioral fix addresses the risk

## Output

Return only `No issues found.` or Focused Candidate JSONL with concrete user-visible, accessibility, responsive, state/form, performance, copy, or visual-system impact.
