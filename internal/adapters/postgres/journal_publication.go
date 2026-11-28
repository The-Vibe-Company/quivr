package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const journalModeLock int64 = 642006

const parallelJournalSQL = `COALESCE((SELECT parallel FROM change_journal_publication WHERE singleton),false)`

// ErrJournalUnpublished leaves the canonical work pending. It must be retried
// after the publisher proves the boundary, never recorded as a final refusal.
var ErrJournalUnpublished = errors.New("change journal publication pending")

// journalHeadSQL covers a complete prefix, including aborted allocation gaps.
// Parallel writers never update the legacy organization head or this watermark.
func journalHeadSQL(org string) string {
	return `CASE WHEN ` + parallelJournalSQL + ` THEN
 (SELECT published_through FROM change_journal_publication WHERE singleton)
 ELSE COALESCE((SELECT last_sequence FROM organization_journals WHERE organization=` + org + `),0) END`
}

// JournalStore publishes sequence barriers only after every transaction which
// could own a lower number has finished. It is independent of canonical writes.
type JournalStore struct{ Pool *pgxpool.Pool }

// JournalStatus exposes publication progress without exposing transaction IDs.
type JournalStatus struct {
	Parallel             bool    `json:"parallel"`
	Epoch                int64   `json:"epoch"`
	PublishedThrough     int64   `json:"published_through"`
	AllocatedThrough     int64   `json:"allocated_through"`
	UnpublishedPositions int64   `json:"unpublished_positions"`
	PendingAgeSeconds    float64 `json:"pending_age_seconds"`
}

func (s JournalStore) Status(ctx context.Context) (JournalStatus, error) {
	var status JournalStatus
	err := s.Pool.QueryRow(ctx, `SELECT parallel,epoch,published_through,
 (SELECT CASE WHEN is_called THEN last_value ELSE last_value-1 END FROM change_event_positions),
 CASE WHEN sample_xid IS NULL THEN 0 ELSE GREATEST(0,EXTRACT(EPOCH FROM clock_timestamp()-sampled_at))::double precision END
 FROM change_journal_publication WHERE singleton`).Scan(&status.Parallel, &status.Epoch, &status.PublishedThrough, &status.AllocatedThrough, &status.PendingAgeSeconds)
	status.UnpublishedPositions = max(0, status.AllocatedThrough-status.PublishedThrough)
	return status, err
}

