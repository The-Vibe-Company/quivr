# Plugin CLI and Contract Runner live in the quivr binary and reuse engine validation

Status: accepted

Plugin authors get one CLI, `quivr plugin init | dev | test | inspect`, shipped in the same Go `quivr` binary as the engine, rather than in a Python (or other language) SDK. The Plugin Contract Runner (`quivr plugin test`) and `inspect` call the engine's own validation code, such as the manifest checks in `internal/plugins` and the structural Manifest rules in `content.CheckManifest`. So "passes the runner" means "the engine accepts it". The runner tests only the public Plugin Protocol, so it applies unchanged to a future TypeScript SDK. Authors need the `quivr` binary but no running stack.

## Considered Options

- **Runner inside the Python SDK.** It is closer to authors, but it would re-implement Manifest and extension validation in a second language and drift from what the engine accepts. Every future SDK would repeat that work.
- **Standalone runner binary.** It adds a second artifact to version and release, and still needs the engine's validation code.

## Consequences

- Validation rules the runner uses must stay exported from engine packages and free of infrastructure dependencies (database, Temporal, object storage).
- Normative fixtures in `contracts/plugins/v0/fixtures/` stay independent of any SDK. They bind every implementation language.
