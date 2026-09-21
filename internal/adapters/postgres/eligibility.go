package postgres

// eligibleVersionSQL is the canonical eligibility predicate for a Version that
// retrieval, enrichment and Relation expansion may expose or link. Reusing one
// definition keeps the guards from drifting apart.
//
// It requires the aliases v (record_versions) and r (records) to be in scope.
// It does not include the Record's current-Version equality: callers that need
// currentness add "r.current_version_id = v.id" explicitly.
const eligibleVersionSQL = `v.baseline_ready AND NOT v.quarantined AND NOT r.withdrawn AND NOT EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)`
