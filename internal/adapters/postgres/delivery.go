package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// DeliveryStore owns delivery work claims and append-only attempt facts.
// Admission and outcome commit under the Organization journal lock, which
// disable and withdrawal also take, and never span network I/O.
type DeliveryStore struct {
	ContentStore
	// Organization restricts claims to one Organization (tests); empty claims any.
	Organization string
}

// ClaimDelivery leases one due delivery outbox row.
func (s DeliveryStore) ClaimDelivery(ctx context.Context, lease time.Duration) (monitoring.DeliveryWork, error) {
	var w monitoring.DeliveryWork
	err := s.Pool.QueryRow(ctx, `UPDATE delivery_outbox o SET lease_until=now()+make_interval(secs => $1::double precision)
FROM (SELECT organization,delivery_id FROM delivery_outbox
  WHERE available_at<=now() AND lease_until<now() AND ($2='' OR organization=$2)
  ORDER BY available_at LIMIT 1 FOR UPDATE SKIP LOCKED) due
WHERE (o.organization,o.delivery_id)=(due.organization,due.delivery_id)
RETURNING o.organization,o.delivery_id,o.lease_until`, lease.Seconds(), s.Organization).Scan(&w.Organization, &w.DeliveryID, &w.Lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, monitoring.ErrNoWork
	}
	return w, err
}

// deliveryUpdated appends the feed-only delivery.updated event for one state
// transition. It creates no Delivery or outbox work, so it never causes a webhook.
func deliveryUpdated(ctx context.Context, tx pgx.Tx, org, corpusID, deliveryID string, number int, state string) error {
	return appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: "delivery.updated", Resource: "delivery", ResourceID: deliveryID, MutationID: fmt.Sprint(deliveryID, ":", number, ":", state)})
}

func park(ctx context.Context, tx pgx.Tx, org, deliveryID string) error {
	_, err := tx.Exec(ctx, `UPDATE delivery_outbox SET available_at='infinity',lease_until='-infinity' WHERE organization=$1 AND delivery_id=$2`, org, deliveryID)
	return err
}

func attemptID(org, deliveryID string, number int) string {
	return content.StableID("attempt", org, deliveryID, fmt.Sprint(number))
}

// Admit rechecks canonical admission under the journal lock and commits the
// attempt fact with the Delivery's move to delivering. A Delivery found still
// delivering was lost by a crashed worker (its lease expired): its attempt is
// recorded unknown and a fresh attempt is admitted for the same notice.
func (s DeliveryStore) Admit(ctx context.Context, w monitoring.DeliveryWork, configured func(org, destinationID string) bool) (monitoring.AdmittedAttempt, string, error) {
	org := w.Organization
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return monitoring.AdmittedAttempt{}, "", err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return monitoring.AdmittedAttempt{}, "", err
	}
	// Fence stale claims: a step that outlived its lease must not admit work
	// another worker now holds or that an outcome already parked.
	var held bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2 AND lease_until=$3 AND lease_until>now() FOR UPDATE)`, org, w.DeliveryID, w.Lease).Scan(&held); err != nil {
		return monitoring.AdmittedAttempt{}, "", err
	}
	if !held {
		return monitoring.AdmittedAttempt{}, "lease_lost", nil
	}
	var state, destination, eventID, corpusID string
	var count int
	var body []byte
	var enabled, withdrawn bool
	err = tx.QueryRow(ctx, `SELECT d.state,d.attempt_count,d.destination_id,d.event_id,n.body,m.corpus_id,s.enabled,
  r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)
