package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

// fanOutPage bounds how many Subscriptions one dispatch step enumerates for a
// trigger event; larger scopes continue on the next step from a checkpoint.
const fanOutPage = 100

// fanOutEvents bounds how many dispatch steps one FanOut call runs per Organization.
const fanOutEvents = 50

// EvaluationStore is the PostgreSQL evaluation dispatch and Match commit.
type EvaluationStore struct {
	ContentStore
	// Page overrides fanOutPage; tests use it to exercise pagination.
	Page int
}

var _ monitoring.EvaluationStore = EvaluationStore{}

func (s EvaluationStore) page() int {
	if s.Page > 0 {
		return s.Page
	}
	return fanOutPage
}

// FanOut initializes checkpoints for Organizations with Subscriptions and
// runs bounded dispatch steps for each.
func (s EvaluationStore) FanOut(ctx context.Context) (int, error) {
	// A checkpoint starts at the first activation boundary, so dispatch never
	// scans history that no Subscription can evaluate.
	if _, err := s.Pool.Exec(ctx, `INSERT INTO monitoring_checkpoints(organization,position)
SELECT v.organization, min(v.activation_position) FROM subscription_versions v
WHERE NOT EXISTS(SELECT 1 FROM monitoring_checkpoints c WHERE c.organization=v.organization)
GROUP BY v.organization
ON CONFLICT DO NOTHING`); err != nil {
		return 0, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT c.organization FROM monitoring_checkpoints c JOIN organization_journals j USING(organization) WHERE c.position < j.last_sequence OR c.subscription_after <> '' ORDER BY c.organization`)
	if err != nil {
		return 0, err
	}
	orgs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	progressed := 0
	for _, org := range orgs {
		for i := 0; i < fanOutEvents; i++ {
			moved, err := s.dispatchStep(ctx, org)
			if err != nil {
				return progressed, err
			}
			if !moved {
				break
			}
			progressed++
		}
	}
	return progressed, nil
}

// dispatchStep turns one trigger event (or one page of its Subscriptions)
// into intents and advances the checkpoint in the same transaction.
func (s EvaluationStore) dispatchStep(ctx context.Context, org string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var position int64
	var after string
	if err = tx.QueryRow(ctx, `SELECT position,subscription_after FROM monitoring_checkpoints WHERE organization=$1 FOR UPDATE`, org).Scan(&position, &after); err != nil {
		return false, err
	}
	// Every position at or below the committed head is committed: writers
	// allocate positions under the journal lock held until commit.
	var head int64
	if err = tx.QueryRow(ctx, `SELECT last_sequence FROM organization_journals WHERE organization=$1`, org).Scan(&head); err != nil {
		return false, err
	}
	var sequence int64
	var corpusID, recordID, versionID string
	err = tx.QueryRow(ctx, `SELECT sequence,corpus_id,resource_id,coalesce(record_version_id,'') FROM change_events
WHERE organization=$1 AND sequence>$2 AND sequence<=$3 AND event_type IN ('record.retrieval_ready','record.enrichment_available')
ORDER BY sequence LIMIT 1`, org, position, head).Scan(&sequence, &corpusID, &recordID, &versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		if position == head {
			return false, nil
		}
		if _, err = tx.Exec(ctx, `UPDATE monitoring_checkpoints SET position=$2,subscription_after='' WHERE organization=$1`, org, head); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	next, nextAfter := sequence, ""
	// Pre-migration trigger events carry no Version and are skipped.
	if versionID != "" {
		rows, err := tx.Query(ctx, `SELECT s.id,v.id FROM subscription_corpora sc
JOIN subscriptions s ON (s.organization,s.id)=(sc.organization,sc.subscription_id)
JOIN subscription_versions v ON (v.organization,v.id)=(s.organization,s.current_version_id)
WHERE sc.organization=$1 AND sc.corpus_id=$2 AND s.enabled AND v.activation_position<$3 AND s.id>$4
ORDER BY s.id LIMIT $5`, org, corpusID, sequence, after, s.page())
		if err != nil {
			return false, err
		}
		type candidate struct{ subscription, version string }
		page, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (candidate, error) {
			var c candidate
			return c, r.Scan(&c.subscription, &c.version)
		})
		if err != nil {
			return false, err
		}
		for _, c := range page {
			if _, err = tx.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, org, c.version, sequence, c.subscription, corpusID, recordID, versionID); err != nil {
				return false, err
			}
		}
		if len(page) == s.page() {
			// The event may have more Subscriptions: keep it and resume after this page.
			next, nextAfter = position, page[len(page)-1].subscription
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE monitoring_checkpoints SET position=$2,subscription_after=$3 WHERE organization=$1`, org, next, nextAfter); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s EvaluationStore) Claim(ctx context.Context, lease time.Duration) (monitoring.Intent, error) {
	var in monitoring.Intent
	err := s.Pool.QueryRow(ctx, `UPDATE evaluation_intents i SET lease_until=now()+make_interval(secs => $1::double precision)
FROM (SELECT organization,subscription_version_id,sequence FROM evaluation_intents
  WHERE state='pending' AND available_at<=now() AND lease_until<now()
  ORDER BY available_at,sequence LIMIT 1 FOR UPDATE SKIP LOCKED) due
WHERE (i.organization,i.subscription_version_id,i.sequence)=(due.organization,due.subscription_version_id,due.sequence)
RETURNING i.organization,i.subscription_id,i.subscription_version_id,i.sequence,i.corpus_id,i.record_id,i.record_version_id,i.attempts`, lease.Seconds()).Scan(
		&in.Organization, &in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.CorpusID, &in.RecordID, &in.VersionID, &in.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, monitoring.ErrNoWork
	}
	return in, err
}

