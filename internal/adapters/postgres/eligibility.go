package postgres

// recordGoneSQL is the absorbing Record fence shared by content reads and
// workers. It requires alias r (records); the Tombstone is Organization-scoped.
const recordGoneSQL = `(r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))`

// eligibleVersionSQL is the canonical eligibility predicate for a Version that
// retrieval, enrichment and Relation expansion may expose or link. Reusing one
// definition keeps the guards from drifting apart.
//
// It requires the aliases v (record_versions) and r (records) to be in scope.
// It does not include the Record's current-Version equality: callers that need
// currentness add "r.current_version_id = v.id" explicitly.
// Separate the two absorbing fences so PostgreSQL can use an anti join for
// Tombstones and parallelize large reads. This is the negation of recordGoneSQL.
const eligibleVersionStateSQL = `v.baseline_ready AND NOT v.quarantined AND NOT r.withdrawn AND NOT `
const eligibleVersionSQL = eligibleVersionStateSQL + `EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)`

// A bounded journal guard must probe the Tombstone key instead of letting
// PostgreSQL pre-hash the whole table as an alternative EXISTS subplan.
// The primary key makes this scalar lookup equivalent; large reads retain
// eligibleVersionSQL's anti-join and parallel-planning opportunities.
const eligibleVersionPointSQL = eligibleVersionStateSQL + `COALESCE((SELECT true FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),false)`

// routedGenerationSQL is the canonical routing predicate: the logical Projection
// Generation serving a Corpus is its installed route, else the default active
// generation. Promotion, enrichment, hydration and rebuild activation all use it,
// and every writer that changes a route holds the Organization journal lock.
func routedGenerationSQL(org, corpusID string) string {
	return `COALESCE((SELECT cr.generation_id FROM corpus_projection_routes cr WHERE cr.organization=` + org + ` AND cr.corpus_id=` + corpusID + `),(SELECT dg.id FROM projection_generations dg WHERE dg.active))`
}

// laterNoticesSQL selects, as two booleans, whether a match.corrected and
// whether a match.no_longer_matches notice exists after the notice aliased n
// (by journal position) for the same Subscription and Record. It only reports
// facts: monitoring.AdmissionReason decides which notice kinds they
// supersede. Delivery admission and Delivery reads share it.
const laterNoticesSQL = `EXISTS(SELECT 1 FROM monitoring_notices later WHERE later.organization=n.organization AND later.subscription_id=n.subscription_id AND later.record_id=n.record_id AND later.kind='match.corrected' AND later.position>n.position),
  EXISTS(SELECT 1 FROM monitoring_notices later WHERE later.organization=n.organization AND later.subscription_id=n.subscription_id AND later.record_id=n.record_id AND later.kind='match.no_longer_matches' AND later.position>n.position)`
