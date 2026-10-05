package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fanOutPage bounds how many Subscriptions one dispatch step enumerates for a
// trigger event; larger scopes continue on the next step from a checkpoint.
const fanOutPage = 100

// fanOutEvents bounds how many dispatch steps one FanOut call runs per Organization.
const fanOutEvents = 50

// EvaluationStore is the PostgreSQL evaluation dispatch and Match commit.
type EvaluationStore struct {
	Pool *pgxpool.Pool
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
			// after its re-enable: the pause is never backfilled. Enrichment
			// asks again only the Subscription Versions that have not decided
			// this Record Version yet (they answered not_ready, or their
			// intent is still pending), so a decided rule, such as one that
			// calls a paid classifier, is not called a second time.
			rows, err = tx.Query(ctx, `SELECT s.id,v.id,$6::text FROM subscription_corpora sc
JOIN subscriptions s ON (s.organization,s.id)=(sc.organization,sc.subscription_id)
JOIN LATERAL (SELECT e.id,e.saved_query_version_id FROM subscription_versions e
  WHERE e.organization=s.organization AND e.subscription_id=s.id AND e.activation_position<$3
  ORDER BY e.activation_position DESC LIMIT 1) v ON true
JOIN saved_query_versions q ON (q.organization,q.id)=(s.organization,v.saved_query_version_id)
WHERE sc.organization=$1 AND sc.corpus_id=$2 AND s.enabled AND $2=ANY(q.corpus_ids) AND coalesce(s.enabled_position,0)<$3 AND s.id>$4
  AND NOT ($7 AND EXISTS(SELECT 1 FROM evaluation_intents d WHERE d.organization=$1 AND d.subscription_version_id=v.id AND d.record_version_id=$6
    AND d.kind='evaluation' AND d.state='done' AND d.outcome IN `+decidedOutcomes+`))
ORDER BY s.id LIMIT $5`, org, corpusID, sequence, after, s.page(), versionID, eventType == "record.enrichment_available")
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

// decidedOutcomes are the outcomes of an intent that decided its Record
// Version for its Subscription Version.
const decidedOutcomes = "('matched','duplicate','no_match','no_longer_matches')"

// evaluatingSibling is true for an evaluation intent of table alias t whose
// Subscription Version is evaluating the same Record Version under another,
// leased intent: it waits, so one decision serves both and a rule that calls
// a paid backend is not called twice for the same pair.
func evaluatingSibling(t string) string {
	return `(` + t + `.kind='evaluation' AND EXISTS(SELECT 1 FROM evaluation_intents o WHERE o.organization=` + t + `.organization AND o.subscription_version_id=` + t + `.subscription_version_id
    AND o.record_version_id=` + t + `.record_version_id AND o.sequence<>` + t + `.sequence AND o.kind='evaluation' AND o.state='pending' AND o.lease_until>=now()))`
}

// errArticleBusy reports a claim candidate whose Record Version another worker
// is claiming or evaluating; errClaimLost one another worker leased first.
var (
	errArticleBusy = errors.New("record version busy")
	errClaimLost   = errors.New("claim lost")
)

// claimAttempts bounds the candidates one Claim tries before reporting no work.
const claimAttempts = 8

// Claim leases one due intent. An evaluation intent is claimed only while no
// other worker claims or evaluates its Record Version: the claim takes a
// per-Version advisory lock, then checks with a fresh snapshot that no
// evaluation intent of that Version is leased. The winner then takes the
// Version's other due intents with ClaimRelated, so one article's alerts are
// decided in one batch (one plugin call) and never split between workers.
// The lock is held until the lease is committed, so a later claimer that gets
// the lock always sees the lease.
func (s EvaluationStore) Claim(ctx context.Context, lease time.Duration) (monitoring.Intent, error) {
	busy := []string{}
	for attempt := 0; attempt < claimAttempts; attempt++ {
		in, err := s.claimOnce(ctx, lease, busy)
		switch {
		case errors.Is(err, errArticleBusy):
			busy = append(busy, in.Organization+":"+in.VersionID)
		case errors.Is(err, errClaimLost):
		default:
			return in, err
		}
	}
	return monitoring.Intent{}, monitoring.ErrNoWork
}

func (s EvaluationStore) claimOnce(ctx context.Context, lease time.Duration, busy []string) (monitoring.Intent, error) {
	var in monitoring.Intent
	// Read committed: the check after the lock must see leases committed since
	// the candidate was read, whatever the server's default isolation.
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return in, err
	}
	defer tx.Rollback(ctx)
	// The candidate is only read: locking its row here would make a worker
	// evaluating the same Version skip it in ClaimRelated.
	err = tx.QueryRow(ctx, `SELECT c.kind,c.organization,c.subscription_version_id,c.sequence,c.record_version_id FROM evaluation_intents c
WHERE c.state='pending' AND c.available_at<=now() AND c.lease_until<now()
  AND NOT (c.kind='evaluation' AND (c.organization || ':' || c.record_version_id = ANY($1::text[]) OR `+leasedArticle("c")+`))
ORDER BY c.available_at,c.sequence LIMIT 1`, busy).Scan(&in.Kind, &in.Organization, &in.SubscriptionVersionID, &in.Sequence, &in.VersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, monitoring.ErrNoWork
	}
	if err != nil {
		return in, err
	}
	if in.Kind == monitoring.IntentEvaluation {
		var locked bool
		if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('evaluation:' || $1 || ':' || $2, 0))`, in.Organization, in.VersionID).Scan(&locked); err != nil {
			return in, err
		}
		if !locked {
			return in, errArticleBusy
		}
		// A new statement sees every lease committed before the lock was taken.
		var leased bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM evaluation_intents c WHERE c.organization=$1 AND c.subscription_version_id=$2 AND c.sequence=$3 AND `+leasedArticle("c")+`)`,
			in.Organization, in.SubscriptionVersionID, in.Sequence).Scan(&leased); err != nil {
			return in, err
		}
		if leased {
			return in, errArticleBusy
		}
	}
	err = tx.QueryRow(ctx, `UPDATE evaluation_intents i SET lease_until=now()+make_interval(secs => $4::double precision)
WHERE i.organization=$1 AND i.subscription_version_id=$2 AND i.sequence=$3 AND i.state='pending' AND i.available_at<=now() AND i.lease_until<now()
RETURNING i.kind,i.organization,i.subscription_id,i.subscription_version_id,i.sequence,i.corpus_id,i.record_id,i.record_version_id,i.attempts`,
		in.Organization, in.SubscriptionVersionID, in.Sequence, lease.Seconds()).Scan(
		&in.Kind, &in.Organization, &in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.CorpusID, &in.RecordID, &in.VersionID, &in.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, errClaimLost
	}
	if err != nil {
		return in, err
	}
	return in, tx.Commit(ctx)
}