// Initialize adds the bridge's mode row without changing any installation mode.
func (s JournalStore) Initialize(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO change_journal_publication(singleton) VALUES(true) ON CONFLICT DO NOTHING`)
	return err
}

// SetParallel is an explicit deployment-wide cutover. The operator must first
// retire pre-bridge binaries: they do not participate in this writer fence.
// Queue maintenance is acquired first, then the exclusive mode fence drains
// bridge writers. No allocator reset can overlap a writer or sampler.
func (s JournalStore) SetParallel(ctx context.Context, parallel bool) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, queueMaintenanceLock); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, journalModeLock); err != nil {
		return err
	}
	var current bool
	if err = tx.QueryRow(ctx, `SELECT parallel FROM change_journal_publication WHERE singleton FOR UPDATE`).Scan(&current); err != nil {
		return err
	}
	if current == parallel {
		return tx.Commit(ctx)
	}
	var initialized bool
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT initialized FROM queue_enrichment_bootstrap WHERE singleton),false)
 AND COALESCE((SELECT initialized FROM queue_backlog_bootstrap WHERE singleton),false)
 AND (SELECT count(*)=3 AND bool_and(initialized) FROM queue_metric_bootstrap)`).Scan(&initialized)
	if err != nil {
		return err
	}
	if !initialized {
		return errors.New("queue initialization must finish before journal cutover")
	}
	for {
		var dirty bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM parallel_queue_observations WHERE revision<>applied_revision)`).Scan(&dirty); err != nil {
			return err
		}
		if !dirty {
			break
		}
		if _, err = ReconcileParallelQueueTx(ctx, tx, 1000); err != nil {
			return err
		}
	}
	var position int64
	err = tx.QueryRow(ctx, `SELECT GREATEST(COALESCE((SELECT max(last_sequence) FROM organization_journals),0),
 (SELECT CASE WHEN is_called THEN last_value ELSE last_value-1 END FROM change_event_positions))`).Scan(&position)
	if err != nil {
		return err
	}
	if parallel {
		_, err = tx.Exec(ctx, `SELECT setval('change_event_positions',GREATEST($1::bigint,1),$1::bigint>0)`, position)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO organization_journals(organization,last_sequence)
 SELECT DISTINCT organization,$1::bigint FROM corpora
 ON CONFLICT(organization) DO UPDATE SET last_sequence=GREATEST(organization_journals.last_sequence,EXCLUDED.last_sequence)`, position)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE change_journal_publication SET parallel=$1,epoch=epoch+1,
 published_through=$2,sampled_through=$2,sample_xid=NULL,sampled_at=NULL,published_at=clock_timestamp() WHERE singleton`, parallel, position)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResetAfterRestore is used only with every service stopped after restoring to
// a new PostgreSQL cluster. Saved snapshots and pending XID observations belong
// to the old cluster; invalidate them and prove a prefix with the writer drain
// instead. Existing numeric cursors and event identities remain unchanged.
func (s JournalStore) ResetAfterRestore(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, queueMaintenanceLock); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, journalModeLock); err != nil {
		return err
	}
	var upper int64
	err = tx.QueryRow(ctx, `SELECT GREATEST(COALESCE((SELECT max(sequence) FROM change_events),0),
 COALESCE((SELECT max(last_sequence) FROM organization_journals),0),
 (SELECT CASE WHEN is_called THEN last_value ELSE last_value-1 END FROM change_event_positions))`).Scan(&upper)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT setval('change_event_positions',GREATEST($1::bigint,1),$1::bigint>0)`, upper); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM change_feed_starts`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE change_journal_publication SET epoch=epoch+1,published_through=$1,sampled_through=$1,
 sample_xid=NULL,sampled_at=NULL,published_at=clock_timestamp() WHERE singleton`, upper)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Advance takes one bounded publication turn. Sampling and assigning its XID
// use separate top-level transactions: nextval may itself assign an XID. Writers
// assign their XID before nextval, so any allocation below B has XID below X.
// A later snapshot xmin > X therefore proves B is a complete prefix.
func (s JournalStore) Advance(ctx context.Context) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `WITH horizon AS MATERIALIZED (
 SELECT pg_snapshot_xmin(pg_current_snapshot()) AS xmin
 ) UPDATE change_journal_publication p SET published_through=sampled_through,
 sample_xid=NULL,published_at=clock_timestamp()
 FROM horizon h WHERE singleton AND parallel AND sample_xid<h.xmin`)
	if err != nil {
		return false, err
	}
	progress := tag.RowsAffected() != 0
	var parallel bool
	var epoch, head, sampled, allocated int64
	var pending *string
	err = s.Pool.QueryRow(ctx, `SELECT parallel,epoch,published_through,sampled_through,sample_xid::text,
 (SELECT CASE WHEN is_called THEN last_value ELSE last_value-1 END FROM change_event_positions)
 FROM change_journal_publication WHERE singleton`).Scan(&parallel, &epoch, &head, &sampled, &pending, &allocated)
	if err != nil {
		return progress, err
	}
	if !parallel || pending != nil || allocated <= head {
		return progress, nil
	}
	var barrier int64
	if err = s.Pool.QueryRow(ctx, `WITH fence AS MATERIALIZED (SELECT pg_advisory_xact_lock_shared($1))
 SELECT nextval('change_event_positions') FROM fence`, journalModeLock).Scan(&barrier); err != nil {
		return progress, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return progress, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var xid string
	if err = tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xid); err != nil {
		return progress, err
	}
	// Another sampler or a mode change may win. Its valid observation remains;
	// losing this CAS leaves only a harmless gap in the sequence.
	_, err = tx.Exec(ctx, `UPDATE change_journal_publication SET sampled_through=$1,
 sample_xid=$2::xid8,sampled_at=clock_timestamp()
 WHERE singleton AND parallel AND epoch=$3 AND sample_xid IS NULL
 AND sampled_through=$4 AND published_through=$5`, barrier, xid, epoch, sampled, head)
	if err != nil {
		return progress, err
	}
	return progress, tx.Commit(ctx)
}

