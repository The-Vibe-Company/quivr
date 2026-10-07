package postgres

import (
	"context"
	"errors"
	"strings"
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

var (
	_ workqueue.Tracker = QueueTracker{}
	_ workqueue.Tracker = Store{}
)

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
// releases it with the latest fencing token. If another worker takes the row,
// run's context is canceled and ErrLeaseLost is returned alongside its error.
func (t QueueTracker) Track(ctx context.Context, org, kind, workID, documentID string, run func(context.Context) error) error {
	if run == nil {
		return errors.New("workqueue: nil tracked function")
	}
	if err := t.validate(org, kind, workID, documentID); err != nil {
		return err
	}
	canonicalDocumentID, err := t.canonicalDocumentID(ctx, kind, org, workID, documentID)
	if err != nil {
		return err
	}
	lease := t.leaseDuration()
	token, err := t.claim(ctx, org, kind, workID, canonicalDocumentID, lease)
	if err != nil {
		return err
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewed := make(chan error, 1)
	go func() {
		interval := lease / 3
		if interval < 10*time.Millisecond {
			interval = 10 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				renewed <- nil
				return
			case <-ticker.C:
				token, err = t.renew(workCtx, org, kind, workID, canonicalDocumentID, token, lease)
				if err != nil {
					if workCtx.Err() != nil {
						renewed <- nil
					} else {
						cancel()
						renewed <- err
					}
					return
				}
			}
		}
	}()

	runErr := run(workCtx)
	cancel()
	renewErr := <-renewed

	cleanup := context.Background()
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 0 {
		var releaseCancel context.CancelFunc
		cleanup, releaseCancel = context.WithTimeout(context.Background(), min(time.Until(deadline), lease))
		defer releaseCancel()
	} else {
		var releaseCancel context.CancelFunc
		cleanup, releaseCancel = context.WithTimeout(context.Background(), lease)
		defer releaseCancel()
	}
	releaseErr := t.release(cleanup, org, kind, workID, canonicalDocumentID, token)
	return errors.Join(runErr, renewErr, releaseErr)
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
WHERE queue_document_attempts.lease_until<=clock_timestamp()
RETURNING token`, org, kind, workID, documentID, lease.Seconds()).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, workqueue.ErrLeaseHeld
	}
	return token, err
}

func (t QueueTracker) renew(ctx context.Context, org, kind, workID, documentID string, token int64, lease time.Duration) (int64, error) {
	var next int64
	err := t.Pool.QueryRow(ctx, `UPDATE queue_document_attempts
SET lease_until=clock_timestamp()+make_interval(secs => $6::double precision)
WHERE organization=$1 AND kind=$2 AND work_id=$3 AND document_id=$4 AND token=$5 AND lease_until>clock_timestamp()
RETURNING token`, org, kind, workID, documentID, token, lease.Seconds()).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return token, workqueue.ErrLeaseLost
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
		return workqueue.ErrLeaseLost
	}
	return nil
}

// Track lets a Store be attached directly with workqueue.WithTracker. This is
// useful for applications that already share one PostgreSQL Store value.
func (s Store) Track(ctx context.Context, org, kind, workID, documentID string, run func(context.Context) error) error {
	return (QueueTracker{Pool: s.Pool}).Track(ctx, org, kind, workID, documentID, run)
}

func backlogRebuildPredicate() string {
	return strings.NewReplacer(
		"$3", "o.target_generation_id",
		"$2", "o.corpus_id",
		"$1", "o.organization",
	).Replace(rebuildGapSQL)
}

func backlogBackfillScope() string {
	return strings.NewReplacer(
		"$11", "b.checkpoint",
		"$10", "false",
		"$9", "b.plan_id",
		"$8", "b.registration_id",
		"$7", "b.spaces",
		"$6", "g.space_id",
		"$5", "b.accepted_before",
		"$4", "b.accepted_after",
		"$3", "o.target_generation_id",
		"$2", "o.corpus_id",
		"$1", "o.organization",
	).Replace(backfillScopeSQL("AND v.id>$11"))
}

func queueBacklogSQL() string {
	// The empty queue value is the rolling-upgrade representation of live work.
	// Operations have no source queue because administrative work is always
	// bulk. Version class comes from its receipt rather than a Version column.
	return `WITH work AS (
 SELECT CASE WHEN COALESCE(NULLIF(rc.work_queue,''),'live')='bulk' THEN 'bulk' ELSE 'live' END AS queue,
        rc.organization,COALESCE(NULLIF(rc.version_id,''),ar.version_id,rc.id) AS document_id,
        COALESCE(LEAST(rc.accepted_at,ar.accepted_at),rc.accepted_at,ar.accepted_at) AS admitted_at,false AS active
 FROM ingestion_receipts rc
 JOIN records r ON (r.organization,r.id)=(rc.organization,rc.record_id)
 LEFT JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(rc.organization,rc.record_id,rc.slot)
 WHERE rc.route_family='ingestion' AND rc.state='pending'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT CASE WHEN COALESCE(NULLIF(rc.work_queue,''),'live')='bulk' THEN 'bulk' ELSE 'live' END,
        v.organization,v.id,COALESCE(LEAST(rc.accepted_at,ar.accepted_at),rc.accepted_at,ar.accepted_at),false
 FROM record_versions v
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
 WHERE NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
   AND ((NOT v.baseline_ready AND NOT v.quarantined AND v.processing IN ('queued','running','retrying'))
     OR (v.baseline_ready AND r.current_version_id=v.id AND NOT v.quarantined AND v.enrichment_state IN ('queued','running','retrying')))
 UNION ALL
 SELECT CASE WHEN COALESCE(NULLIF(e.work_queue,''),NULLIF(rc.work_queue,''),'live')='bulk' THEN 'bulk' ELSE 'live' END,
        e.organization,e.version_id,e.created_at,false
 FROM ingestion_evaluations e
 JOIN record_versions v ON (v.organization,v.id)=(e.organization,e.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
 WHERE e.state='queued'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT CASE WHEN COALESCE(NULLIF(sp.work_queue,''),NULLIF(rc.work_queue,''),'live')='bulk' THEN 'bulk' ELSE 'live' END,
        sp.organization,sp.version_id,sp.created_at,false
 FROM serving_projections sp
 JOIN record_versions v ON (v.organization,v.id)=(sp.organization,sp.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
 WHERE sp.state='queued'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT 'live',
        i.organization,i.record_version_id,i.available_at,false
 FROM evaluation_intents i
 JOIN record_versions v ON (v.organization,v.id)=(i.organization,i.record_version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
 WHERE i.kind='evaluation' AND i.state='pending' AND i.available_at<=statement_timestamp()
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT 'bulk',o.organization,v.id,o.created_at,false
 FROM operations o
 JOIN records r ON (r.organization,r.corpus_id)=(o.organization,o.corpus_id)
 JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)
 WHERE o.kind IN ('projection_rebuild','retrieval_configuration')
   AND o.state IN ('queued','running')
   AND ` + backlogRebuildPredicate() + `
 UNION ALL
 SELECT 'bulk',o.organization,candidate.version_id,o.created_at,false
 FROM operations o
 JOIN backfills b ON (b.organization,b.operation_id)=(o.organization,o.id)
 JOIN projection_generations g ON g.id=o.target_generation_id
 JOIN LATERAL (
   SELECT scope.version_id
   FROM ` + backlogBackfillScope() + ` scope
   WHERE scope.version_id>b.checkpoint
 ) candidate ON true
 LEFT JOIN record_versions v ON (v.organization,v.id)=(o.organization,candidate.version_id)
 LEFT JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 WHERE o.kind='backfill' AND o.state IN ('queued','running')
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
          WHEN COALESCE(NULLIF(rc.work_queue,''),NULLIF(ie.work_queue,''),NULLIF(sp.work_queue,''),'live')='bulk' THEN 'bulk'
          ELSE 'live'
        END AS queue,
        a.organization,a.document_id,clock_timestamp() AS admitted_at,true AS active
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
 FROM (SELECT * FROM work UNION ALL SELECT * FROM active_attempts) all_work
 WHERE document_id<>''
 GROUP BY queue,organization,document_id
), queues(queue) AS (VALUES ('live'::text),('bulk'::text))
SELECT q.queue,
       count(d.document_id) FILTER (WHERE NOT COALESCE(d.active,false))::bigint,
       count(d.document_id) FILTER (WHERE COALESCE(d.active,false))::bigint,
       GREATEST(0,COALESCE(MAX(EXTRACT(EPOCH FROM statement_timestamp()-d.admitted_at))
                    FILTER (WHERE NOT COALESCE(d.active,false)),0))::double precision
FROM queues q
LEFT JOIN documents d ON d.queue=q.queue
GROUP BY q.queue
ORDER BY CASE q.queue WHEN 'live' THEN 0 ELSE 1 END`
}