func (s EvaluationStore) Target(ctx context.Context, in monitoring.Intent) (monitoring.Target, error) {
	var t monitoring.Target
	v := &t.Subscription
	var evaluator, definition []byte
	err := s.Pool.QueryRow(ctx, `SELECT s.enabled AND s.current_version_id=v.id,v.subscription_id,v.id,v.saved_query_id,v.saved_query_version_id,v.evaluator,v.destination_id,v.activation_position,q.corpus_ids,q.definition,
  EXISTS(SELECT 1 FROM segments sg JOIN embedding_coverage ec ON (ec.organization,ec.segment_id)=(sg.organization,sg.id) WHERE sg.organization=$1 AND sg.version_id=$3 AND ec.generation_id=`+routedGenerationSQL("$1", "$4")+`)
FROM subscription_versions v
JOIN subscriptions s ON (s.organization,s.id)=(v.organization,v.subscription_id)
JOIN saved_query_versions q ON (q.organization,q.id)=(v.organization,v.saved_query_version_id)
WHERE v.organization=$1 AND v.id=$2`, in.Organization, in.SubscriptionVersionID, in.VersionID, in.CorpusID).Scan(
		&t.Enabled, &v.SubscriptionID, &v.VersionID, &v.SavedQueryID, &v.SavedQueryVersionID, &evaluator, &v.DestinationID, &v.ActivationPosition, &v.CorpusIDs, &definition, &t.Enriched)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, monitoring.ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if err = unmarshalNumbers(evaluator, &v.Evaluator); err != nil {
		return t, err
	}
	return t, unmarshalNumbers(definition, &t.Definition)
}

func (s EvaluationStore) Complete(ctx context.Context, in monitoring.Intent, outcome string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE evaluation_intents SET state='done',outcome=$4,error_code='',lease_until='-infinity' WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3 AND state='pending'`, in.Organization, in.SubscriptionVersionID, in.Sequence, outcome)
	return err
}

func (s EvaluationStore) Retry(ctx context.Context, in monitoring.Intent, code string, delay time.Duration) error {
	_, err := s.Pool.Exec(ctx, `UPDATE evaluation_intents SET attempts=attempts+1,error_code=$4,available_at=now()+make_interval(secs => $5::double precision),lease_until='-infinity' WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3 AND state='pending'`, in.Organization, in.SubscriptionVersionID, in.Sequence, code, delay.Seconds())
	return err
}

func (s EvaluationStore) Backlog(ctx context.Context) (monitoring.Backlog, error) {
	var b monitoring.Backlog
	err := s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE error_code<>'') FROM evaluation_intents WHERE state='pending'`).Scan(&b.Pending, &b.Erroring)
	return b, err
}

