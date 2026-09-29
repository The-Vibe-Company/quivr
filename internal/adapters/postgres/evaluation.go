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
	var corpusID, recordID, versionID, eventType string
	err = tx.QueryRow(ctx, `SELECT sequence,corpus_id,resource_id,coalesce(record_version_id,''),event_type FROM change_events
WHERE organization=$1 AND sequence>$2 AND sequence<=$3 AND event_type IN ('record.retrieval_ready','record.enrichment_available','record.withdrawn')
ORDER BY sequence LIMIT 1`, org, position, head).Scan(&sequence, &corpusID, &recordID, &versionID, &eventType)
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
	if versionID != "" || eventType == "record.withdrawn" {
		kind := monitoring.IntentEvaluation
		var rows pgx.Rows
		if eventType == "record.withdrawn" {
			// A withdrawal concerns the Subscriptions already alerted about the
			// Record, whatever their activation or enabled state: a disabled
			// one gets its notice committed and delivered after re-enable. No
			// Match commits after the Tombstone, so every relevant Match
			// precedes this event.
			kind = monitoring.IntentWithdrawal
			rows, err = tx.Query(ctx, `SELECT s.id,s.current_version_id,latest.record_version_id
FROM (SELECT DISTINCT subscription_id FROM matches WHERE organization=$1 AND record_id=$2 AND position<$3 AND subscription_id>$4) alerted
JOIN subscriptions s ON s.organization=$1 AND s.id=alerted.subscription_id
JOIN LATERAL (SELECT m.record_version_id FROM matches m WHERE m.organization=$1 AND m.record_id=$2 AND m.subscription_id=s.id ORDER BY m.position DESC LIMIT 1) latest ON true
ORDER BY s.id LIMIT $5`, org, recordID, sequence, after, s.page())
		} else {
			// A trigger is judged by the Subscription Version effective at
			// its position: the latest one activated before it. An edit
			// therefore applies from its commit on, and a checkpoint lagging
			// behind it still judges earlier changes with the earlier
			// Version. A re-enabled Subscription evaluates only triggers
			// after its re-enable: the pause is never backfilled.
			rows, err = tx.Query(ctx, `SELECT s.id,v.id,$6::text FROM subscription_corpora sc
JOIN subscriptions s ON (s.organization,s.id)=(sc.organization,sc.subscription_id)
JOIN LATERAL (SELECT e.id,e.saved_query_version_id FROM subscription_versions e
  WHERE e.organization=s.organization AND e.subscription_id=s.id AND e.activation_position<$3
  ORDER BY e.activation_position DESC LIMIT 1) v ON true
JOIN saved_query_versions q ON (q.organization,q.id)=(s.organization,v.saved_query_version_id)
WHERE sc.organization=$1 AND sc.corpus_id=$2 AND s.enabled AND $2=ANY(q.corpus_ids) AND coalesce(s.enabled_position,0)<$3 AND s.id>$4
ORDER BY s.id LIMIT $5`, org, corpusID, sequence, after, s.page(), versionID)
		}
		if err != nil {
			return false, err
		}
		type candidate struct{ subscription, version, recordVersion string }
		page, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (candidate, error) {
			var c candidate
			return c, r.Scan(&c.subscription, &c.version, &c.recordVersion)
		})
		if err != nil {
			return false, err
		}
		for _, c := range page {
			if _, err = tx.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,kind) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, org, c.version, sequence, c.subscription, corpusID, recordID, c.recordVersion, kind); err != nil {
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
RETURNING i.kind,i.organization,i.subscription_id,i.subscription_version_id,i.sequence,i.corpus_id,i.record_id,i.record_version_id,i.attempts`, lease.Seconds()).Scan(
		&in.Kind, &in.Organization, &in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.CorpusID, &in.RecordID, &in.VersionID, &in.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, monitoring.ErrNoWork
	}
	return in, err
}

func (s EvaluationStore) Target(ctx context.Context, in monitoring.Intent) (monitoring.Target, error) {
	var t monitoring.Target
	v := &t.Subscription
	var evaluator, definition []byte
	err := s.Pool.QueryRow(ctx, `SELECT s.enabled,`+supersededSQL("v", "$5")+`,v.subscription_id,v.id,v.saved_query_id,v.saved_query_version_id,v.evaluator,v.destination_id,v.activation_position,q.corpus_ids,q.definition,
  EXISTS(SELECT 1 FROM segments sg JOIN embedding_coverage ec ON (ec.organization,ec.segment_id)=(sg.organization,sg.id) WHERE sg.organization=$1 AND sg.version_id=$3 AND ec.generation_id=`+routedGenerationSQL("$1", "$4")+`)
FROM subscription_versions v
JOIN subscriptions s ON (s.organization,s.id)=(v.organization,v.subscription_id)
JOIN saved_query_versions q ON (q.organization,q.id)=(v.organization,v.saved_query_version_id)
WHERE v.organization=$1 AND v.id=$2`, in.Organization, in.SubscriptionVersionID, in.VersionID, in.CorpusID, in.Sequence).Scan(
		&t.Enabled, &t.Superseded, &v.SubscriptionID, &v.VersionID, &v.SavedQueryID, &v.SavedQueryVersionID, &evaluator, &v.DestinationID, &v.ActivationPosition, &v.CorpusIDs, &definition, &t.Enriched)
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
// unique Match with its Delivery, notice, public event and outbox work. A
// Match on a correction of an already matched Record links its predecessor
// and is announced as match.corrected.
func (s EvaluationStore) CommitMatch(ctx context.Context, in monitoring.Intent, evidence monitoring.MatchEvidence) (string, error) {
	return s.commit(ctx, in, func(tx pgx.Tx) (string, error) { return commitMatch(ctx, tx, in, evidence) })
}

// CommitNoMatch records a negative decision. On an eligible correction of a
// Record with a prior positive Match it commits one match.no_longer_matches
// notice referencing that Match and the non-matching Version, never a Match.
func (s EvaluationStore) CommitNoMatch(ctx context.Context, in monitoring.Intent) (string, error) {
	// Most negative decisions concern Records never matched on another
	// Version: complete them without the journal lock. A Match on another
	// Version commits only while that Version is current, so before this
	// Version's trigger was dispatched; none can appear after this read.
	var alerted bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM matches WHERE organization=$1 AND record_id=$2 AND subscription_id=$3 AND record_version_id<>$4)`, in.Organization, in.RecordID, in.SubscriptionID, in.VersionID).Scan(&alerted); err != nil {
		return "", err
	}
	if !alerted {
		return monitoring.OutcomeNoMatch, s.Complete(ctx, in, monitoring.OutcomeNoMatch)
	}
	return s.commit(ctx, in, func(tx pgx.Tx) (string, error) { return commitNoMatch(ctx, tx, in) })
}

