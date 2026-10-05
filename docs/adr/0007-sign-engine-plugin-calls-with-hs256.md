# Sign engine calls to plugins with per-plugin HS256 keys

Date: 2026-10-05

Status: accepted

Plugin endpoints receive credentials, document content and engine-issued Blob grants. An exposed endpoint previously accepted requests without proving they came from the engine. Plugin API 0.14.0 adds authentication to every discovery and Contribution call.

## Decision

Use compact JWS with HS256, following [RFC 7515](https://www.rfc-editor.org/rfc/rfc7515) and [RFC 7518](https://www.rfc-editor.org/rfc/rfc7518). Each plugin receives an independent random shared secret of at least 32 bytes. Keys use the existing environment-based plugin secret mechanism. The engine maps plugin ids to key rings; each SDK receives only its own ring. The registry stores no signing secret and public discovery returns none.

The signed claims bind the plugin audience, Contribution, HTTP method, complete request target and SHA-256 of the exact body bytes. Tokens have integer issuance and expiry times with a lifetime of at most 60 seconds. Both SDKs pin HS256 and the token type, select only a configured key id, compare MACs in constant time and reject invalid tokens before dispatch with HTTP 401 `invalid_engine_token`.

Key rings hold an active signing key and verification keys with optional `not_before` and `not_after` Unix times. Operators distribute a ring containing both keys, switch the engine's active key, and retire the previous key after the overlap. Rolling engine and plugin processes may use different active keys during that overlap. Expired key windows are enforced even when the token has not expired.

## Consequences

HS256 uses standard-library primitives in Go and Python and adds no certificate or crypto-library deployment requirement. Ed25519 would let plugins verify without holding a signing secret; HS256 is chosen for the existing shared-secret deployment model. A compromised plugin can forge calls to itself, so secrets must differ across plugins and the engine's full key map must never be passed to a plugin process.

Tokens authenticate requests, not responses, and provide no confidentiality. Use HTTPS for remote endpoints. An identical token and request can be replayed until expiry; invocation idempotency remains the protection for repeated work. This slice introduces no nonce store.

A manifest restricted to an older API retains unsigned behavior with an engine startup warning. A manifest admitting 0.14.0 requires keys and a compatible SDK; discovery cannot silently downgrade it to an unsigned version. The readiness-only health endpoint stays public for platform probes. The Contract Runner and local dev host provision ephemeral keys for locally launched plugins; remote checks use the engine's configured ring.
