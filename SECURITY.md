# Security policy

## Supported versions

Quivr V2 is at the evaluation stage. Security fixes target the newest
`2.0.0-alpha.N` release; older alpha releases are not maintained. Upgrade to the
newest release and verify its signed image digest before deploying it.

## Report a vulnerability

Use [GitHub private vulnerability reporting](https://github.com/The-Vibe-Company/quivr/security/advisories/new)
to send a report to the maintainers. Do not open a public issue with exploit
details.

Include the affected release and image digest, the component or endpoint,
steps to reproduce, expected impact and any suggested fix. Remove credentials
and private data from examples. Maintainers will coordinate disclosure and a
fix with the reporter; no response-time guarantee is offered during alpha.

## Release inventories and verification

Each image published by the release security workflow has an SPDX JSON inventory
(SBOM) attached to its digest as a signed cosign attestation and uploaded to its
[GitHub release](https://github.com/The-Vibe-Company/quivr/releases).
[Security of Quivr releases](https://docs.quivr.thevibecompany.co/run-quivr/security)
explains how to locate and verify them, what scans run, and their limits.
Older releases may predate these inventories; choose a release with SBOM assets.
