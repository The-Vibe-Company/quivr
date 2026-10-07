package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

const maxDeliveryBatch = 32

var _ monitoring.DeliveryBatchStore = DeliveryStore{}

// ClaimDeliveries starts with the oldest unlocked due row across organizations,
// then leases a bounded group in that organization. Stable arrival order keeps
// a busy organization from taking priority over older work elsewhere.
func (s DeliveryStore) ClaimDeliveries(ctx context.Context, lease time.Duration, limit int) ([]monitoring.DeliveryWork, error) {
	limit = max(1, min(limit, maxDeliveryBatch))
	rows, err := s.Pool.Query(ctx, `WITH oldest AS MATERIALIZED (
  SELECT organization FROM delivery_outbox
  WHERE available_at<=now() AND lease_until<now() AND ($2='' OR organization=$2)
  ORDER BY available_at,organization,delivery_id LIMIT 1 FOR UPDATE SKIP LOCKED),
due AS MATERIALIZED (
  SELECT o.organization,o.delivery_id FROM delivery_outbox o JOIN oldest USING(organization)
  WHERE o.available_at<=now() AND o.lease_until<now()
  ORDER BY o.available_at,o.delivery_id LIMIT $3 FOR UPDATE OF o SKIP LOCKED),
claimed AS (
  UPDATE delivery_outbox o SET lease_until=now()+make_interval(secs => $1::double precision)
  FROM due WHERE (o.organization,o.delivery_id)=(due.organization,due.delivery_id)
  RETURNING o.organization,o.delivery_id,o.lease_until,o.available_at,o.trace_context)
SELECT organization,delivery_id,lease_until,trace_context FROM claimed ORDER BY available_at,delivery_id`, lease.Seconds(), s.Organization, limit)
	if err != nil {
		return nil, err
	}
	works, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitoring.DeliveryWork, error) {
		var w monitoring.DeliveryWork
		err := row.Scan(&w.Organization, &w.DeliveryID, &w.Lease, &w.TraceContext)
		return w, err
	})
	if err == nil && len(works) == 0 {
		return nil, monitoring.ErrNoWork
	}
	return works, err
}

