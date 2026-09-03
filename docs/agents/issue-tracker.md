# Issue tracker: Linear

Specs, decision maps, and implementation tickets for this repository live in Linear. Use the configured Linear integration for all tracker operations.

## Location

- **Workspace:** The Vibe Company (`thevibecompany`)
- **Quivr V2 parent investigation:** [`THE-531`](https://linear.app/thevibecompany/issue/THE-531/investigation-et-planification-profonde-technique-du-projet)
- **Repository:** [`The-Vibe-Company/quivr-v2`](https://github.com/The-Vibe-Company/quivr-v2)

New Quivr V2 specs and tickets must be created as sub-issues of `THE-531` when the hierarchy fits. Otherwise, relate them explicitly to `THE-531` and keep them in the same Linear project as the parent issue.

## Conventions

- **Create an issue:** create it in The Vibe Company workspace, associate it with the same project as `THE-531`, and attach it to `THE-531` as a sub-issue or explicit relation.
- **Read an issue:** fetch its full description, labels, relations, sub-issues, and comments before acting.
- **Comment on an issue:** add the result as a Linear comment; do not replace useful historical context in the description.
- **Apply or remove labels:** use the mapping in `docs/agents/triage-labels.md`.
- **Express blocking:** use Linear's native blocking and blocked-by relations. Text in the issue body may summarize dependencies, but it is not the source of truth.
- **Close work:** move only the issue being completed to the team's completed state. Do not close or modify its parent automatically.
- **Reference code:** include the repository URL and, once available, the pull request or commit URL.

## When a skill says "publish to the issue tracker"

Create a Linear issue following the conventions above. A spec becomes its own issue under or related to `THE-531`; implementation tickets become sub-issues of that spec when appropriate and remain traceable to `THE-531`.

Apply the `ready-for-agent` label only when the output is fully specified and can be picked up in a fresh agent session.

## When a skill says "fetch the relevant ticket"

Resolve the supplied Linear identifier or URL, then read the complete issue, comments, parent, sub-issues, labels, and blocking relations.

## Ticket decomposition

`/to-tickets` publishes tickets in dependency order, blockers first. Every ticket must:

- deliver a narrow, verifiable vertical slice;
- name its parent spec and retain a relation to `THE-531`;
- use native blocked-by relations for genuine prerequisites;
- receive `ready-for-agent` only after the breakdown is approved.

Do not close or rewrite the source spec or `THE-531` while publishing its tickets.

## Wayfinding operations

Used by `/wayfinder` if the project enters another genuinely foggy decision phase.

- **Map:** one Linear issue related to `THE-531`, labelled `wayfinder:map`, containing Notes, Decisions so far, and Fog.
- **Decision ticket:** a sub-issue of the map, labelled by type (`wayfinder:research`, `wayfinder:prototype`, `wayfinder:grilling`, or `wayfinder:task`).
- **Blocking:** Linear's native blocked-by relation is canonical.
- **Frontier:** open, unassigned child issues whose blockers are all completed, in map order.
- **Claim:** assign the frontier issue to the agent or driving developer before starting work.
- **Resolve:** record the decision in a comment, complete the ticket, and add a concise result with a link under Decisions so far in the map.

The map and its parent `THE-531` remain open until a human explicitly decides otherwise.