// Run keeps publication responsibility alive in both API and worker processes.
// The durable observation CAS makes simultaneous samplers and restarts safe.
func (s JournalStore) Run(ctx context.Context) {
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := s.Advance(attempt)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "change journal publication unavailable")
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// lockJournalRequest supplies request/entity uniqueness before a fresh guarded
// read. Legacy mode already owns its organization fence; it creates no claims.
// Using ON CONFLICT DO NOTHING followed by SELECT FOR UPDATE serializes
// concurrent writers without rewriting heap tuples (avoiding dead-tuple bloat).
const journalClaimInsertSQL = `INSERT INTO journal_request_claims(organization,family,request_key)
 SELECT $1,$2,$3 WHERE ` + parallelJournalSQL + `
 ON CONFLICT(organization,family,request_key) DO NOTHING`

const journalClaimLockSQL = `SELECT 1 FROM journal_request_claims
 WHERE organization=$1 AND family=$2 AND request_key=$3 AND ` + parallelJournalSQL + `
 FOR UPDATE`

func queueJournalClaim(batch *pgx.Batch, org, family, key string) {
	batch.Queue(journalClaimInsertSQL, org, family, key)
	batch.Queue(journalClaimLockSQL, org, family, key)
}

func lockJournalRequest(ctx context.Context, tx pgx.Tx, org, family, key string) error {
	batch := &pgx.Batch{}
	queueJournalClaim(batch, org, family, key)
	return tx.SendBatch(ctx, batch).Close()
}

func lockJournalRecord(ctx context.Context, tx pgx.Tx, org, record string) error {
	if record == "" {
		return fmt.Errorf("missing record identity")
	}
	batch := &pgx.Batch{}
	queueJournalClaim(batch, org, "record", record)
	batch.Queue(`SELECT id FROM records WHERE organization=$1 AND id=$2 AND `+parallelJournalSQL+` FOR UPDATE`, org, record)
	return tx.SendBatch(ctx, batch).Close()
}

// lockJournalRecords uses the same identity fence as acceptance before any
// speculative Version writes. Sorted acquisition keeps multi-Record commits
// from inverting another group's locks.
func lockJournalRecords(ctx context.Context, tx pgx.Tx, org string, records []string) error {
	keys := slices.Clone(records)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO journal_request_claims(organization,family,request_key)
 SELECT $1,'record',k FROM unnest($2::text[]) k WHERE `+parallelJournalSQL+`
 ORDER BY k
 ON CONFLICT(organization,family,request_key) DO NOTHING`, org, keys)
	batch.Queue(`SELECT request_key FROM journal_request_claims
 WHERE organization=$1 AND family='record' AND request_key=ANY($2::text[]) AND `+parallelJournalSQL+`
 ORDER BY request_key FOR UPDATE`, org, keys)
	batch.Queue(`SELECT id FROM records WHERE organization=$1 AND id=ANY($2) AND `+parallelJournalSQL+` ORDER BY id FOR UPDATE`, org, keys)
	return tx.SendBatch(ctx, batch).Close()
}

func lockJournalVersion(ctx context.Context, tx pgx.Tx, org, version string) error {
	// The Version-to-Record identity is immutable. Acquire its Record before
	// any shared hash or mutable guard, then read canonical state afresh.
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO journal_request_claims(organization,family,request_key)
 SELECT organization,'record',record_id FROM record_versions
 WHERE organization=$1 AND id=$2 AND `+parallelJournalSQL+`
 ON CONFLICT(organization,family,request_key) DO NOTHING`, org, version)
	batch.Queue(`SELECT c.request_key FROM journal_request_claims c
 JOIN record_versions v ON (v.organization,v.record_id)=(c.organization,c.request_key)
 WHERE v.organization=$1 AND v.id=$2 AND c.family='record' AND `+parallelJournalSQL+`
 FOR UPDATE OF c`, org, version)
	batch.Queue(`SELECT r.id FROM records r JOIN record_versions v ON (v.organization,v.record_id)=(r.organization,r.id)
 WHERE v.organization=$1 AND v.id=$2 AND `+parallelJournalSQL+` FOR UPDATE OF r`, org, version)
	return tx.SendBatch(ctx, batch).Close()
}