FROM deliveries d
JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
JOIN monitoring_notices n ON (n.organization,n.event_id)=(d.organization,d.event_id)
JOIN subscriptions s ON (s.organization,s.id)=(m.organization,m.subscription_id)
JOIN records r ON (r.organization,r.id)=(m.organization,m.record_id)
WHERE d.organization=$1 AND d.id=$2 FOR UPDATE OF d`, org, w.DeliveryID).Scan(&state, &count, &destination, &eventID, &body, &corpusID, &enabled, &withdrawn)
	if err != nil {
		return monitoring.AdmittedAttempt{}, "", err
	}
	refuse := func(reason string) (monitoring.AdmittedAttempt, string, error) {
		if err := park(ctx, tx, org, w.DeliveryID); err != nil {
			return monitoring.AdmittedAttempt{}, "", err
		}
		return monitoring.AdmittedAttempt{}, reason, tx.Commit(ctx)
	}
	switch state {
	case "delivered", "exhausted":
		if _, err = tx.Exec(ctx, `DELETE FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2`, org, w.DeliveryID); err != nil {
			return monitoring.AdmittedAttempt{}, "", err
		}
		return monitoring.AdmittedAttempt{}, "terminal", tx.Commit(ctx)
	case "delivering":
		tag, err := tx.Exec(ctx, `INSERT INTO delivery_attempt_outcomes(organization,attempt_id,outcome,error_code,error_message) VALUES($1,$2,'unknown','webhook_outcome_unknown','attempt outcome unknown after worker interruption') ON CONFLICT DO NOTHING`, org, attemptID(org, w.DeliveryID, count))
		if err != nil {
			return monitoring.AdmittedAttempt{}, "", err
		}
		if tag.RowsAffected() == 1 {
			if _, err = tx.Exec(ctx, `UPDATE deliveries SET state='pending',last_outcome='unknown' WHERE organization=$1 AND id=$2`, org, w.DeliveryID); err != nil {
				return monitoring.AdmittedAttempt{}, "", err
			}
			if err = deliveryUpdated(ctx, tx, org, corpusID, w.DeliveryID, count, "pending"); err != nil {
				return monitoring.AdmittedAttempt{}, "", err
			}
		}
	}
	switch {
	case !configured(org, destination):
		return refuse("destination_unavailable")
	case !enabled:
		return refuse("subscription_disabled")
	case withdrawn:
		return refuse("record_withdrawn")
	}
	a := monitoring.AdmittedAttempt{Organization: org, DeliveryID: w.DeliveryID, Number: count + 1, EventID: eventID, DestinationID: destination, Body: body}
	a.AttemptID = attemptID(org, w.DeliveryID, a.Number)
	if _, err = tx.Exec(ctx, `INSERT INTO delivery_attempts(organization,id,delivery_id,number) VALUES($1,$2,$3,$4)`, org, a.AttemptID, a.DeliveryID, a.Number); err != nil {
		return monitoring.AdmittedAttempt{}, "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE deliveries SET state='delivering',attempt_count=$3 WHERE organization=$1 AND id=$2`, org, a.DeliveryID, a.Number); err != nil {
		return monitoring.AdmittedAttempt{}, "", err
	}
	if err = deliveryUpdated(ctx, tx, org, corpusID, a.DeliveryID, a.Number, "delivering"); err != nil {
		return monitoring.AdmittedAttempt{}, "", err
	}
	return a, "", tx.Commit(ctx)
}

// Record appends the attempt outcome once and moves the Delivery: delivered on
// acknowledgement, otherwise back to pending with its work parked until retry
// scheduling exists. An outcome already recorded (for example unknown after a
// lease expiry) is never overwritten.
func (s DeliveryStore) Record(ctx context.Context, a monitoring.AdmittedAttempt, o monitoring.AttemptOutcome) error {
	org := a.Organization
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var status any
	if o.HTTPStatus != 0 {
		status = o.HTTPStatus
	}
	tag, err := tx.Exec(ctx, `INSERT INTO delivery_attempt_outcomes(organization,attempt_id,outcome,http_status,error_code,error_message) VALUES($1,$2,$3,$4,$5,left($6,200)) ON CONFLICT DO NOTHING`,
		org, a.AttemptID, o.Outcome, status, o.ErrorCode, o.ErrorMessage)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	state := "pending"
	if o.Outcome == monitoring.AttemptAcknowledged {
		state = "delivered"
	}
	var corpusID string
	err = tx.QueryRow(ctx, `UPDATE deliveries d SET state=$4,last_outcome=$5 FROM matches m
WHERE d.organization=$1 AND d.id=$2 AND d.state='delivering' AND d.attempt_count=$3 AND (m.organization,m.id)=(d.organization,d.match_id)
RETURNING m.corpus_id`, org, a.DeliveryID, a.Number, state, o.Outcome).Scan(&corpusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if err = deliveryUpdated(ctx, tx, org, corpusID, a.DeliveryID, a.Number, state); err != nil {
		return err
	}
	if state == "delivered" {
		_, err = tx.Exec(ctx, `DELETE FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2`, org, a.DeliveryID)
	} else {
		err = park(ctx, tx, org, a.DeliveryID)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Attempts reads a Delivery's attempt facts in number order.
func (s ContentStore) Attempts(ctx context.Context, org, deliveryID string, after, limit int) ([]monitoring.Attempt, error) {
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

var _ monitoring.DeliveryStore = DeliveryStore{}
