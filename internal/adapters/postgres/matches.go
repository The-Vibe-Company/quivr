package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Attempts reads a Delivery's attempt facts in number order.
func (s MatchStore) Attempts(ctx context.Context, org, deliveryID string, after, limit int) ([]monitoring.Attempt, error) {
	rows, err := s.Pool.Query(ctx, `SELECT a.id,a.number,coalesce(o.outcome,'in_flight'),coalesce(o.http_status,0),coalesce(o.error_code,''),coalesce(o.error_message,'')
FROM delivery_attempts a LEFT JOIN delivery_attempt_outcomes o ON (o.organization,o.attempt_id)=(a.organization,a.id)
WHERE a.organization=$1 AND a.delivery_id=$2 AND a.number>$3 ORDER BY a.number LIMIT $4`, org, deliveryID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []monitoring.Attempt{}
	for rows.Next() {
		a := monitoring.Attempt{DeliveryID: deliveryID}
		if err = rows.Scan(&a.ID, &a.Number, &a.Outcome, &a.HTTPStatus, &a.ErrorCode, &a.ErrorMessage); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s MatchStore) Match(ctx context.Context, org, id string) (monitoring.Match, error) {
	m, err := scanMatch(s.Pool.QueryRow(ctx, `SELECT `+matchColumns+matchFrom+`WHERE m.organization=$1 AND m.id=$2`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, monitoring.ErrNotFound
	}
	return m, err
}

func (s MatchStore) Matches(ctx context.Context, org, subscriptionID string, after int64, limit int) ([]monitoring.Match, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+matchColumns+matchFrom+`WHERE m.organization=$1 AND m.subscription_id=$2 AND m.position>$3 ORDER BY m.position LIMIT $4`, org, subscriptionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []monitoring.Match{}
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Delivery reads a logical Delivery, its immutable notice bytes and the
// current admission view derived from canonical state.
func (s MatchStore) Delivery(ctx context.Context, org, id string) (monitoring.Delivery, error) {
	var d monitoring.Delivery
	var enabled, deleted, withdrawn bool
	var later monitoring.Later
	var kind string
	var next *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT d.id,d.match_id,m.subscription_id,d.destination_id,d.state,d.attempt_count,n.body,n.kind,s.enabled,s.deleted,
  `+recordGoneSQL+`,`+laterNoticesSQL+`,
  d.last_outcome,coalesce(last.error_code,''),coalesce(last.error_message,''),
  (SELECT o.available_at FROM delivery_outbox o WHERE o.organization=d.organization AND o.delivery_id=d.id AND o.available_at<'infinity')
FROM deliveries d
LEFT JOIN LATERAL (SELECT o.error_code,o.error_message FROM delivery_attempts a
  JOIN delivery_attempt_outcomes o ON (o.organization,o.attempt_id)=(a.organization,a.id)
  WHERE a.organization=d.organization AND a.delivery_id=d.id AND a.number=d.attempt_count AND o.outcome<>'acknowledged') last ON true
JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
JOIN monitoring_notices n ON (n.organization,n.event_id)=(d.organization,d.event_id)
JOIN subscriptions s ON (s.organization,s.id)=(m.organization,m.subscription_id)
JOIN records r ON (r.organization,r.id)=(m.organization,m.record_id)
WHERE d.organization=$1 AND d.id=$2`, org, id).Scan(&d.ID, &d.MatchID, &d.SubscriptionID, &d.DestinationID, &d.State, &d.AttemptCount, &d.Event, &kind, &enabled, &deleted, &withdrawn, &later.Corrected, &later.NoLongerMatches,
		&d.LastOutcome, &d.LastErrorCode, &d.LastErrorMessage, &next)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, monitoring.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	switch {
	case d.State == "delivered" || d.State == "exhausted":
		d.Admission = monitoring.Admission{Reason: "terminal"}
	case monitoring.AdmissionReason(kind, enabled, deleted, withdrawn, later) != "":
		d.Admission = monitoring.Admission{Reason: monitoring.AdmissionReason(kind, enabled, deleted, withdrawn, later)}
	default:
		d.Admission = monitoring.Admission{Allowed: true}
		// Scheduled work of an admissible pending Delivery; a claimed or
		// delivering one has no future eligibility to report.
		if d.State == "pending" && next != nil {
			d.NextAttemptAt = next
		}
	}
	return d, nil
}

// MatchStore persists monitoring matches, deliveries and attempt history.
type MatchStore struct{ Pool *pgxpool.Pool }

var _ monitoring.MatchStore = MatchStore{}