// batchPositionsSQL is a CTE for an ordered input batch. The writer CTE
// assigns the top-level XID before any nextval in the consuming statement.
// Parallel callers persist each returned number; they never infer a range.
func batchPositionsSQL(count string) string {
	return `mode AS MATERIALIZED (SELECT ` + parallelJournalSQL + ` AS parallel),
 writer AS MATERIALIZED (SELECT pg_current_xact_id() AS xid FROM mode WHERE parallel),
 legacy AS (UPDATE organization_journals SET last_sequence=last_sequence+` + count + `
 WHERE organization=$1 AND NOT (SELECT parallel FROM mode) RETURNING last_sequence-` + count + ` AS base),
 positions AS MATERIALIZED (
 SELECT base,NULL::xid8 AS xid,false AS parallel FROM legacy
 UNION ALL SELECT 0,xid,true FROM writer)`
}

// subscriptionBoundarySQL includes the current edit even when its older
// Version would still be eligible. NULL legacy fields mean the old boundary.
func subscriptionBoundarySQL(alias string) string {
	return `GREATEST(COALESCE(` + alias + `.enabled_position,0),COALESCE(` + alias + `.disabled_position,0),
 COALESCE(` + alias + `.deleted_position,0),COALESCE((SELECT activation_position FROM subscription_versions cv
 WHERE (cv.organization,cv.id)=(` + alias + `.organization,` + alias + `.current_version_id)),0))`
}

func pendingSubscriptionsSQL(org string) string {
	return `EXISTS(SELECT FROM subscriptions s WHERE s.organization=` + org + ` AND ` +
		subscriptionBoundarySQL("s") + `>` + journalHeadSQL(org) + `)`
}

// Dispatch pauses while a canonical Subscription edit is ahead of the safe
// prefix. Filtering it out would permanently skip its earlier trigger events.
func dispatchHeadSQL(org, checkpoint string) string {
	return `CASE WHEN ` + parallelJournalSQL + ` AND ` + pendingSubscriptionsSQL(org) + ` THEN ` + checkpoint +
		` ELSE COALESCE(` + journalHeadSQL(org) + `,0) END`
}

func lockEvaluationResources(ctx context.Context, tx pgx.Tx, org string, records, subscriptions []string) error {
	if err := lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	if err := lockJournalRecords(ctx, tx, org, records); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	batch.Queue(`SELECT id FROM records WHERE organization=$1 AND id=ANY($2) AND `+parallelJournalSQL+` ORDER BY id FOR UPDATE`, org, records)
	batch.Queue(`SELECT id FROM subscriptions WHERE organization=$1 AND id=ANY($2) AND `+parallelJournalSQL+` ORDER BY id FOR UPDATE`, org, subscriptions)
	return tx.SendBatch(ctx, batch).Close()
}

