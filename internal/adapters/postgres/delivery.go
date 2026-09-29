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

// exhaust ends automatic attempts for a pending Delivery without a new attempt.
func exhaust(ctx context.Context, tx pgx.Tx, org, corpusID, deliveryID string, number int, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE deliveries SET state='exhausted',exhausted_reason=$3 WHERE organization=$1 AND id=$2`, org, deliveryID, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2`, org, deliveryID); err != nil {
		return err
	}
	return deliveryUpdated(ctx, tx, org, corpusID, deliveryID, number, "exhausted")
}

func attemptID(org, deliveryID string, number int) string {
	return content.StableID("attempt", org, deliveryID, fmt.Sprint(number))
}

// Admit rechecks canonical admission under the journal lock and commits the
// attempt fact with the Delivery's move to delivering. A Delivery found still
// delivering was lost by a crashed worker (its lease expired): its attempt is
// recorded unknown and a fresh attempt is admitted for the same notice.
//
// Refusals are checked before the window: work that is not admissible stays
// pending and parked. Retries are always scheduled no later than the window
// end (the final one exactly at the edge), so work that became due after the
// window end can only be parked work made claimable again (for example by a
// re-enable): it ends exhausted without an attempt, and disabling and
// re-enabling never extends the window. The window starts at the Delivery's
// creation, except for a notice committed while its Subscription was disabled
// (a withdrawal), whose window starts at the re-enable (window_start).
func (s DeliveryStore) Admit(ctx context.Context, w monitoring.DeliveryWork, window time.Duration, configured func(org, destinationID string) bool) (monitoring.AdmittedAttempt, string, error) {
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
	var state, destination, eventID, corpusID, kind string
	var count int
	var body []byte
	var enabled, withdrawn, elapsed bool
	var later monitoring.Later
	err = tx.QueryRow(ctx, `SELECT d.state,d.attempt_count,d.destination_id,d.event_id,n.body,m.corpus_id,n.kind,s.enabled,
  r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),
  `+laterNoticesSQL+`,
  coalesce((SELECT o.available_at FROM delivery_outbox o WHERE o.organization=d.organization AND o.delivery_id=d.id)>coalesce(d.window_start,d.created_at)+make_interval(secs => $3::double precision),false)
FROM deliveries d
JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
JOIN monitoring_notices n ON (n.organization,n.event_id)=(d.organization,d.event_id)
JOIN subscriptions s ON (s.organization,s.id)=(m.organization,m.subscription_id)
JOIN records r ON (r.organization,r.id)=(m.organization,m.record_id)
WHERE d.organization=$1 AND d.id=$2 FOR UPDATE OF d`, org, w.DeliveryID, window.Seconds()).Scan(&state, &count, &destination, &eventID, &body, &corpusID, &kind, &enabled, &withdrawn, &later.Corrected, &later.NoLongerMatches, &elapsed)
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
	reason := monitoring.AdmissionReason(kind, enabled, withdrawn, later)
	switch {
	case !configured(org, destination):
		return refuse("destination_unavailable")
	case reason != "":
		return refuse(reason)
	case elapsed:
		if err = exhaust(ctx, tx, org, corpusID, w.DeliveryID, count, "window_elapsed"); err != nil {
			return monitoring.AdmittedAttempt{}, "", err
		}
		return monitoring.AdmittedAttempt{}, "window_elapsed", tx.Commit(ctx)
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
// acknowledgement; exhausted on a permanent failure or once its window has
// elapsed; otherwise pending with its work scheduled at the window-bounded
// retry time. An outcome already recorded (for example unknown after a lease
// expiry) is never overwritten.
func (s DeliveryStore) Record(ctx context.Context, a monitoring.AdmittedAttempt, o monitoring.AttemptOutcome, r monitoring.Retry) error {
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
	// The window is judged on the database clock from the Delivery's window
	// start (its creation, or the re-enable for a notice committed while
	// disabled); every wait, including a receiver's Retry-After, is capped at
	// the window end so one final attempt can happen at the edge.
	var corpusID, state string
	var next time.Time
	err = tx.QueryRow(ctx, `UPDATE deliveries d SET
  state=CASE WHEN $4='acknowledged' THEN 'delivered' WHEN $4='permanent_error' OR now()>=coalesce(d.window_start,d.created_at)+make_interval(secs => $6::double precision) THEN 'exhausted' ELSE 'pending' END,
  exhausted_reason=CASE WHEN $4='acknowledged' THEN '' WHEN $4='permanent_error' THEN 'permanent_error' WHEN now()>=coalesce(d.window_start,d.created_at)+make_interval(secs => $6::double precision) THEN 'window_elapsed' ELSE '' END,
  last_outcome=$4
FROM matches m
WHERE d.organization=$1 AND d.id=$2 AND d.state='delivering' AND d.attempt_count=$3 AND (m.organization,m.id)=(d.organization,d.match_id)
RETURNING m.corpus_id,d.state,LEAST(now()+make_interval(secs => $5::double precision),coalesce(d.window_start,d.created_at)+make_interval(secs => $6::double precision))`,
		org, a.DeliveryID, a.Number, o.Outcome, r.Delay.Seconds(), r.Window.Seconds()).Scan(&corpusID, &state, &next)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if err = deliveryUpdated(ctx, tx, org, corpusID, a.DeliveryID, a.Number, state); err != nil {
		return err
	}
	if state == "pending" {
		_, err = tx.Exec(ctx, `UPDATE delivery_outbox SET available_at=$3,lease_until='-infinity' WHERE organization=$1 AND delivery_id=$2`, org, a.DeliveryID, next)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2`, org, a.DeliveryID)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeliveryBacklog reads the admissible scheduled delivery work: pending or
// delivering Deliveries whose outbox work is due or waiting for a retry.
// Parked work (refused admission) is not stuck work and is excluded, as is a
// notice committed while its Subscription is disabled whose window has not
// started (it is parked on its first claim).
func (s DeliveryStore) DeliveryBacklog(ctx context.Context) (monitoring.DeliveryBacklog, error) {
	var b monitoring.DeliveryBacklog
	var age float64
	err := s.Pool.QueryRow(ctx, `SELECT count(*),coalesce(extract(epoch FROM now()-min(coalesce(d.window_start,d.created_at))),0)::double precision
FROM delivery_outbox o JOIN deliveries d ON (d.organization,d.id)=(o.organization,o.delivery_id)
WHERE o.available_at<'infinity' AND d.state IN ('pending','delivering') AND d.window_start IS DISTINCT FROM 'infinity'`).Scan(&b.Pending, &age)
	b.OldestAge = time.Duration(age * float64(time.Second))
	return b, err
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
