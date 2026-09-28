package postgres

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
)

// changeScanBudget bounds how many journal positions one read may traverse,
// so busy neighbouring Corpora cannot make a single poll unbounded.
const changeScanBudget = 1000

// ReadChanges reads one consistent journal window. The committed head, the
// retention check and the events come from a single statement snapshot, and
// writers allocate positions under the Organization journal lock held until
// commit, so every position at or below the head is committed and scanned.
func (s ContentStore) ReadChanges(ctx context.Context, org, corpusID string, after int64, limit int, retention time.Duration) (changes.Window, error) {
	rows, err := s.Pool.Query(ctx, `
WITH head AS (
  SELECT COALESCE((SELECT last_sequence FROM organization_journals WHERE organization=$1), 0) AS h
), bound AS (
  SELECT h, LEAST(h, $3::bigint + $5::bigint) AS upper,
    COALESCE((SELECT occurred_at < now() - make_interval(secs => $6::double precision) FROM change_events WHERE organization=$1 AND sequence=$3::bigint + 1), false) AS expired
  FROM head
)
SELECT b.h, b.upper, b.expired, e.sequence, e.event_id, e.event_type, e.resource_type, e.resource_id, e.occurred_at
FROM bound b
LEFT JOIN LATERAL (
  SELECT sequence, event_id, event_type, resource_type, resource_id, occurred_at
  FROM change_events
  WHERE organization=$1 AND corpus_id=$2 AND sequence > $3 AND sequence <= b.upper
  ORDER BY sequence
  LIMIT $4
) e ON true`, org, corpusID, after, max(limit, 0)+1, changeScanBudget, retention.Seconds())
	if err != nil {
		return changes.Window{}, err
	}
	defer rows.Close()
	var w changes.Window
	var upper int64
	var visible []changes.Event
	for rows.Next() {
		var sequence *int64
		var id, kind, resource, resourceID *string
		var occurred *time.Time
		if err = rows.Scan(&w.Head, &upper, &w.Expired, &sequence, &id, &kind, &resource, &resourceID, &occurred); err != nil {
			return changes.Window{}, err
		}
		if sequence != nil {
			visible = append(visible, changes.Event{Position: *sequence, ID: *id, Type: *kind, CorpusID: corpusID, ResourceKind: *resource, ResourceID: *resourceID, OccurredAt: *occurred})
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
