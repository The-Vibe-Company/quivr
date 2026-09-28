package postgres

// eligibleVersionSQL is the canonical eligibility predicate for a Version that
// retrieval, enrichment and Relation expansion may expose or link. Reusing one
// definition keeps the guards from drifting apart.
//
// It requires the aliases v (record_versions) and r (records) to be in scope.
// It does not include the Record's current-Version equality: callers that need
// currentness add "r.current_version_id = v.id" explicitly.
const eligibleVersionSQL = `v.baseline_ready AND NOT v.quarantined AND NOT r.withdrawn AND NOT EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)`

// routedGenerationSQL is the canonical routing predicate: the logical Projection
// Generation serving a Corpus is its installed route, else the default active
// generation. Promotion, enrichment, hydration and rebuild activation all use it,
// and every writer that changes a route holds the Organization journal lock.
func routedGenerationSQL(org, corpusID string) string {
	return `COALESCE((SELECT cr.generation_id FROM corpus_projection_routes cr WHERE cr.organization=` + org + ` AND cr.corpus_id=` + corpusID + `),(SELECT dg.id FROM projection_generations dg WHERE dg.active))`
}