// CommitMatch is the Monitoring atomic boundary. Under the Organization
// journal lock, which disable and withdrawal also take, it rechecks the
// enabled pinned Subscription Version, the Record Version's currentness and
// canonical eligibility and the Subscription's Corpus scope, then creates the
// unique Match with its Delivery, notice, public event and outbox work.
func (s EvaluationStore) CommitMatch(ctx context.Context, in monitoring.Intent, evidence monitoring.MatchEvidence) (string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, in.Organization); err != nil {
		return "", err
	}
	outcome, err := s.commitMatch(ctx, tx, in, evidence)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE evaluation_intents SET state='done',outcome=$4,error_code='',lease_until='-infinity' WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3 AND state='pending'`, in.Organization, in.SubscriptionVersionID, in.Sequence, outcome); err != nil {
		return "", err
	}
	return outcome, tx.Commit(ctx)
}

func (s EvaluationStore) commitMatch(ctx context.Context, tx pgx.Tx, in monitoring.Intent, evidence monitoring.MatchEvidence) (string, error) {
	org := in.Organization
	var enabled bool
	var current, queryID, queryVersionID, destination string
	err := tx.QueryRow(ctx, `SELECT s.enabled,s.current_version_id,v.saved_query_id,v.saved_query_version_id,v.destination_id FROM subscriptions s JOIN subscription_versions v ON (v.organization,v.id)=(s.organization,$3::text) WHERE s.organization=$1 AND s.id=$2`, org, in.SubscriptionID, in.SubscriptionVersionID).Scan(&enabled, &current, &queryID, &queryVersionID, &destination)
	if errors.Is(err, pgx.ErrNoRows) {
		return monitoring.OutcomeIneligible, nil
	}
	if err != nil {
		return "", err
	}
	if !enabled || current != in.SubscriptionVersionID {
		return monitoring.OutcomeSubscriptionDisabled, nil
	}
	var corpusID string
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT r.corpus_id, r.current_version_id IS NOT DISTINCT FROM v.id AND `+eligibleVersionSQL+`
  AND EXISTS(SELECT 1 FROM subscription_corpora sc WHERE sc.organization=r.organization AND sc.corpus_id=r.corpus_id AND sc.subscription_id=$4)
FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
WHERE v.organization=$1 AND v.id=$2 AND r.id=$3`, org, in.VersionID, in.RecordID, in.SubscriptionID).Scan(&corpusID, &eligible)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !eligible) {
		return monitoring.OutcomeIneligible, nil
	}
	if err != nil {
		return "", err
	}
	matchID := content.StableID("match", org, in.SubscriptionVersionID, in.VersionID)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM matches WHERE organization=$1 AND subscription_version_id=$2 AND record_version_id=$3)`, org, in.SubscriptionVersionID, in.VersionID).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return monitoring.OutcomeDuplicate, nil
	}
	const kind = "match.created"
	deliveryID := content.StableID("delivery", org, matchID, destination, kind)
	event := eventInput{Organization: org, CorpusID: corpusID, Kind: kind, Resource: "match", ResourceID: matchID, MutationID: matchID}
	// The notice time is the transaction time, which is also the event's occurred_at.
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return "", err
	}
	refs := monitoring.NoticeReferences{MatchID: matchID, RecordID: in.RecordID, RecordVersionID: in.VersionID, SubscriptionID: in.SubscriptionID, SubscriptionVersionID: in.SubscriptionVersionID, DeliveryID: deliveryID}
	body, err := json.Marshal(monitoring.Notice{EventID: eventID(event), Type: kind, SchemaVersion: "1", OccurredAt: now.UTC(), References: refs})
	if err != nil {
		return "", err
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	position, err := appendEventAt(ctx, tx, event)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO matches(organization,id,subscription_id,subscription_version_id,saved_query_id,saved_query_version_id,corpus_id,record_id,record_version_id,evidence,position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		org, matchID, in.SubscriptionID, in.SubscriptionVersionID, queryID, queryVersionID, corpusID, in.RecordID, in.VersionID, evidenceJSON, position); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deliveries(organization,id,match_id,destination_id,event_kind,event_id) VALUES($1,$2,$3,$4,$5,$6)`, org, deliveryID, matchID, destination, kind, eventID(event)); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO monitoring_notices(organization,event_id,kind,match_id,record_id,record_version_id,subscription_id,subscription_version_id,delivery_id,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		org, eventID(event), kind, matchID, in.RecordID, in.VersionID, in.SubscriptionID, in.SubscriptionVersionID, deliveryID, body); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO delivery_outbox(organization,delivery_id) VALUES($1,$2)`, org, deliveryID); err != nil {
		return "", err
	}
	return monitoring.OutcomeMatched, nil
}

const matchColumns = `id,subscription_id,subscription_version_id,saved_query_id,saved_query_version_id,record_id,record_version_id,coalesce(previous_match_id,''),evidence,position`

func scanMatch(row pgx.Row) (monitoring.Match, error) {
	var m monitoring.Match
	var evidence []byte
	if err := row.Scan(&m.ID, &m.SubscriptionID, &m.SubscriptionVersionID, &m.SavedQueryID, &m.SavedQueryVersionID, &m.RecordID, &m.RecordVersionID, &m.PreviousMatchID, &evidence, &m.Position); err != nil {
		return m, err
	}
	return m, unmarshalNumbers(evidence, &m.Evidence)
}

func (s ContentStore) Match(ctx context.Context, org, id string) (monitoring.Match, error) {
	m, err := scanMatch(s.Pool.QueryRow(ctx, `SELECT `+matchColumns+` FROM matches WHERE organization=$1 AND id=$2`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, monitoring.ErrNotFound
	}
	return m, err
}

func (s ContentStore) Matches(ctx context.Context, org, subscriptionID string, after int64, limit int) ([]monitoring.Match, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+matchColumns+` FROM matches WHERE organization=$1 AND subscription_id=$2 AND position>$3 ORDER BY position LIMIT $4`, org, subscriptionID, after, limit)
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
func (s ContentStore) Delivery(ctx context.Context, org, id string) (monitoring.Delivery, error) {
	var d monitoring.Delivery
	var enabled, withdrawn bool
	var next *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT d.id,d.match_id,m.subscription_id,d.destination_id,d.state,d.attempt_count,n.body,s.enabled,
  r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),
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
WHERE d.organization=$1 AND d.id=$2`, org, id).Scan(&d.ID, &d.MatchID, &d.SubscriptionID, &d.DestinationID, &d.State, &d.AttemptCount, &d.Event, &enabled, &withdrawn,
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
	case !enabled:
		d.Admission = monitoring.Admission{Reason: "subscription_disabled"}
	case withdrawn:
		d.Admission = monitoring.Admission{Reason: "record_withdrawn"}
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

var _ monitoring.MatchStore = ContentStore{}