// deliveryGroup reports whether one ordered group can use a common write path.
// Repeated IDs and cross-organization calls retain per-item semantics.
func deliveryGroup(orgs, ids []string) bool {
	if len(ids) == 0 || len(ids) > maxDeliveryBatch {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for i, id := range ids {
		if orgs[i] != orgs[0] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// deliveryGuardRows sends the journal fence and guarded bulk read together,
// using a separate statement snapshot after the journal lock is acquired.
func deliveryGuardRows[T any](ctx context.Context, tx pgx.Tx, org, query string, args []any, collect func(pgx.CollectableRow) (T, error)) ([]T, error) {
	batch := journalBatch(org)
	batch.Queue(query, args...)
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	for range batch.Len() - 1 {
		if _, err := results.Exec(); err != nil {
			return nil, err
		}
	}
	rows, err := results.Query()
	if err != nil {
		return nil, err
	}
	guards, err := pgx.CollectRows(rows, collect)
	if err != nil {
		return nil, err
	}
	return guards, results.Close()
}

type deliveryAdmissionGuard struct {
	ordinal                                     int
	held                                        bool
	state, destination, eventID, corpusID, kind string
	count                                       int
	body                                        []byte
	enabled, deleted, withdrawn, elapsed        bool
	later                                       monitoring.Later
}

var deliveryAdmissionBatchSQL = `WITH requested AS (
  SELECT * FROM unnest($2::text[],$3::timestamptz[]) WITH ORDINALITY AS x(delivery_id,lease,ordinal)),
locked_work AS MATERIALIZED (
  SELECT o.delivery_id,o.lease_until,o.available_at FROM delivery_outbox o
  WHERE o.organization=$1 AND o.delivery_id=ANY($2) FOR UPDATE OF o),
locked_deliveries AS MATERIALIZED (
  SELECT d.* FROM deliveries d WHERE d.organization=$1 AND d.id=ANY($2) FOR UPDATE OF d)
SELECT x.ordinal,coalesce(o.lease_until=x.lease AND o.lease_until>now(),false),
  d.state,d.attempt_count,d.destination_id,d.event_id,n.body,m.corpus_id,n.kind,s.enabled,s.deleted,
  ` + recordGoneSQL + `,` + laterNoticesSQL + `,
  coalesce(o.available_at>coalesce(d.window_start,d.created_at)+make_interval(secs => $4::double precision),false)
FROM requested x JOIN locked_deliveries d ON d.id=x.delivery_id
LEFT JOIN locked_work o ON o.delivery_id=x.delivery_id
JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
JOIN monitoring_notices n ON (n.organization,n.event_id)=(d.organization,d.event_id)
JOIN subscriptions s ON (s.organization,s.id)=(m.organization,m.subscription_id)
JOIN records r ON (r.organization,r.id)=(m.organization,m.record_id)
ORDER BY x.ordinal`

// AdmitDeliveries commits fresh admissible first attempts together. Every other
// case drops the read transaction before entering the established admission
// path, which owns refusals, window exhaustion and unknown-attempt recovery.
func (s DeliveryStore) AdmitDeliveries(ctx context.Context, works []monitoring.DeliveryWork, window time.Duration, configured func(string, string) bool) ([]monitoring.AdmittedAttempt, []string, []error) {
	attempts, refused, errs := make([]monitoring.AdmittedAttempt, len(works)), make([]string, len(works)), make([]error, len(works))
	if len(works) == 0 {
		return attempts, refused, errs
	}
	orgs, ids, leases := make([]string, len(works)), make([]string, len(works)), make([]time.Time, len(works))
	for i, w := range works {
		orgs[i], ids[i], leases[i] = w.Organization, w.DeliveryID, w.Lease
	}
	if deliveryGroup(orgs, ids) {
		common, admitted, err := s.admitDeliveryGroup(ctx, works, window, configured, ids, leases)
		if err != nil {
			for i := range errs {
				errs[i] = err
			}
			return attempts, refused, errs
		}
		if common {
			return admitted, refused, errs
		}
	}
	for i, w := range works {
		attempts[i], refused[i], errs[i] = s.admit(telemetry.Restore(ctx, w.TraceContext), w, window, configured)
	}
	return attempts, refused, errs
}

func (s DeliveryStore) admitDeliveryGroup(ctx context.Context, works []monitoring.DeliveryWork, window time.Duration, configured func(string, string) bool, ids []string, leases []time.Time) (bool, []monitoring.AdmittedAttempt, error) {
	var result0 bool
	var result1 []monitoring.AdmittedAttempt
	err := retryJournalWrite(ctx, "admitDeliveryGroup", func(ctx context.Context) error {
		var err error
		result0, result1, err = s.admitDeliveryGroupAttempt(ctx, works, window, configured, ids, leases)
		return err
	})
	return result0, result1, err
}

func (s DeliveryStore) admitDeliveryGroupAttempt(ctx context.Context, works []monitoring.DeliveryWork, window time.Duration, configured func(string, string) bool, ids []string, leases []time.Time) (bool, []monitoring.AdmittedAttempt, error) {
	org := works[0].Organization
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback(ctx)
	guards, err := deliveryGuardRows(ctx, tx, org, deliveryAdmissionBatchSQL, []any{org, ids, leases, window.Seconds()}, func(row pgx.CollectableRow) (deliveryAdmissionGuard, error) {
		var g deliveryAdmissionGuard
		err := row.Scan(&g.ordinal, &g.held, &g.state, &g.count, &g.destination, &g.eventID, &g.body, &g.corpusID, &g.kind, &g.enabled, &g.deleted, &g.withdrawn, &g.later.Corrected, &g.later.NoLongerMatches, &g.elapsed)
		return g, err
	})
	if err != nil {
		return false, nil, err
	}
	if len(guards) != len(works) {
		return false, nil, nil
	}
	for i, g := range guards {
		if g.ordinal != i+1 || !g.held || g.state != "pending" || g.count != 0 || g.elapsed || monitoring.AdmissionReason(g.kind, g.enabled, g.deleted, g.withdrawn, g.later) != "" {
			return false, nil, nil
		}
	}
	for _, g := range guards {
		if !configured(org, g.destination) {
			return false, nil, nil
		}
	}
	attempts := make([]monitoring.AdmittedAttempt, len(works))
	attemptIDs, eventIDs, corpusIDs := make([]string, len(works)), make([]string, len(works)), make([]string, len(works))
	writes := &pgx.Batch{}
	for i, g := range guards {
		a := monitoring.AdmittedAttempt{Organization: org, DeliveryID: works[i].DeliveryID, Number: 1, EventID: g.eventID, DestinationID: g.destination, Body: g.body, TraceContext: works[i].TraceContext}
		a.AttemptID = attemptID(org, a.DeliveryID, a.Number)
		attempts[i] = a
		attemptIDs[i], eventIDs[i], corpusIDs[i] = a.AttemptID, eventID(deliveryStateEvent(org, g.corpusID, a.DeliveryID, a.Number, "delivering")), g.corpusID
	}
	writes.Queue(`INSERT INTO delivery_attempts(organization,id,delivery_id,number)
SELECT $1,x.attempt_id,x.delivery_id,1 FROM unnest($2::text[],$3::text[]) AS x(attempt_id,delivery_id)`, org, attemptIDs, ids)
	writes.Queue(`UPDATE deliveries SET state='delivering',attempt_count=1 WHERE organization=$1 AND id=ANY($2::text[])`, org, ids)
	writes.Queue(deliveryEventsBatchSQL, org, eventIDs, corpusIDs, ids, attemptTraceContexts(attempts))
	writes.Queue(acknowledgeQueueJournalSQL, org, len(eventIDs))
	if err = tx.SendBatch(ctx, writes).Close(); err != nil {
		return true, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, nil, err
	}
	return true, attempts, nil
}

func deliveryStateEvent(org, corpusID, deliveryID string, number int, state string) eventInput {
	return eventInput{Organization: org, CorpusID: corpusID, Kind: "delivery.updated", Resource: "delivery", ResourceID: deliveryID, MutationID: fmt.Sprint(deliveryID, ":", number, ":", state)}
}

// deliveryEventsBatchSQL allocates one contiguous range under the already held
// journal lock. Ordinality assigns each event its original input position;
// the committed head is visible only with the whole range and its other facts.
const deliveryEventsBatchSQL = `WITH inputs AS (
  SELECT * FROM unnest($2::text[],$3::text[],$4::text[],$5::text[])
    WITH ORDINALITY AS x(event_id,corpus_id,delivery_id,trace_context,ordinal)),
position AS (
  UPDATE organization_journals SET last_sequence=last_sequence+cardinality($2::text[])
  WHERE organization=$1 RETURNING last_sequence-cardinality($2::text[]) AS base)
INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id,trace_context)
SELECT $1,position.base+inputs.ordinal,inputs.event_id,inputs.corpus_id,'delivery.updated','delivery',inputs.delivery_id,NULL,inputs.trace_context
FROM inputs CROSS JOIN position ORDER BY inputs.ordinal`

type deliveryRecordGuard struct {
	ordinal    int
	state      string
	count      int
	corpusID   string
	hasOutcome bool
}

const deliveryRecordBatchSQL = `WITH requested AS (
  SELECT * FROM unnest($2::text[],$3::text[],$4::integer[]) WITH ORDINALITY AS x(delivery_id,attempt_id,number,ordinal)),
locked_work AS MATERIALIZED (
  SELECT o.delivery_id FROM delivery_outbox o WHERE o.organization=$1 AND o.delivery_id=ANY($2) FOR UPDATE OF o),
locked_deliveries AS MATERIALIZED (
  SELECT d.* FROM deliveries d WHERE d.organization=$1 AND d.id=ANY($2) FOR UPDATE OF d)
SELECT x.ordinal,d.state,d.attempt_count,m.corpus_id,
  EXISTS(SELECT 1 FROM delivery_attempt_outcomes o WHERE o.organization=$1 AND o.attempt_id=x.attempt_id)
FROM requested x JOIN locked_deliveries d ON d.id=x.delivery_id
LEFT JOIN locked_work w ON w.delivery_id=x.delivery_id
JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
JOIN delivery_attempts a ON a.organization=$1 AND a.id=x.attempt_id AND a.delivery_id=x.delivery_id AND a.number=x.number
ORDER BY x.ordinal`

// RecordDeliveries groups unrecorded acknowledgements only. Other outcomes and
// stale/replayed attempts retain the existing append-only per-item transition.
// Ordered errors let callers observe successful siblings after fallback errors.
func (s DeliveryStore) RecordDeliveries(ctx context.Context, attempts []monitoring.AdmittedAttempt, outcomes []monitoring.AttemptOutcome, retries []monitoring.Retry) []error {
	errs := make([]error, len(attempts))
	if len(attempts) != len(outcomes) || len(attempts) != len(retries) {
		for i := range errs {
			errs[i] = errors.New("delivery outcomes and retries must follow attempts")
		}
		return errs
	}
	if len(attempts) == 0 {
		return errs
	}
	orgs, ids, attemptIDs, numbers := make([]string, len(attempts)), make([]string, len(attempts)), make([]string, len(attempts)), make([]int, len(attempts))
	acknowledged := true
	for i, a := range attempts {
		orgs[i], ids[i], attemptIDs[i], numbers[i] = a.Organization, a.DeliveryID, a.AttemptID, a.Number
		acknowledged = acknowledged && outcomes[i].Outcome == monitoring.AttemptAcknowledged
	}
	if acknowledged && deliveryGroup(orgs, ids) {
		common, err := s.recordDeliveryGroup(ctx, attempts, outcomes, ids, attemptIDs, numbers)
		if err != nil {
			for i := range errs {
				errs[i] = err
			}
			return errs
		}
		if common {
			return errs
		}
	}
	for i, a := range attempts {
		errs[i] = s.record(telemetry.Restore(ctx, a.TraceContext), a, outcomes[i], retries[i])
	}
	return errs
}

func (s DeliveryStore) recordDeliveryGroup(ctx context.Context, attempts []monitoring.AdmittedAttempt, outcomes []monitoring.AttemptOutcome, ids, attemptIDs []string, numbers []int) (bool, error) {
	var result0 bool
	err := retryJournalWrite(ctx, "recordDeliveryGroup", func(ctx context.Context) error {
		var err error
		result0, err = s.recordDeliveryGroupAttempt(ctx, attempts, outcomes, ids, attemptIDs, numbers)
		return err
	})
	return result0, err
}

func (s DeliveryStore) recordDeliveryGroupAttempt(ctx context.Context, attempts []monitoring.AdmittedAttempt, outcomes []monitoring.AttemptOutcome, ids, attemptIDs []string, numbers []int) (bool, error) {
	org := attempts[0].Organization
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	guards, err := deliveryGuardRows(ctx, tx, org, deliveryRecordBatchSQL, []any{org, ids, attemptIDs, numbers}, func(row pgx.CollectableRow) (deliveryRecordGuard, error) {
		var g deliveryRecordGuard
		err := row.Scan(&g.ordinal, &g.state, &g.count, &g.corpusID, &g.hasOutcome)
		return g, err
	})
	if err != nil {
		return false, err
	}
	if len(guards) != len(attempts) {
		return false, nil
	}
	for i, g := range guards {
		if g.ordinal != i+1 || g.state != "delivering" || g.count != attempts[i].Number || g.hasOutcome {
			return false, nil
		}
	}
	writes := &pgx.Batch{}
	statuses := make([]int, len(attempts))
	codes, messages, eventIDs, corpusIDs := make([]string, len(attempts)), make([]string, len(attempts)), make([]string, len(attempts)), make([]string, len(attempts))
	for i, g := range guards {
		a, o := attempts[i], outcomes[i]
		statuses[i], codes[i], messages[i] = o.HTTPStatus, o.ErrorCode, o.ErrorMessage
		eventIDs[i], corpusIDs[i] = eventID(deliveryStateEvent(org, g.corpusID, a.DeliveryID, a.Number, "delivered")), g.corpusID
	}
	writes.Queue(`INSERT INTO delivery_attempt_outcomes(organization,attempt_id,outcome,http_status,error_code,error_message)
SELECT $1,x.attempt_id,'acknowledged',NULLIF(x.http_status,0),x.error_code,left(x.error_message,200)
FROM unnest($2::text[],$3::integer[],$4::text[],$5::text[]) AS x(attempt_id,http_status,error_code,error_message)`, org, attemptIDs, statuses, codes, messages)
	writes.Queue(`UPDATE deliveries SET state='delivered',exhausted_reason='',last_outcome='acknowledged' WHERE organization=$1 AND id=ANY($2::text[])`, org, ids)
	writes.Queue(deliveryEventsBatchSQL, org, eventIDs, corpusIDs, ids, attemptTraceContexts(attempts))
	writes.Queue(acknowledgeQueueJournalSQL, org, len(eventIDs))
	writes.Queue(`DELETE FROM delivery_outbox WHERE organization=$1 AND delivery_id=ANY($2::text[])`, org, ids)
	if err = tx.SendBatch(ctx, writes).Close(); err != nil {
		return true, err
	}
	return true, tx.Commit(ctx)
}

func attemptTraceContexts(attempts []monitoring.AdmittedAttempt) []string {
	out := make([]string, len(attempts))
	for i, a := range attempts {
		out[i] = a.TraceContext
	}
	return out
}
