# Admin export
Status: approved. User explicitly approved this revision and implementation; local only, do not push.
Original request: add admin-only CSV export for tenant records.
Acceptance: unauthenticated rejected; members403; tenant isolation; correct CSV escaping.
Approved test boundary: exported exportRows and public CSV response.
Completed: tenant isolation test and implementation (src/export.js). Remaining: admin check and CSV formatting.
Validation guide: docs/agents/plan-pr-validation.md
Ship PR handoff after remaining steps, local-only.