// CommitWithdrawal consumes a withdrawal intent. It does not run the positive
// no-Tombstone guard: it requires the Tombstone and the Record's Corpus in the
// Subscription's scope, then commits the match.withdrawn notice for the latest
// positive Match once. A disabled Subscription gets it too: committing a
// notice is not an attempt, and admission parks it until a re-enable.
func (s EvaluationStore) CommitWithdrawal(ctx context.Context, in monitoring.Intent) (string, error) {
	return s.commit(ctx, in, func(tx pgx.Tx) (string, error) { return commitWithdrawal(ctx, tx, in) })
}

// commit runs one decision under the journal lock and completes its intent in
// the same transaction.
func (s EvaluationStore) commit(ctx context.Context, in monitoring.Intent, decide func(pgx.Tx) (string, error)) (string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, in.Organization); err != nil {
		return "", err
	}
	outcome, err := decide(tx)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE evaluation_intents SET state='done',outcome=$4,error_code='',lease_until='-infinity' WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3 AND state='pending'`, in.Organization, in.SubscriptionVersionID, in.Sequence, outcome); err != nil {
		return "", err
	}
	return outcome, tx.Commit(ctx)
}

type subscriptionPin struct{ queryID, queryVersionID, destination string }

// supersededSQL reports whether a later Version of the Subscription of the
// subscription_versions row v was activated before the trigger position.
func supersededSQL(v, position string) string {
	return `EXISTS(SELECT 1 FROM subscription_versions later WHERE later.organization=` + v + `.organization AND later.subscription_id=` + v + `.subscription_id
  AND later.activation_position>` + v + `.activation_position AND later.activation_position<` + position + `)`
}

// guardEvaluation is the core eligibility guard of an evaluated Version: the
// Subscription is enabled (and the trigger is later than its latest
// re-enable), the pinned Subscription Version is the one effective at the
// trigger's position, the Record Version is the Record's current eligible
// Version and its Corpus is in that Subscription Version's scope. A refusal is
// returned as an outcome.
func guardEvaluation(ctx context.Context, tx pgx.Tx, in monitoring.Intent) (pinned subscriptionPin, corpusID, refused string, err error) {
	org := in.Organization
	var enabled, superseded bool
	var resumed int64
	var scope []string
	err = tx.QueryRow(ctx, `SELECT s.enabled,coalesce(s.enabled_position,0),`+supersededSQL("v", "$4")+`,v.saved_query_id,v.saved_query_version_id,v.destination_id,q.corpus_ids
