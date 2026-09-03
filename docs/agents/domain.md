# Domain documentation

How engineering skills and agents consume this repository's domain documentation.

## Layout

Quivr V2 is currently a single-context repository:

```text
/
├── CONTEXT.md
├── docs/adr/
└── src/
```

- `CONTEXT.md` is the canonical glossary for the domain-neutral Quivr engine.
- `docs/adr/` contains repository-wide architecture decisions when they are created.
- Agency Customer terms and other vertical-specific concepts belong in plugins or their own repositories, not in the core glossary.

## Before exploring or implementing

- Read `CONTEXT.md` before naming domain concepts, designing interfaces, writing tests, or drafting tracker issues.
- Read the ADRs in `docs/adr/` that touch the area being changed.
- If a referenced domain file or ADR directory does not exist, proceed silently. `/domain-modeling` creates documentation lazily when a term or decision is actually resolved.

## Use the glossary's vocabulary

Use the terms defined in `CONTEXT.md` in issue titles, specifications, interfaces, test names, and documentation. Do not drift toward synonyms the glossary explicitly marks with `_Avoid_`.

If a required concept is missing, first question whether it is unnecessary or vertical-specific. When it is a genuine engine concept, use `/domain-modeling` to define it before spreading a new term through the project.

## Respect architecture decisions

Surface any conflict with an existing ADR explicitly instead of silently overriding it:

> Contradicts ADR-0007, but is worth reopening because...

Record a new hard-to-reverse architecture decision in `docs/adr/` with the next available numeric prefix. Do not use an ADR for an easily reversible implementation detail.
