# Critique

Run an independent UX and visual-quality critique of a frontend surface. This clean edition does not use the upstream detector CLI, live overlay, or local storage scripts. Use direct file reads, browser inspection when available, screenshots when useful, and focused sub-agents when the host runtime supports them.

## Scope

Resolve one stable target:

- source file or component path when reviewing code
- route or URL when reviewing rendered behavior
- screenshot or design artifact when reviewing a static surface

Prefer source paths over drifting local ports when both identify the same surface.

## Assessment

When sub-agents are available and allowed, run two independent passes:

- **Design director pass**: hierarchy, composition, emotional fit, brand/product register, typography, color, spacing, copy, state design, and AI-looking patterns.
- **Implementation evidence pass**: accessibility, responsive behavior, semantic HTML, focus/keyboard behavior, form states, loading/error/empty states, performance, tokens, and design-system drift.

If sub-agents are unavailable, run the two passes sequentially and keep their notes separate until synthesis.

## Lenses

Evaluate:

- AI slop: obvious generated tropes, generic SaaS grammar, unearned gradients, glass, hero metrics, identical card grids, weak font/palette reflexes.
- Visual hierarchy: can the user identify primary, secondary, and tertiary actions quickly?
- Information architecture: does the surface reveal complexity at the right pace?
- Accessibility: contrast, labels, semantic structure, focus states, keyboard path, touch targets, reduced motion.
- Responsive behavior: mobile/tablet/desktop layout, text overflow, fixed elements, long content, viewport-specific breakage.
- Interaction quality: all states, feedback timing, optimistic/pessimistic choices, destructive action safeguards.
- UX copy: clarity, action labels, error recovery, empty states, localization risk.
- Design-system fit: tokens, shared components, spacing, typography, icon style, motion vocabulary.

## Output

Return a concise critique with this structure:

```markdown
# Impeccable Critique

## Verdict
Score: ?/10
AI-slop verdict: pass/fail with one sentence

## Top Findings
**[P1] Surface or file - Concrete title**
Impact, evidence, and fix direction.

## Strengths
- What is already working and should be preserved.

## Next Pass
- The highest-leverage next action.
```

Do not bury findings in praise. Do not recommend broad rewrites when a smaller fix addresses the issue.
