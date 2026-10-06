package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// dispatchPrefix writes a bounded prefix of complete trigger events together.
// A partial event or a first oversized event returns paged=true so dispatchStep
// retains its existing Subscription pagination and checkpoint semantics.
func (s EvaluationStore) dispatchPrefix(ctx context.Context, org string, limit int) (processed int, paged bool, err error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	var position int64
	var after string
	if err = tx.QueryRow(ctx, `SELECT position,subscription_after FROM monitoring_checkpoints WHERE organization=$1 FOR UPDATE`, org).Scan(&position, &after); err != nil {
		return 0, false, err
	}
	if after != "" {
		return 0, true, nil
	}
	// The journal head and candidates share a fresh snapshot after the checkpoint
	// lock. Both materialized inputs are bounded before any writes: at most
	// limit events and page+1 targets per event, including the oversize sentinel.
	err = tx.QueryRow(ctx, `WITH head AS MATERIALIZED (
 SELECT last_sequence FROM organization_journals WHERE organization=$1
), events AS MATERIALIZED (
 SELECT e.sequence,e.corpus_id,e.resource_id,coalesce(e.record_version_id,'') AS version_id,e.event_type,e.trace_context
 FROM change_events e CROSS JOIN head h
 WHERE e.organization=$1 AND e.sequence>$2 AND e.sequence<=h.last_sequence
 AND e.event_type IN ('record.retrieval_ready','record.enrichment_available','record.withdrawn')
 ORDER BY e.sequence LIMIT $3
), targets AS MATERIALIZED (
 SELECT e.sequence,e.corpus_id,e.resource_id,t.subscription_id,t.subscription_version_id,t.record_version_id,t.kind,e.trace_context
 FROM events e CROSS JOIN LATERAL (
  (SELECT s.id AS subscription_id,v.id AS subscription_version_id,e.version_id AS record_version_id,'evaluation'::text AS kind
  FROM subscription_corpora sc
  JOIN subscriptions s ON (s.organization,s.id)=(sc.organization,sc.subscription_id)
  JOIN LATERAL (SELECT v.id,v.saved_query_version_id FROM subscription_versions v
   WHERE v.organization=s.organization AND v.subscription_id=s.id AND v.activation_position<e.sequence
   ORDER BY v.activation_position DESC LIMIT 1) v ON true
  JOIN saved_query_versions q ON (q.organization,q.id)=(s.organization,v.saved_query_version_id)
  WHERE e.event_type<>'record.withdrawn' AND e.version_id<>''
  AND sc.organization=$1 AND sc.corpus_id=e.corpus_id AND s.enabled AND e.corpus_id=ANY(q.corpus_ids)
  AND coalesce(s.enabled_position,0)<e.sequence
  AND NOT (e.event_type='record.enrichment_available' AND EXISTS(
   SELECT 1 FROM evaluation_intents d WHERE d.organization=$1 AND d.subscription_version_id=v.id AND d.record_version_id=e.version_id
   AND d.kind='evaluation' AND d.state='done' AND d.outcome IN `+decidedOutcomes+`))
  ORDER BY s.id LIMIT ($4::int+1))
  UNION ALL
  (SELECT s.id,s.current_version_id,latest.record_version_id,'withdrawal'::text
  FROM (SELECT DISTINCT subscription_id FROM matches
   WHERE organization=$1 AND record_id=e.resource_id AND position<e.sequence) alerted
  JOIN subscriptions s ON s.organization=$1 AND s.id=alerted.subscription_id
  JOIN LATERAL (SELECT m.record_version_id FROM matches m
   WHERE m.organization=$1 AND m.record_id=e.resource_id AND m.subscription_id=s.id
   ORDER BY m.position DESC LIMIT 1) latest ON true
  WHERE e.event_type='record.withdrawn'
  ORDER BY s.id LIMIT ($4::int+1))
 ) t
), sizes AS (
 SELECT e.sequence,count(t.subscription_id) AS targets FROM events e
 LEFT JOIN targets t ON t.sequence=e.sequence GROUP BY e.sequence
), oversized AS (
 SELECT min(sequence) AS first_sequence FROM sizes WHERE targets>$4
), selected AS MATERIALIZED (
 SELECT e.sequence FROM events e CROSS JOIN oversized o
 WHERE o.first_sequence IS NULL OR e.sequence<o.first_sequence
), intents AS (
 INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,kind,trace_context)
 SELECT $1,t.subscription_version_id,t.sequence,t.subscription_id,t.corpus_id,t.resource_id,t.record_version_id,t.kind,t.trace_context
 FROM targets t JOIN selected s ON s.sequence=t.sequence
 ORDER BY t.sequence,t.subscription_id ON CONFLICT DO NOTHING
), progress AS (
 SELECT count(*)::int AS processed,max(sequence) AS last_sequence,(SELECT count(*) FROM events) AS considered,
 (SELECT first_sequence FROM oversized) AS oversized FROM selected
), checkpoint AS (
 UPDATE monitoring_checkpoints c SET position=CASE
  WHEN p.processed>0 AND (p.considered=$3 OR p.oversized IS NOT NULL) THEN p.last_sequence
  ELSE h.last_sequence END,subscription_after=''
 FROM progress p CROSS JOIN head h WHERE c.organization=$1
 AND (p.processed>0 OR (p.considered=0 AND $2<h.last_sequence))
)
SELECT CASE WHEN p.processed>0 THEN p.processed WHEN p.considered=0 AND $2<h.last_sequence THEN 1 ELSE 0 END,
 p.considered>0 AND p.processed=0 FROM progress p CROSS JOIN head h`, org, position, limit, s.page()).Scan(&processed, &paged)
	if err != nil {
		return 0, false, err
	}
	if paged {
		return 0, true, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return processed, false, nil
}
