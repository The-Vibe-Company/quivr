package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultQueueAttemptLease = 15 * time.Second
	maxQueueAttemptLease     = 5 * time.Minute
)

// QueueTracker owns the durable document-attempt lease used by queue
// observations. A zero Lease uses the bounded default; larger values are
// capped so a dead worker cannot hide active work for an unbounded interval.
type QueueTracker struct {
	Pool  *pgxpool.Pool
	Lease time.Duration
}

var _ workqueue.Tracker = QueueTracker{}

// errLeaseLost reports that another attempt took the row's fencing token.
var errLeaseLost = errors.New("workqueue: lease lost")

func (t QueueTracker) leaseDuration() time.Duration {
	lease := t.Lease
	if lease <= 0 {
		lease = defaultQueueAttemptLease
	}
	if lease > maxQueueAttemptLease {
		lease = maxQueueAttemptLease
	}
	return lease
}

func (t QueueTracker) validate(org, kind, workID, documentID string) error {
	if t.Pool == nil {
		return errors.New("workqueue: nil postgres pool")
	}
	for name, value := range map[string]string{
		"organization": org,
		"kind":         kind,
		"work_id":      workID,
		"document_id":  documentID,
	} {
		if value == "" {
			return errors.New("workqueue: empty " + name)
		}
	}
	return nil
}

// Track claims one document attempt, renews its lease while run executes and
// releases it with the latest fencing token. The row is an observation only:
// run receives ctx unchanged and Track returns run's result. A failed lookup,
// claim, renewal or release is logged and leaves the row to expire. A retry
// takes the row over; Temporal and the batch and operation leases, not this
// row, keep two workers off the same document.
func (t QueueTracker) Track(ctx context.Context, org, kind, workID, documentID string, run func(context.Context) error) error {
	if run == nil {
		return errors.New("workqueue: nil tracked function")
	}
	if err := t.validate(org, kind, workID, documentID); err != nil {
		return err
	}
	failed := func(step string, err error) {
		slog.WarnContext(ctx, "work tracking failed; the work continues", "event", "quivr.workqueue.tracking_failed",
			"step", step, "kind", kind, "work_id", workID, "error", err.Error())
	}
	documentID, err := t.canonicalDocumentID(ctx, kind, org, workID, documentID)
	if err != nil {
		failed("lookup", err)
		return run(ctx)
	}
	lease := t.leaseDuration()
	token, err := t.claim(ctx, org, kind, workID, documentID, lease)
	if err != nil {
		failed("claim", err)
		return run(ctx)
	}

	renewal, stopRenewal := context.WithCancel(ctx)
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(max(lease/3, 10*time.Millisecond))
		defer ticker.Stop()
		for {
			select {
			case <-renewal.Done():
				return
			case <-ticker.C:
			}
			next, err := t.renew(renewal, org, kind, workID, documentID, token, lease)
			switch {
			case renewal.Err() != nil:
				return
			case err != nil:
				failed("renew", err)
				if errors.Is(err, errLeaseLost) {
					return
				}
			default:
				token = next
			}
		}
	}()
	defer func() {
		stopRenewal()
		<-renewed
		timeout := lease
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 0 {
			timeout = min(timeout, time.Until(deadline))
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		if err := t.release(cleanup, org, kind, workID, documentID, token); err != nil {
			failed("release", err)
		}
	}()
	return run(ctx)
}

// canonicalDocumentID maps an ingestion receipt attempt to the Version it
// reserved. Acceptance creates that reservation before publication, so a
// receipt lease and the later Version/evaluation/projection rows collapse to
// one document in the backlog snapshot. Other work kinds already pass their
// Version ID directly.
func (t QueueTracker) canonicalDocumentID(ctx context.Context, kind, org, workID, documentID string) (string, error) {
	if kind != "ingestion" {
		return documentID, nil
	}
	var canonical string
	err := t.Pool.QueryRow(ctx, `SELECT COALESCE(NULLIF(rc.version_id,''),ar.version_id,rc.id)
FROM ingestion_receipts rc
LEFT JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(rc.organization,rc.record_id,rc.slot)
WHERE rc.organization=$1 AND rc.id=$2 AND rc.route_family='ingestion'`, org, workID).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		// Keep the generic tracker useful for a caller that claims a work item
		// before its source row is visible. Once the receipt exists, the
		// reservation above is the canonical identity.
		return documentID, nil
	}
	return canonical, err
}

func (t QueueTracker) claim(ctx context.Context, org, kind, workID, documentID string, lease time.Duration) (int64, error) {
	var token int64
	err := t.Pool.QueryRow(ctx, `INSERT INTO queue_document_attempts(organization,kind,work_id,document_id,token,lease_until)
VALUES($1,$2,$3,$4,nextval('queue_document_attempt_tokens'),clock_timestamp()+make_interval(secs => $5::double precision))
ON CONFLICT (organization,kind,work_id,document_id) DO UPDATE
SET token=EXCLUDED.token,
    lease_until=clock_timestamp()+make_interval(secs => $5::double precision)
RETURNING token`, org, kind, workID, documentID, lease.Seconds()).Scan(&token)
	return token, err
}

