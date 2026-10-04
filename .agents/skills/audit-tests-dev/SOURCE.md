# Source and modifications

Upstream: https://github.com/openclaw/openclaw/tree/main/.agents/skills/test-audit
Pinned commit: `80930af448ebabc84174146b56bc106d37fab3b4`
Upstream SKILL.md and CAMPAIGN.md were fetched from that commit before adaptation.

License: MIT; upstream attribution (Copyright (c) 2026 OpenClaw Foundation) is retained in LICENSE.

## Modifications

- Renamed `test-audit` to `audit-tests-dev` under The Vibe Company naming policy.
- Made the skill portable: replaced OpenClaw-specific tools and paths (Vitest runner script, changed-gate script, crabbox, autoreview, PR maintainer flow, `src/`/`packages/`/`extensions/` lanes) with the repository's own test, verification, review and pull-request steps.
- Generalised the Telegram campaign examples in CAMPAIGN.md into neutral lessons.
- Added portable Skillpack metadata.
