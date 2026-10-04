# Upstream attribution and local changes

Source: https://github.com/alibaba/open-code-review/blob/v1.12.1/skills/open-code-review-delegate/SKILL.md
Release: v1.12.1
Commit: 1f5caf4d5b7d5324c6e4c836c971136e4010192e
Author: Alibaba / open-code-review contributors
License: Apache-2.0 (see LICENSE)

The upstream skill body is retained verbatim. Local modifications:
- Frontmatter name changed to review-code-dev to retain the existing package identity.
- Distribution setup section added before the upstream workflow: portable checksum-pinned installation and caller handoff compatibility.
- Local Python bootstrap/test files and Skillpack metadata included.

No Alibaba affiliation or endorsement is implied by this repackaging. Package version is independent of the upstream CLI and skill metadata versions.

## Armada distribution changes

The upstream Alibaba workflow and Apache-2.0 LICENSE are retained.
Local bootstrap, tests and companion metadata from the source package are
MIT-licensed, Copyright (c) 2026 The Vibe Company (see LICENSE-MIT).
Imported package version: 2.1.0.

Armada adds a read-only `check` command to scripts/ocr.py, its offline tests,
and setup notes to SKILL.md. It checks the platform, cache permissions and
cached binary checksum without downloading or executing OCR.