// leasedArticle is true for an intent of table alias t whose Record Version
// has another evaluation intent leased: a worker is evaluating it.
func leasedArticle(t string) string {
	return `EXISTS(SELECT 1 FROM evaluation_intents o WHERE o.organization=` + t + `.organization AND o.record_version_id=` + t + `.record_version_id
    AND (o.subscription_version_id,o.sequence)<>(` + t + `.subscription_version_id,` + t + `.sequence) AND o.kind='evaluation' AND o.state='pending' AND o.lease_until>=now())`
}

func (s EvaluationStore) Target(ctx context.Context, in monitoring.Intent) (monitoring.Target, error) {
	var t monitoring.Target
	v := &t.Subscription
	var evaluator, definition, vectors []byte
	err := s.Pool.QueryRow(ctx, `SELECT s.enabled,`+supersededSQL("v", "$5")+`,v.subscription_id,v.id,v.saved_query_id,v.saved_query_version_id,v.evaluator,v.destination_id,v.activation_position,q.corpus_ids,q.definition,coalesce(s.owner,''),
  EXISTS(SELECT 1 FROM segments sg JOIN embedding_coverage ec ON (ec.organization,ec.segment_id)=(sg.organization,sg.id) WHERE sg.organization=$1 AND sg.version_id=$3 AND ec.generation_id=`+routedGenerationSQL("$1", "$4")+`),
  EXISTS(SELECT 1 FROM evaluation_intents d WHERE d.organization=$1 AND d.subscription_version_id=$2 AND d.record_version_id=$3 AND d.sequence<>$5 AND d.kind='evaluation' AND d.state='done' AND d.outcome IN `+decidedOutcomes+`),q.query_vectors
FROM subscription_versions v
JOIN subscriptions s ON (s.organization,s.id)=(v.organization,v.subscription_id)
JOIN saved_query_versions q ON (q.organization,q.id)=(v.organization,v.saved_query_version_id)
WHERE v.organization=$1 AND v.id=$2`, in.Organization, in.SubscriptionVersionID, in.VersionID, in.CorpusID, in.Sequence).Scan(
		&t.Enabled, &t.Superseded, &v.SubscriptionID, &v.VersionID, &v.SavedQueryID, &v.SavedQueryVersionID, &evaluator, &v.DestinationID, &v.ActivationPosition, &v.CorpusIDs, &definition, &v.Owner, &t.Enriched, &t.Decided, &vectors)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, monitoring.ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if err = unmarshalNumbers(evaluator, &v.Evaluator); err != nil {
		return t, err
	}
	if err := json.Unmarshal(vectors, &t.QueryVectors); err != nil {
		return t, err
	}
	return t, unmarshalNumbers(definition, &t.Definition)
}

