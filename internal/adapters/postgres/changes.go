package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// changeScanBudget bounds how many journal positions one read may traverse,
// so busy neighbouring Corpora cannot make a single poll unbounded.
const changeScanBudget = 1000

// ReadChanges reads one consistent journal window. A cursor is expired when
// it precedes the pruned watermark (its next events are physically gone) or
// when its next event is older than retention. The committed head, the
// expiry check and the events come from a single statement snapshot, and
// writers allocate positions under the Organization journal lock held until
// commit, so every position at or below the head is committed and scanned.
func (s ChangeStore) ReadChanges(ctx context.Context, org, corpusID string, after int64, limit int, retention time.Duration) (changes.Window, error) {
	rows, err := s.Pool.Query(ctx, `
WITH head AS (
  SELECT COALESCE((SELECT last_sequence FROM organization_journals WHERE organization=$1), 0) AS h
), bound AS (
  SELECT h, LEAST(h, $3::bigint + $5::bigint) AS upper,
    EXISTS(SELECT 1 FROM corpora c WHERE c.organization=$1 AND c.id=$2 AND c.archived) AS archived,
    $3::bigint < COALESCE((SELECT pruned_through FROM change_journal_prunes WHERE organization=$1), 0)
    OR COALESCE((SELECT occurred_at < now() - make_interval(secs => $6::double precision) FROM change_events WHERE organization=$1 AND sequence=$3::bigint + 1), false) AS expired
  FROM head
)
SELECT b.h, b.upper, b.expired, b.archived, e.sequence, e.event_id, e.event_type, e.resource_type, e.resource_id, e.occurred_at,
  n.match_id, n.record_id, n.record_version_id, n.subscription_id, n.subscription_version_id, n.delivery_id, coalesce(n.previous_match_id,''), coalesce(s.owner,'')
FROM bound b
LEFT JOIN LATERAL (
  SELECT sequence, event_id, event_type, resource_type, resource_id, occurred_at
  FROM change_events
  WHERE organization=$1 AND corpus_id=$2 AND NOT b.archived AND sequence > $3 AND sequence <= b.upper
  ORDER BY sequence
  LIMIT $4
) e ON true
LEFT JOIN monitoring_notices n ON n.organization=$1 AND n.event_id=e.event_id
LEFT JOIN subscriptions s ON s.organization=$1 AND s.id=n.subscription_id
ORDER BY e.sequence`, org, corpusID, after, max(limit, 0)+1, changeScanBudget, retention.Seconds())
	if err != nil {
		return changes.Window{}, err
	}
	defer rows.Close()
	var w changes.Window
	var upper int64
	var visible []changes.Event
	for rows.Next() {
		var archived bool
		var sequence *int64
		var id, kind, resource, resourceID *string
		var occurred *time.Time
		var match, record, version, subscription, subscriptionVersion, delivery, previous, owner *string
		if err = rows.Scan(&w.Head, &upper, &w.Expired, &archived, &sequence, &id, &kind, &resource, &resourceID, &occurred, &match, &record, &version, &subscription, &subscriptionVersion, &delivery, &previous, &owner); err != nil {
			return changes.Window{}, err
		}
		if archived {
			return changes.Window{}, corpus.ErrArchived
		}
		if sequence != nil {
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
		w.Through = after
	case len(visible) > limit:
		w.Events = visible[:limit]
		w.Through = visible[limit-1].Position
	default:
		w.Events = visible
		w.Through = upper
	}
	return w, nil
}

// PruneChanges physically deletes aged journal events of the named
// Organizations (all when organizations is empty). Each batch is its own
// transaction over a contiguous prefix of at most batch positions; at most
// batches run per Organization. It returns the number of deleted events.
func (s ChangeStore) PruneChanges(ctx context.Context, retention time.Duration, organizations []string, batch, batches int) (int, error) {
	// pgx sends a nil slice as NULL: coalesce keeps "no list" meaning every Organization.
	rows, err := s.Pool.Query(ctx, `SELECT organization FROM organization_journals WHERE coalesce(cardinality($1::text[]),0)=0 OR organization=ANY($1) ORDER BY organization`, organizations)
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
// passes the committed head, the evaluation dispatch checkpoint (or, before
// dispatch starts, the earliest activation boundary), nor the first event
// still inside retention. It holds only the Organization's prune row, never
// the journal lock, and skips an Organization another prune holds.
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
	// Every position at or below the committed head is committed: writers
	// allocate positions under the journal lock held until commit.
	var upper int64
	if err = tx.QueryRow(ctx, `
WITH bound AS (
  SELECT LEAST($2::bigint + $3::bigint,
    COALESCE((SELECT last_sequence FROM organization_journals WHERE organization=$1), 0),
    COALESCE((SELECT position FROM monitoring_checkpoints WHERE organization=$1),
      (SELECT min(activation_position) FROM subscription_versions WHERE organization=$1))) AS upper
)
SELECT LEAST(b.upper, COALESCE((SELECT min(sequence) - 1 FROM change_events
  WHERE organization=$1 AND sequence > $2 AND sequence <= b.upper AND occurred_at >= now() - make_interval(secs => $4::double precision)), b.upper))
FROM bound b`, org, pruned, batch, retention.Seconds()).Scan(&upper); err != nil {
		return 0, err
	}
	if upper <= pruned {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `DELETE FROM change_events WHERE organization=$1 AND sequence > $2 AND sequence <= $3`, org, pruned, upper)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE change_journal_prunes SET pruned_through=$2, pruned_at=now() WHERE organization=$1`, org, upper); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), tx.Commit(ctx)
}

// ChangeStore persists changes state.
type ChangeStore struct{ Pool *pgxpool.Pool }