FROM subscriptions s
JOIN subscription_versions v ON (v.organization,v.id)=(s.organization,$3::text) AND v.subscription_id=s.id
JOIN saved_query_versions q ON (q.organization,q.id)=(v.organization,v.saved_query_version_id)
WHERE s.organization=$1 AND s.id=$2`, org, in.SubscriptionID, in.SubscriptionVersionID, in.Sequence).Scan(&enabled, &resumed, &superseded, &pinned.queryID, &pinned.queryVersionID, &pinned.destination, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return pinned, "", monitoring.OutcomeIneligible, nil
	}
	if err != nil {
		return pinned, "", "", err
	}
	if !enabled || in.Sequence <= resumed {
		return pinned, "", monitoring.OutcomeSubscriptionDisabled, nil
	}
	if superseded {
		return pinned, "", monitoring.OutcomeVersionSuperseded, nil
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT r.corpus_id, r.current_version_id IS NOT DISTINCT FROM v.id AND `+eligibleVersionSQL+` AND r.corpus_id=ANY($4::text[])
FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
WHERE v.organization=$1 AND v.id=$2 AND r.id=$3`, org, in.VersionID, in.RecordID, scope).Scan(&corpusID, &eligible)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !eligible) {
		return pinned, "", monitoring.OutcomeIneligible, nil
	}
	return pinned, corpusID, "", err
}

// priorMatch is the latest positive Match of a Subscription for a Record,
// with the destination pinned by the Subscription Version that produced it.
type priorMatch struct{ id, versionID, subscriptionVersionID, destination string }