func (s EvaluationStore) Complete(ctx context.Context, in monitoring.Intent, outcome string) error {
	_, err := s.Pool.Exec(ctx, completeIntentSQL, in.Organization, in.SubscriptionVersionID, in.Sequence, outcome)
	return err
}

// completeIntentSQL completes a pending intent. When it leaves every
// Subscription asked about its Record Version decided, the same statement
// records the Version's evaluated step, once: no evaluation of the Version is
// pending, and every not_ready answer, which waits for vectors, has a later
// decision by the same Subscription, under whichever of its Versions was in
// effect then. A Subscription disabled between the two rounds never decides,
// and the step stays unrecorded. The subqueries read the snapshot before the
// update, so they skip the intent being completed. Versions materialized
// before step times were recorded get none.
const completeIntentSQL = `WITH done AS (
  UPDATE evaluation_intents SET state='done',outcome=$4,error_code='',lease_until='-infinity'
  WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3 AND state='pending'
  RETURNING organization,record_version_id,kind)
UPDATE record_versions v SET evaluated_at=clock_timestamp() FROM done
WHERE done.kind='evaluation' AND $4<>'not_ready' AND v.organization=done.organization AND v.id=done.record_version_id
  AND v.evaluated_at IS NULL AND v.materialized_at IS NOT NULL
  AND NOT EXISTS(SELECT 1 FROM evaluation_intents p WHERE p.organization=done.organization AND p.record_version_id=done.record_version_id
    AND p.kind='evaluation' AND p.state='pending' AND NOT (p.subscription_version_id=$2 AND p.sequence=$3))
  AND NOT EXISTS(SELECT 1 FROM evaluation_intents n WHERE n.organization=done.organization AND n.record_version_id=done.record_version_id
    AND n.outcome='not_ready' AND n.subscription_version_id<>$2
    AND NOT EXISTS(SELECT 1 FROM subscription_versions e JOIN evaluation_intents d ON d.organization=e.organization AND d.subscription_version_id=e.id
      WHERE e.organization=n.organization AND e.subscription_id=n.subscription_id
      AND d.record_version_id=n.record_version_id AND d.kind='evaluation' AND d.state='done' AND d.outcome<>'not_ready'))`

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
	outcomes, err := s.CommitMatches(ctx, []monitoring.MatchCommit{{Intent: in, Evidence: evidence}})
	if err != nil {
		return "", err
	}
	return outcomes[0], nil
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
	var state, previous string
	if err = tx.QueryRow(ctx, `SELECT state,outcome FROM evaluation_intents WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3 FOR UPDATE`, in.Organization, in.SubscriptionVersionID, in.Sequence).Scan(&state, &previous); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if state == "done" && previous == monitoring.OutcomeEvaluatorRetired {
		return previous, nil
	}
	outcome, err := decide(tx)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, completeIntentSQL, in.Organization, in.SubscriptionVersionID, in.Sequence, outcome); err != nil {
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
	// The Subscription Owner is fixed at creation, so the notice shares it for good.
	if err := tx.QueryRow(ctx, `SELECT coalesce(owner,'') FROM subscriptions WHERE organization=$1 AND id=$2`, org, r.SubscriptionID).Scan(&r.Owner); err != nil {
		return false, err
	}
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

// matchColumns reads a Match m with its Subscription s's owner.
const matchColumns = `m.id,m.subscription_id,m.subscription_version_id,m.saved_query_id,m.saved_query_version_id,m.record_id,m.record_version_id,coalesce(m.previous_match_id,''),coalesce(s.owner,''),m.evidence,m.position`

// matchFrom joins each Match to its Subscription for the owner.
const matchFrom = ` FROM matches m JOIN subscriptions s ON s.organization=m.organization AND s.id=m.subscription_id `

func scanMatch(row pgx.Row) (monitoring.Match, error) {
	var m monitoring.Match
	var evidence []byte
	if err := row.Scan(&m.ID, &m.SubscriptionID, &m.SubscriptionVersionID, &m.SavedQueryID, &m.SavedQueryVersionID, &m.RecordID, &m.RecordVersionID, &m.PreviousMatchID, &m.Owner, &evidence, &m.Position); err != nil {
		return m, err
	}
	return m, unmarshalNumbers(evidence, &m.Evidence)
}
