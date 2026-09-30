# Page types

Each page is one type. The template is a skeleton: keep its order, drop a section only when it has nothing to say.

## Tutorial

A lesson. The reader learns by building something that works, following you step by step. Example: "Build your first plugin".

```
---
title: <outcome in the reader's words>
description: <what they will have built, in one sentence>
keywords: [<3 to 5 search terms>]
---
<one paragraph: what you will build, how long it takes, what you need>

## Before you start      prerequisites as a checklist, each with the command that verifies it
## <Step 1..n>           inside <Steps>; one action per step, with the expected output
## What you built        two or three sentences, then Cards to the next pages
```

Rules: one path, no alternatives; every step ends in a visible result; the whole tutorial runs end to end on a clean checkout.

## How-to

A recipe for a reader who knows what they want. Example: "Connect an RSS feed", "Rebuild a Corpus".

```
---
title: <task as a verb phrase>
description: <when you need this>
keywords: [<3 to 5 search terms>]
---
<one sentence: the goal and the result>

## Prerequisites         only what this task needs
## Steps                 numbered, minimal, with the command or request for each
## Check it worked       the observable result
## Troubleshooting       optional: a table symptom | cause | fix, for failures readers actually hit
```

Rules: start from the reader's goal, not from the feature; link to reference for every field instead of explaining them all.

## Reference

Exact facts, looked up, never read top to bottom. Example: "Plugin contract", "Configuration", "Errors".

```
---
title: <thing described>
description: <what it lists>
keywords: [<3 to 5 search terms>]
---
<one sentence: what this reference covers and its source of truth>

## <Item>                one heading per operation, field group or error family
                         a table: name, type, required, default, meaning
                         one minimal example
```

Rules: complete and consistent; same structure for every item; generated from the contract when a contract exists.

## Explanation

Understanding: why it works this way and how the parts fit. Example: "How plugins work", "Core concepts".

```
---
title: <topic>
description: <the question this page answers>
keywords: [<3 to 5 search terms>]
---
<the answer in two sentences>
<one diagram>

## <Idea 1..n>           each idea: what it is, why it is that way, what it means for the reader
## Next                  Cards to the tutorial or how-to that puts it into practice
```

Rules: no step-by-step instructions (link to them); explain decisions and trade-offs; keep it under about 150 lines.

## Plugin type page

The "Plugin types" overview and each type's how-to share one shape, so a reader can compare types:

| Field | Content |
| --- | --- |
| What it does | one sentence in product terms |
| When Quivr calls it | the moment in the flow (on ingestion, on search, on a schedule, on a Change) |
| Operations | the contract operations with a one-line purpose each, linked to the reference |
| Minimal example | the smallest working manifest and handler, run with `quivr plugin test` |
| Pin it | the `QUIVR_CONFIG` snippet that makes Quivr use it, and how to switch to it at runtime; say which types `make dev` can pin for an author today |
| First-party examples | the plugins in `plugins/` of this type |