// latestMatch reads the latest Match of a Subscription for a Record on any
// Record Version other than exceptVersion.
func latestMatch(ctx context.Context, tx pgx.Tx, org, subscriptionID, recordID, exceptVersion string) (priorMatch, bool, error) {
	var p priorMatch
	err := tx.QueryRow(ctx, `SELECT m.id,m.record_version_id,m.subscription_version_id,v.destination_id FROM matches m
JOIN subscription_versions v ON (v.organization,v.id)=(m.organization,m.subscription_version_id)
WHERE m.organization=$1 AND m.record_id=$3 AND m.subscription_id=$2 AND m.record_version_id<>$4
ORDER BY m.position DESC LIMIT 1`, org, subscriptionID, recordID, exceptVersion).Scan(&p.id, &p.versionID, &p.subscriptionVersionID, &p.destination)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

func commitMatch(ctx context.Context, tx pgx.Tx, in monitoring.Intent, evidence monitoring.MatchEvidence) (string, error) {
	org := in.Organization
	pinned, corpusID, refused, err := guardEvaluation(ctx, tx, in)
	if err != nil || refused != "" {
		return refused, err
	}
	// A Record Version matched by any Version of the Subscription is not
	// matched again: an edit between its retrieval and enrichment triggers
	// neither repeats the alert nor links a Match to one on the same content.
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM matches WHERE organization=$1 AND record_id=$4 AND subscription_id=$2 AND record_version_id=$3)`, org, in.SubscriptionID, in.VersionID, in.RecordID).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return monitoring.OutcomeDuplicate, nil
	}
	prior, corrected, err := latestMatch(ctx, tx, org, in.SubscriptionID, in.RecordID, in.VersionID)
	if err != nil {
		return "", err
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	// match.corrected carries every match.created reference plus its
	// predecessor, so a consumer can act on it alone.
	n := notice{Kind: monitoring.NoticeCreated, CorpusID: corpusID, Destination: pinned.destination,
		References: monitoring.NoticeReferences{MatchID: content.StableID("match", org, in.SubscriptionVersionID, in.VersionID), RecordID: in.RecordID, RecordVersionID: in.VersionID, SubscriptionID: in.SubscriptionID, SubscriptionVersionID: in.SubscriptionVersionID}}
	var previous any
	if corrected {
		n.Kind, n.References.PreviousMatchID, previous = monitoring.NoticeCorrected, prior.id, prior.id
	}
	created, err := commitNotice(ctx, tx, org, n, func(position int64) error {
		_, err := tx.Exec(ctx, `INSERT INTO matches(organization,id,subscription_id,subscription_version_id,saved_query_id,saved_query_version_id,corpus_id,record_id,record_version_id,previous_match_id,evidence,position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			org, n.References.MatchID, in.SubscriptionID, in.SubscriptionVersionID, pinned.queryID, pinned.queryVersionID, corpusID, in.RecordID, in.VersionID, previous, evidenceJSON, position)
		return err
	})
	if err != nil || !created {
		return monitoring.OutcomeDuplicate, err
	}
	return monitoring.OutcomeMatched, nil
}

func commitNoMatch(ctx context.Context, tx pgx.Tx, in monitoring.Intent) (string, error) {
	org := in.Organization
	_, corpusID, refused, err := guardEvaluation(ctx, tx, in)
	if err != nil || refused != "" {
		return refused, err
	}
	// A Version that matched is never invalidated by a later decision about itself.
	var matched bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM matches WHERE organization=$1 AND record_id=$4 AND subscription_id=$2 AND record_version_id=$3)`, org, in.SubscriptionID, in.VersionID, in.RecordID).Scan(&matched); err != nil {
		return "", err
	}
	if matched {
		return monitoring.OutcomeNoMatch, nil
	}
	prior, found, err := latestMatch(ctx, tx, org, in.SubscriptionID, in.RecordID, in.VersionID)
	if err != nil || !found {
		return monitoring.OutcomeNoMatch, err
	}
	created, err := commitNotice(ctx, tx, org, notice{Kind: monitoring.NoticeNoLongerMatches, CorpusID: corpusID, Destination: prior.destination,
		References: monitoring.NoticeReferences{MatchID: prior.id, RecordID: in.RecordID, RecordVersionID: in.VersionID, SubscriptionID: in.SubscriptionID, SubscriptionVersionID: prior.subscriptionVersionID}}, nil)
	if err != nil || !created {
		return monitoring.OutcomeDuplicate, err
	}
	return monitoring.OutcomeNoLongerMatches, nil
}

func commitWithdrawal(ctx context.Context, tx pgx.Tx, in monitoring.Intent) (string, error) {
	org := in.Organization
	var enabled, tombstoned, scoped bool
	var corpusID string
	err := tx.QueryRow(ctx, `SELECT s.enabled,r.corpus_id,
  EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),
  EXISTS(SELECT 1 FROM subscription_corpora sc WHERE sc.organization=r.organization AND sc.corpus_id=r.corpus_id AND sc.subscription_id=s.id)