func (t QueueTracker) renew(ctx context.Context, org, kind, workID, documentID string, token int64, lease time.Duration) (int64, error) {
	var next int64
	err := t.Pool.QueryRow(ctx, `UPDATE queue_document_attempts
SET lease_until=clock_timestamp()+make_interval(secs => $6::double precision)
WHERE organization=$1 AND kind=$2 AND work_id=$3 AND document_id=$4 AND token=$5
RETURNING token`, org, kind, workID, documentID, token, lease.Seconds()).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return token, errLeaseLost
	}
	return next, err
}

func (t QueueTracker) release(ctx context.Context, org, kind, workID, documentID string, token int64) error {
	tag, err := t.Pool.Exec(ctx, `DELETE FROM queue_document_attempts
WHERE organization=$1 AND kind=$2 AND work_id=$3 AND document_id=$4 AND token=$5`, org, kind, workID, documentID, token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errLeaseLost
	}
	return nil
}

func queueBacklogSQL() string {
	// Operations have no source queue because administrative work is always
	// bulk. Version class comes from its receipt rather than a Version column.
	// Rebuild/backfill scope is estimated from durable operation counters; overlap
	// with other operations/stages is intentionally not deduplicated.
	return `WITH enrichment_versions AS MATERIALIZED (
 SELECT organization,version_id FROM queue_enrichment_records WHERE pending
), work AS (
 SELECT CASE WHEN rc.work_queue='bulk' THEN 'bulk' ELSE 'live' END AS queue,
        rc.organization,COALESCE(NULLIF(rc.version_id,''),ar.version_id,rc.id) AS document_id,
        COALESCE(LEAST(rc.accepted_at,ar.accepted_at),rc.accepted_at,ar.accepted_at) AS admitted_at,false AS active
 FROM ingestion_receipts rc
 JOIN records r ON (r.organization,r.id)=(rc.organization,rc.record_id)
 LEFT JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(rc.organization,rc.record_id,rc.slot)
 WHERE rc.route_family='ingestion' AND rc.state='pending'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT CASE WHEN rc.work_queue='bulk' THEN 'bulk' ELSE 'live' END,
        v.organization,v.id,COALESCE(LEAST(rc.accepted_at,ar.accepted_at),rc.accepted_at,ar.accepted_at),false
 FROM record_versions v
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
 WHERE NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
   AND NOT v.baseline_ready AND NOT v.quarantined AND v.processing IN ('queued','running','retrying')
 UNION ALL
 SELECT CASE WHEN rc.work_queue='bulk' THEN 'bulk' ELSE 'live' END,
        v.organization,v.id,COALESCE(LEAST(rc.accepted_at,ar.accepted_at),rc.accepted_at,ar.accepted_at),false
 FROM enrichment_versions ev
 JOIN record_versions v ON (v.organization,v.id)=(ev.organization,ev.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
 WHERE v.baseline_ready AND r.current_version_id=v.id AND NOT v.quarantined
   AND v.enrichment_state IN ('queued','running','retrying')
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT CASE WHEN e.work_queue='bulk' THEN 'bulk' ELSE 'live' END,
        e.organization,e.version_id,e.created_at,false
 FROM ingestion_evaluations e
 JOIN record_versions v ON (v.organization,v.id)=(e.organization,e.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE e.state='queued'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT CASE WHEN sp.work_queue='bulk' THEN 'bulk' ELSE 'live' END,
        sp.organization,sp.version_id,sp.created_at,false
 FROM serving_projections sp
 JOIN record_versions v ON (v.organization,v.id)=(sp.organization,sp.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE sp.state='queued'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT 'live',
        i.organization,i.record_version_id,i.available_at,false
 FROM evaluation_intents i
 JOIN record_versions v ON (v.organization,v.id)=(i.organization,i.record_version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE i.kind='evaluation' AND i.state='pending' AND i.available_at<=statement_timestamp()
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT 'bulk',o.organization,qi.version_id,o.created_at,false
 FROM operations o
 JOIN quarantine_reprocess_items qi ON (qi.organization,qi.operation_id)=(o.organization,o.id)
 JOIN records r ON (r.organization,r.id)=(qi.organization,qi.record_id)
 WHERE o.kind='quarantine_reprocess' AND o.state IN ('queued','running')
   AND qi.phase IN ('pending','renormalizing','released')
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
), active_attempts AS (
 SELECT CASE
          WHEN a.kind='alert' THEN 'live'
          WHEN a.kind IN ('bulk','rebuild','retrieval_configuration','backfill','quarantine_reprocess','operation')
            OR COALESCE(op.kind,'') IN ('projection_rebuild','retrieval_configuration','backfill','quarantine_reprocess') THEN 'bulk'
          WHEN COALESCE(rc.work_queue,ie.work_queue,sp.work_queue)='bulk' THEN 'bulk'
          ELSE 'live'
        END AS queue,
        a.organization,a.document_id,clock_timestamp() AS admitted_at,true AS active,op.id AS operation_id
 FROM queue_document_attempts a
 LEFT JOIN ingestion_receipts rc ON a.organization=rc.organization AND a.work_id=rc.id
 LEFT JOIN ingestion_evaluations ie ON a.organization=ie.organization AND a.work_id=ie.id
 LEFT JOIN serving_projections sp ON a.organization=sp.organization AND a.work_id=sp.id
 LEFT JOIN operations op ON a.organization=op.organization AND a.work_id=op.id
 LEFT JOIN record_versions av ON (av.organization,av.id)=(a.organization,a.document_id)
 LEFT JOIN accepted_revisions aa ON (aa.organization,aa.version_id)=(a.organization,a.document_id)
 LEFT JOIN records dr ON dr.organization=a.organization AND dr.id=COALESCE(av.record_id,aa.record_id,rc.record_id,ie.record_id,sp.record_id)
 WHERE a.lease_until>clock_timestamp()
   AND (a.kind<>'ingestion' OR rc.id IS NULL OR rc.state='pending'
        OR av.id IS NULL
        OR (NOT av.baseline_ready AND av.processing IN ('queued','running','retrying'))
        OR (av.baseline_ready AND av.enrichment_state IN ('queued','running','retrying')))
   AND (a.kind<>'evaluation' OR ie.id IS NULL OR ie.state='queued')
   AND (a.kind NOT IN ('serving','serving_projection') OR sp.id IS NULL OR sp.state='queued')
   AND (a.kind NOT IN ('operation','rebuild','retrieval_configuration','backfill','quarantine_reprocess') OR op.id IS NULL OR op.state IN ('queued','running'))
   AND (a.kind<>'alert' OR EXISTS(SELECT 1 FROM evaluation_intents ai WHERE ai.organization=a.organization AND ai.record_version_id=a.document_id AND ai.kind='evaluation' AND ai.state='pending' AND ai.available_at<=statement_timestamp()))
   AND (dr.id IS NULL OR (NOT dr.withdrawn AND NOT EXISTS(SELECT 1 FROM tombstones dt WHERE dt.organization=dr.organization AND dt.record_id=dr.id)))
), documents AS (
 SELECT queue,organization,document_id,min(admitted_at) AS admitted_at,bool_or(active) AS active
 FROM (SELECT * FROM work UNION ALL SELECT queue,organization,document_id,admitted_at,active FROM active_attempts) all_work
 WHERE document_id<>''
 GROUP BY queue,organization,document_id
), operation_work AS (
 SELECT o.created_at,
 GREATEST(0,COALESCE((o.counters->>'versions_in_scope')::bigint,(b.estimate->>'versions')::bigint,0)
   - CASE WHEN o.kind='backfill' THEN COALESCE((o.counters->>'versions_done')::bigint,0)+COALESCE((o.counters->>'versions_skipped')::bigint,0)
          ELSE COALESCE((o.counters->>'versions_covered')::bigint,0)+COALESCE((o.counters->>'versions_quarantined')::bigint,0) END
   - COALESCE(a.active,0)) AS waiting
 FROM operations o
 LEFT JOIN backfills b ON (b.organization,b.operation_id)=(o.organization,o.id)
 LEFT JOIN (
  SELECT organization,operation_id,count(DISTINCT document_id) AS active FROM active_attempts
  WHERE queue='bulk' AND operation_id IS NOT NULL GROUP BY organization,operation_id
 ) a ON (a.organization,a.operation_id)=(o.organization,o.id)
 WHERE o.kind IN ('projection_rebuild','retrieval_configuration','backfill') AND o.state IN ('queued','running')
), operation_totals AS (
 SELECT COALESCE(sum(waiting),0)::bigint AS waiting,min(created_at) FILTER (WHERE waiting>0) AS oldest
 FROM operation_work
), queues(queue) AS (VALUES ('live'::text),('bulk'::text))
SELECT q.queue,
       (count(d.document_id) FILTER (WHERE NOT COALESCE(d.active,false))
        + CASE WHEN q.queue='bulk' THEN ot.waiting ELSE 0 END)::bigint,
       count(d.document_id) FILTER (WHERE COALESCE(d.active,false))::bigint,
       GREATEST(0,CASE WHEN q.queue='bulk' THEN COALESCE(EXTRACT(EPOCH FROM statement_timestamp()-ot.oldest),0) ELSE 0 END,
                COALESCE(MAX(EXTRACT(EPOCH FROM statement_timestamp()-d.admitted_at))
                    FILTER (WHERE NOT COALESCE(d.active,false)),0))::double precision
FROM queues q CROSS JOIN operation_totals ot
LEFT JOIN documents d ON d.queue=q.queue
GROUP BY q.queue,ot.waiting,ot.oldest
ORDER BY CASE q.queue WHEN 'live' THEN 0 ELSE 1 END`
}
