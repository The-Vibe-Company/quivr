package postgres

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReadChanges reads one consistent journal window. A cursor is expired when
// it precedes the pruned watermark, when its next eligible event is older
// than retention, or when its short-lived snapshot start has expired. The
// committed head, expiry checks and events come from one statement snapshot.
// In parallel mode the head is the publisher's complete safe prefix; a
// snapshot-aware resume also excludes rows already visible at its start.
func (s ChangeStore) ReadChanges(ctx context.Context, org, corpusID string, after int64, limit int, retention time.Duration) (changes.Window, error) {
	return s.ReadChangesResume(ctx, org, corpusID, changes.Resume{Position: after}, limit, retention)
}

// StartChanges captures an exact start-now boundary when parallel publication
// is active. The prune row is locked before the snapshot is captured, so a
// pruner cannot pass the boundary between registration and persistence. Legacy
// mode keeps the v1 numeric start behavior and creates no start reference.
func (s ChangeStore) StartChanges(ctx context.Context, org, corpusID string, retention time.Duration) (changes.Resume, error) {
	if retention <= 0 {
		retention = changes.DefaultRetention
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return changes.Resume{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, journalModeLock); err != nil {
		return changes.Resume{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO change_journal_prunes(organization) VALUES($1) ON CONFLICT DO NOTHING`, org); err != nil {
		return changes.Resume{}, err
	}
	var ignored int64
	if err = tx.QueryRow(ctx, `SELECT pruned_through FROM change_journal_prunes WHERE organization=$1 FOR UPDATE`, org).Scan(&ignored); err != nil {
		return changes.Resume{}, err
	}

	// This statement owns the exact primary snapshot and safe head used by the
	// start. The sequence ceiling is intentionally read in a later statement,
	// after the snapshot value has been captured.
	var snapshot string
	var position int64
	var parallel bool
	head := "COALESCE(" + journalHeadSQL("$1") + ",0)"
	if err = tx.QueryRow(ctx, `SELECT pg_current_snapshot()::text,`+head+`,`+parallelJournalSQL, org).Scan(&snapshot, &position, &parallel); err != nil {
		return changes.Resume{}, err
	}
	if !parallel {
		if err = tx.Commit(ctx); err != nil {
			return changes.Resume{}, err
		}
		return changes.Resume{Position: position}, nil
	}
	var ceiling int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT CASE WHEN is_called THEN last_value ELSE last_value-1 END FROM change_event_positions),0)`).Scan(&ceiling); err != nil {
		return changes.Resume{}, err
	}
	id, err := startID()
	if err != nil {
		return changes.Resume{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO change_feed_starts(id,organization,corpus_id,snapshot,position,ceiling,expires_at)
VALUES($1,$2,$3,$4::pg_snapshot,$5,$6,clock_timestamp()+make_interval(secs => $7::double precision))`, id, org, corpusID, snapshot, position, ceiling, retention.Seconds()); err != nil {
		return changes.Resume{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return changes.Resume{}, err
	}
	return changes.Resume{Position: position, StartID: id}, nil
}

// ReadChangesResume reads a v1 position or an exact-start resume state. The
// old ReadChanges method above remains the owner for callers that only know a
// numeric position.
func (s ChangeStore) ReadChangesResume(ctx context.Context, org, corpusID string, resume changes.Resume, limit int, retention time.Duration) (changes.Window, error) {
	if retention <= 0 {
		retention = changes.DefaultRetention
	}
	if resume.Position < 0 {
		return changes.Window{}, changes.ErrCursorInvalid
	}
	// The optional start reference is looked up by id and checked against both
	// scope components in the same read snapshot. A missing or expired row is
	// reported through Window.Expired so the service exposes cursor_expired.
	head := "COALESCE(" + journalHeadSQL("$1") + ",0)"
	args := []any{org, corpusID, resume.Position, max(limit, 0) + 1, retention.Seconds(), resume.StartID}
	visibleEvent := ""
	if principals, enforced := visibility.Principals(ctx); enforced {
		args = append(args, principals)
		visibleEvent = "AND " + changeVisibilitySQL("$7")
	}
	query := `
WITH head AS (
  SELECT (` + head + `)::bigint AS h
), feed_start AS (
  SELECT id, organization, corpus_id, snapshot, position, ceiling, expires_at
  FROM change_feed_starts
  WHERE id=$6
), bound AS (
  SELECT h.h,
    EXISTS(SELECT 1 FROM corpora c WHERE c.organization=$1 AND c.id=$2 AND c.archived) AS archived,
    fs.id AS start_id, fs.organization AS start_organization, fs.corpus_id AS start_corpus,
    fs.snapshot AS start_snapshot, fs.position AS start_position, fs.ceiling AS start_ceiling,
    fs.expires_at AS start_expires_at,
    ($6='' OR fs.id IS NULL OR (fs.organization=$1 AND fs.corpus_id=$2)) AS start_scope,
    ($6<>'' AND (fs.id IS NULL OR fs.expires_at<=clock_timestamp())) AS start_expired,
    ($3::bigint < COALESCE((SELECT pruned_through FROM change_journal_prunes WHERE organization=$1),0)) AS pruned
  FROM head h LEFT JOIN feed_start fs ON true
), expiry AS (
  SELECT b.*,
    b.start_expired OR b.pruned OR COALESCE((
      SELECT e.occurred_at < clock_timestamp()-make_interval(secs => $5::double precision)
      FROM change_events e
      WHERE e.organization=$1 AND e.corpus_id=$2
        AND e.sequence>$3::bigint AND e.sequence<=b.h
        ` + visibleEvent + `
        AND (b.start_id IS NULL OR
          (e.sequence>b.start_position AND
            (e.writer_xid IS NULL OR NOT pg_visible_in_snapshot(e.writer_xid,b.start_snapshot))))
      ORDER BY e.sequence
      LIMIT 1
    ),false) AS expired
  FROM bound b
)
SELECT b.h, b.expired, b.archived, b.start_scope, b.start_id, b.start_position, b.start_ceiling,
  e.sequence, e.event_id, e.event_type, e.resource_type, e.resource_id, e.occurred_at,
  n.match_id, n.record_id, n.record_version_id, n.subscription_id, n.subscription_version_id,
  coalesce(n.delivery_id,''), coalesce(n.previous_match_id,''), coalesce(s.owner,'')
FROM expiry b
LEFT JOIN LATERAL (
  SELECT sequence, event_id, event_type, resource_type, resource_id, occurred_at
  FROM change_events e
  WHERE e.organization=$1 AND e.corpus_id=$2 AND NOT b.archived
    AND e.sequence>$3::bigint AND e.sequence<=b.h
        ` + visibleEvent + `
    AND (b.start_id IS NULL OR
      (e.sequence>b.start_position AND
        (e.writer_xid IS NULL OR NOT pg_visible_in_snapshot(e.writer_xid,b.start_snapshot))))
  ORDER BY e.sequence
  LIMIT $4
) e ON true
LEFT JOIN monitoring_notices n ON n.organization=$1 AND n.event_id=e.event_id
LEFT JOIN subscriptions s ON s.organization=$1 AND s.id=n.subscription_id
ORDER BY e.sequence`

	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return changes.Window{}, err
	}
	defer rows.Close()
	var w changes.Window
	var visible []changes.Event
	var startID *string
	var startPosition, startCeiling *int64
	var startScope bool
	for rows.Next() {
		var archived bool
		var sequence *int64
		var id, kind, resource, resourceID *string
		var occurred *time.Time
		var match, record, version, subscription, subscriptionVersion, delivery, previous, owner *string
		if err = rows.Scan(&w.Head, &w.Expired, &archived, &startScope, &startID, &startPosition, &startCeiling, &sequence, &id, &kind, &resource, &resourceID, &occurred, &match, &record, &version, &subscription, &subscriptionVersion, &delivery, &previous, &owner); err != nil {
			return changes.Window{}, err
		}
		if !startScope {
			return changes.Window{}, changes.ErrCursorScope
		}
		if archived {
			return changes.Window{}, corpus.ErrArchived
		}
		if sequence != nil && limit > 0 {
			e := changes.Event{Position: *sequence, ID: *id, Type: *kind, CorpusID: corpusID, ResourceKind: *resource, ResourceID: *resourceID, OccurredAt: *occurred}
			if match != nil {
				e.Monitoring = &changes.References{MatchID: *match, RecordID: *record, RecordVersionID: *version, SubscriptionID: *subscription, SubscriptionVersionID: *subscriptionVersion, DeliveryID: *delivery, PreviousMatchID: *previous, Owner: *owner}
			}
			visible = append(visible, e)
		}
	}
	if err = rows.Err(); err != nil {
		return changes.Window{}, err
	}
	switch {
	case limit <= 0:
		w.Through = resume.Position
	case len(visible) > limit:
		w.Events = visible[:limit]
		w.Through = visible[limit-1].Position
	default:
		w.Events = visible
		w.Through = w.Head
	}
	if startID != nil {
		w.ResumeSet = true
		if startCeiling != nil {
			w.StartCeiling = *startCeiling
		}
		keep := *startID
		if startCeiling != nil && w.Through >= *startCeiling {
			keep = ""
		}
		w.Resume = changes.Resume{Position: w.Through, StartID: keep}
	}
	return w, nil
}

func changeVisibilitySQL(principals string) string {
	return `EXISTS (
 SELECT 1 FROM records r
 WHERE r.organization=e.organization AND r.corpus_id=e.corpus_id
 AND r.id=CASE e.resource_type
   WHEN 'record' THEN e.resource_id
   WHEN 'receipt' THEN (SELECT record_id FROM ingestion_receipts rc WHERE rc.organization=e.organization AND rc.id=e.resource_id)
   WHEN 'match' THEN (SELECT record_id FROM monitoring_notices n WHERE n.organization=e.organization AND n.event_id=e.event_id)
   WHEN 'delivery' THEN (
     SELECT m.record_id FROM deliveries d JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
     WHERE (d.organization,d.id)=(e.organization,e.resource_id)
   )
   ELSE (SELECT record_id FROM record_versions v WHERE v.organization=e.organization AND v.id=e.record_version_id)
 END AND ` + recordVisibilitySQL("r", principals) + ")"
}

func (s ChangeStore) VisibleChange(ctx context.Context, org, corpusID string, position int64) (bool, error) {
	principals, enforced := visibility.Principals(ctx)
	if !enforced {
		return true, nil
	}
	var allowed bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM change_events e
 WHERE e.organization=$1 AND e.corpus_id=$2 AND e.sequence=$3 AND `+changeVisibilitySQL("$4")+`)`, org, corpusID, position, principals).Scan(&allowed)
	return allowed, err
}

func startID() (string, error) {
	b := make([]byte, 24)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PruneChanges physically deletes aged journal events of the named
// Organizations (all when organizations is empty). Each batch is its own
// transaction over a prefix of at most batch events; at most
// batches run per Organization. It returns the number of deleted events.
func (s ChangeStore) PruneChanges(ctx context.Context, retention time.Duration, organizations []string, batch, batches int) (int, error) {
	// pgx sends a nil slice as NULL: coalesce keeps "no list" meaning every Organization.
	rows, err := s.Pool.Query(ctx, `WITH organizations AS (
  SELECT organization FROM organization_journals
  UNION SELECT organization FROM change_feed_starts
  UNION SELECT organization FROM change_events
)
SELECT organization FROM organizations
WHERE coalesce(cardinality($1::text[]),0)=0 OR organization=ANY($1)
ORDER BY organization`, organizations)
	if err != nil {
		return 0, err
	}
	orgs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, org := range orgs {
		for range batches {
			n, err := s.pruneBatch(ctx, org, retention, batch)
			deleted += n
			if err != nil {
				return deleted, err
			}
			if n == 0 {
				break
			}
		}
	}
	return deleted, nil
}

// pruneBatch deletes the positions (pruned_through, upper]. upper never
// passes the safe published head, the evaluation dispatch checkpoint (or,
// before dispatch starts, the earliest activation boundary), the first event
// still inside retention, or a live exact-start boundary. It holds only the
// Organization's prune row, never the journal lock, and skips an Organization
// another prune holds.
func (s ChangeStore) pruneBatch(ctx context.Context, org string, retention time.Duration, batch int) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO change_journal_prunes(organization) VALUES($1) ON CONFLICT DO NOTHING`, org); err != nil {
		return 0, err
	}
	var pruned int64
	err = tx.QueryRow(ctx, `SELECT pruned_through FROM change_journal_prunes WHERE organization=$1 FOR UPDATE SKIP LOCKED`, org).Scan(&pruned)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// Start snapshots expire with cursor retention. The bounded cleanup is
	// serialized with new start registration by the same prune row.
	if _, err = tx.Exec(ctx, `DELETE FROM change_feed_starts WHERE id IN (
      SELECT id FROM change_feed_starts WHERE organization=$1 AND expires_at<=clock_timestamp()
      ORDER BY expires_at,id LIMIT $2)`, org, batch); err != nil {
		return 0, err
	}
	head := "COALESCE(" + journalHeadSQL("$1") + ",0)"
	var upper int64
	if err = tx.QueryRow(ctx, `
WITH bound AS (
  SELECT LEAST(`+head+`,
    COALESCE((SELECT position FROM monitoring_checkpoints WHERE organization=$1),
      (SELECT min(activation_position) FROM subscription_versions WHERE organization=$1))) AS upper,
    `+head+` AS safe_head
), page AS MATERIALIZED (
  SELECT sequence FROM change_events, bound b
  WHERE organization=$1 AND sequence>$2 AND sequence<=b.upper
  ORDER BY sequence LIMIT $3
), limited AS (
  SELECT CASE WHEN (SELECT count(*) FROM page)=$3 THEN (SELECT max(sequence) FROM page)
    ELSE b.upper END AS upper, b.safe_head FROM bound b
), pinned AS (
  SELECT min(f.position) AS position
  FROM change_feed_starts f, bound b
  WHERE f.organization=$1 AND f.expires_at>clock_timestamp() AND f.ceiling>=b.safe_head
)
SELECT LEAST(b.upper,COALESCE(p.position,b.upper),COALESCE((SELECT min(sequence)-1 FROM change_events
  WHERE organization=$1 AND sequence>$2 AND sequence<=b.upper
    AND occurred_at>=clock_timestamp()-make_interval(secs => $4::double precision)),b.upper))
FROM limited b CROSS JOIN pinned p`, org, pruned, batch, retention.Seconds()).Scan(&upper); err != nil {
		return 0, err
	}
	if upper <= pruned {
		return 0, tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM change_events WHERE organization=$1 AND sequence>$2 AND sequence<=$3`, org, pruned, upper)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE change_journal_prunes SET pruned_through=$2,pruned_at=clock_timestamp() WHERE organization=$1`, org, upper); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), tx.Commit(ctx)
}

// ChangeStore persists changes state.
type ChangeStore struct{ Pool *pgxpool.Pool }