FROM subscriptions s JOIN records r ON r.organization=s.organization AND r.id=$3
WHERE s.organization=$1 AND s.id=$2`, org, in.SubscriptionID, in.RecordID).Scan(&enabled, &corpusID, &tombstoned, &scoped)
	if errors.Is(err, pgx.ErrNoRows) {
		return monitoring.OutcomeIneligible, nil
	}
	if err != nil {
		return "", err
	}
	if !tombstoned || !scoped {
		return monitoring.OutcomeIneligible, nil
	}
	prior, found, err := latestMatch(ctx, tx, org, in.SubscriptionID, in.RecordID, "")
	if err != nil || !found {
		return monitoring.OutcomeIneligible, err
	}
	// Committed while disabled, its delivery window opens at the re-enable.
	created, err := commitNotice(ctx, tx, org, notice{Kind: monitoring.NoticeWithdrawn, CorpusID: corpusID, Destination: prior.destination, Dormant: !enabled,
		References: monitoring.NoticeReferences{MatchID: prior.id, RecordID: in.RecordID, RecordVersionID: prior.versionID, SubscriptionID: in.SubscriptionID, SubscriptionVersionID: prior.subscriptionVersionID}}, nil)
	if err != nil || !created {
		return monitoring.OutcomeDuplicate, err
	}
	return monitoring.OutcomeWithdrawalNotified, nil
}

// notice is one monitoring notice to commit; its Delivery ID is derived.
// A Dormant notice is committed while its Subscription is disabled: its
// delivery window does not start until a re-enable makes it admissible.
type notice struct {
	Kind, CorpusID, Destination string
	References                  monitoring.NoticeReferences
	Dormant                     bool
}

// commitNotice commits a notice's public event, optional Match (withMatch runs
// after the event so it can store the journal position), unique Delivery,
// immutable body and outbox work. The event identity derives from the kind and
// the referenced Match, as does the Delivery (match, destination, kind); an
// existing Delivery commits nothing and reports false, so a repeat never
// reaches a unique violation.
func commitNotice(ctx context.Context, tx pgx.Tx, org string, n notice, withMatch func(position int64) error) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deliveries WHERE organization=$1 AND match_id=$2 AND destination_id=$3 AND event_kind=$4)`, org, n.References.MatchID, n.Destination, n.Kind).Scan(&exists); err != nil || exists {
		return false, err
	}
	r := &n.References
	r.DeliveryID = content.StableID("delivery", org, r.MatchID, n.Destination, n.Kind)
	event := eventInput{Organization: org, CorpusID: n.CorpusID, Kind: n.Kind, Resource: "match", ResourceID: r.MatchID, MutationID: r.MatchID}
	// The notice time is the transaction time, which is also the event's occurred_at.
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return false, err
	}
	body, err := json.Marshal(monitoring.Notice{EventID: eventID(event), Type: n.Kind, SchemaVersion: "1", OccurredAt: now.UTC(), References: *r})
	if err != nil {
		return false, err
	}
	position, err := appendEventAt(ctx, tx, event)
	if err != nil {
		return false, err
	}
	if withMatch != nil {
		if err = withMatch(position); err != nil {
			return false, err
		}
	}
	var previous any
	if r.PreviousMatchID != "" {
		previous = r.PreviousMatchID
	}
	var windowStart any
	if n.Dormant {
		windowStart = "infinity"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deliveries(organization,id,match_id,destination_id,event_kind,event_id,window_start) VALUES($1,$2,$3,$4,$5,$6,$7::timestamptz)`, org, r.DeliveryID, r.MatchID, n.Destination, n.Kind, eventID(event), windowStart); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO monitoring_notices(organization,event_id,kind,match_id,record_id,record_version_id,subscription_id,subscription_version_id,delivery_id,previous_match_id,body,position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		org, eventID(event), n.Kind, r.MatchID, r.RecordID, r.RecordVersionID, r.SubscriptionID, r.SubscriptionVersionID, r.DeliveryID, previous, body, position); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO delivery_outbox(organization,delivery_id) VALUES($1,$2)`, org, r.DeliveryID); err != nil {
		return false, err
	}
	return true, nil
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
	var enabled, deleted, withdrawn bool
	var later monitoring.Later
	var kind string
	var next *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT d.id,d.match_id,m.subscription_id,d.destination_id,d.state,d.attempt_count,n.body,n.kind,s.enabled,s.deleted,
  r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),`+laterNoticesSQL+`,
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

var _ monitoring.MatchStore = ContentStore{}
