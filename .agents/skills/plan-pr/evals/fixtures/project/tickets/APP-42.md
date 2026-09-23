# APP-42 Add CSV export
Parent APP-10. Acceptance: only admins may export their own tenant's records; non-admins receive 403; no cross-tenant rows.
Children: APP-43 implements CSV formatting; APP-44 enables member exports.
Dependencies: APP-9 requires audit event export.requested without personal data.
Comment (approved): use synchronous response; large background exports out of scope.