func requirePublishedResources(ctx context.Context, tx pgx.Tx, org string, records, subscriptions []string, positions []int64) error {
	var pending bool
	err := tx.QueryRow(ctx, `SELECT `+parallelJournalSQL+` AND (
 EXISTS(SELECT FROM subscriptions s WHERE s.organization=$1 AND s.id=ANY($3) AND `+subscriptionBoundarySQL("s")+`>`+journalHeadSQL("$1")+`)
 OR EXISTS(SELECT FROM change_events e WHERE e.organization=$1 AND e.resource_type='record' AND e.resource_id=ANY($2) AND e.sequence>`+journalHeadSQL("$1")+`)
 OR EXISTS(SELECT FROM unnest($4::bigint[]) p WHERE p>`+journalHeadSQL("$1")+`))`, org, records, subscriptions, positions).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return ErrJournalUnpublished
	}
	return nil
}

// queueDeliveryResourceLocks runs before outbox/delivery locks. Subscription
// toggles take Subscription then outbox locks; matching takes Record then
// Subscription. The common order prevents admission racing either writer.
func queueDeliveryResourceLocks(batch *pgx.Batch, org string, deliveries []string) {
	batch.Queue(`INSERT INTO journal_request_claims(organization,family,request_key)
 SELECT DISTINCT m.organization,'record',m.record_id FROM deliveries d JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
 WHERE d.organization=$1 AND d.id=ANY($2) AND `+parallelJournalSQL+` ORDER BY m.organization,m.record_id
 ON CONFLICT(organization,family,request_key) DO NOTHING`, org, deliveries)
	batch.Queue(`SELECT c.request_key FROM journal_request_claims c
 WHERE c.organization=$1 AND c.family='record' AND `+parallelJournalSQL+` AND EXISTS(
 SELECT FROM deliveries d JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
 WHERE d.organization=$1 AND d.id=ANY($2) AND m.record_id=c.request_key) ORDER BY c.request_key FOR UPDATE`, org, deliveries)
	batch.Queue(`SELECT id FROM records r WHERE r.organization=$1 AND `+parallelJournalSQL+` AND EXISTS(
 SELECT FROM deliveries d JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
 WHERE d.organization=$1 AND d.id=ANY($2) AND m.record_id=r.id) ORDER BY id FOR UPDATE`, org, deliveries)
	batch.Queue(`SELECT id FROM subscriptions s WHERE s.organization=$1 AND `+parallelJournalSQL+` AND EXISTS(
 SELECT FROM deliveries d JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
 WHERE d.organization=$1 AND d.id=ANY($2) AND m.subscription_id=s.id) ORDER BY id FOR UPDATE`, org, deliveries)
}

func requirePublishedDeliveries(ctx context.Context, tx pgx.Tx, org string, deliveries []string) error {
	var pending bool
	err := tx.QueryRow(ctx, `SELECT `+parallelJournalSQL+` AND EXISTS(
 SELECT FROM deliveries d JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
 JOIN subscriptions s ON (s.organization,s.id)=(m.organization,m.subscription_id)
 JOIN monitoring_notices n ON (n.organization,n.event_id)=(d.organization,d.event_id)
 WHERE d.organization=$1 AND d.id=ANY($2) AND (
 n.position>`+journalHeadSQL("$1")+` OR `+subscriptionBoundarySQL("s")+`>`+journalHeadSQL("$1")+`
 OR EXISTS(SELECT FROM monitoring_notices later WHERE later.organization=$1 AND later.record_id=m.record_id
 AND later.subscription_id=s.id AND later.position>`+journalHeadSQL("$1")+`)
 OR EXISTS(SELECT FROM change_events e WHERE e.organization=$1 AND e.resource_type='record'
 AND e.resource_id=m.record_id AND e.sequence>`+journalHeadSQL("$1")+`)))`, org, deliveries).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return ErrJournalUnpublished
	}
	return nil
}
