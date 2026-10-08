package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

var _ monitoring.MatchBatchStore = EvaluationStore{}

type matchCandidate struct {
	ordinal                  int
	pinned                   subscriptionPin
	corpusID, owner, refused string
	duplicate, prior         bool
	now                      time.Time
}

// matchCandidatesSQL locks existing intents (including retired ones) and
// reads the single-decision guard for every input in one round trip. The
// Organization journal lock fences subscription edits, retirement, promotion,
// quarantine and withdrawal while the group is committed.
var matchCandidatesSQL = `WITH requested AS (
  SELECT * FROM unnest($4::text[],$5::text[],$6::bigint[],$7::text[])
    WITH ORDINALITY AS inputs(subscription_id,subscription_version_id,sequence,match_id,ordinal)),
locked AS MATERIALIZED (
  SELECT i.subscription_version_id,i.sequence,i.state,i.outcome FROM evaluation_intents i
  WHERE i.organization=$1 AND EXISTS(SELECT 1 FROM requested x
    WHERE (x.subscription_version_id,x.sequence)=(i.subscription_version_id,i.sequence))
  FOR UPDATE OF i)
SELECT x.ordinal,coalesce(sv.saved_query_id,''),coalesce(sv.saved_query_version_id,''),coalesce(sv.destination_id,''),
  coalesce(r.corpus_id,''),coalesce(s.owner,''),
  CASE WHEN i.state='done' AND i.outcome='evaluator_retired' THEN 'evaluator_retired'
    WHEN s.id IS NULL OR sv.id IS NULL OR q.id IS NULL THEN 'ineligible'
    WHEN NOT s.enabled OR x.sequence<=coalesce(s.enabled_position,0) THEN 'subscription_disabled'
    WHEN ` + supersededPointSQL("sv", "x.sequence") + ` THEN 'subscription_version_superseded'
    WHEN r.id IS NULL OR v.id IS NULL OR NOT coalesce(r.current_version_id IS NOT DISTINCT FROM v.id
      AND ` + eligibleVersionPointSQL + ` AND r.corpus_id=ANY(q.corpus_ids),false) THEN 'ineligible'
    ELSE '' END,
  EXISTS(SELECT 1 FROM matches m WHERE m.organization=$1 AND m.record_id=$2
    AND m.subscription_id=x.subscription_id AND m.record_version_id=$3)
    OR EXISTS(SELECT 1 FROM deliveries d WHERE d.organization=$1 AND d.match_id=x.match_id
      AND d.destination_id=sv.destination_id AND d.event_kind='match.created'),
  EXISTS(SELECT 1 FROM matches m JOIN subscription_versions previous
    ON (previous.organization,previous.id)=(m.organization,m.subscription_version_id)
    WHERE m.organization=$1 AND m.record_id=$2 AND m.subscription_id=x.subscription_id AND m.record_version_id<>$3),
  now()
FROM requested x
LEFT JOIN locked i ON (i.subscription_version_id,i.sequence)=(x.subscription_version_id,x.sequence)
LEFT JOIN subscriptions s ON s.organization=$1 AND s.id=x.subscription_id
LEFT JOIN subscription_versions sv ON (sv.organization,sv.id)=(s.organization,x.subscription_version_id) AND sv.subscription_id=s.id
LEFT JOIN saved_query_versions q ON (q.organization,q.id)=(sv.organization,sv.saved_query_version_id)
LEFT JOIN record_versions v ON v.organization=$1 AND v.id=$3 AND v.record_id=$2
LEFT JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
ORDER BY x.ordinal`

// CommitMatches commits one bounded Record Version group. New positives take
// a fixed number of round trips regardless of the number of subscriptions.
// Repeated positives share their first eligible Match; corrections keep the
// established decision path so their predecessor and ordering rules hold.
func (s EvaluationStore) CommitMatches(ctx context.Context, matches []monitoring.MatchCommit) ([]string, error) {
	var result0 []string
	err := retryJournalWrite(ctx, "CommitMatches", func(ctx context.Context) error {
		var err error
		result0, err = s.commitMatchesAttempt(ctx, matches)
		return err
	})
	return result0, err
}

func (s EvaluationStore) commitMatchesAttempt(ctx context.Context, matches []monitoring.MatchCommit) ([]string, error) {
	if len(matches) == 0 {
		return []string{}, nil
	}
	first := matches[0].Intent
	ids, versions, sequences, matchIDs := make([]string, len(matches)), make([]string, len(matches)), make([]int64, len(matches)), make([]string, len(matches))
	fallback := false
	for i, match := range matches {
		in := match.Intent
		if in.Organization != first.Organization || in.RecordID != first.RecordID || in.VersionID != first.VersionID {
			return nil, errors.New("match group must name one organization, record and version")
		}
		ids[i], versions[i], sequences[i] = in.SubscriptionID, in.SubscriptionVersionID, in.Sequence
		matchIDs[i] = content.StableID("match", in.Organization, in.SubscriptionVersionID, in.VersionID)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	guards := journalBatch(first.Organization)
	guards.Queue(matchCandidatesSQL, first.Organization, first.RecordID, first.VersionID, ids, versions, sequences, matchIDs)
	results := tx.SendBatch(ctx, guards)
	defer results.Close()
	for range 3 {
		if _, err = results.Exec(); err != nil {
			return nil, err
		}
	}
	rows, err := results.Query()
	if err != nil {
		return nil, err
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (matchCandidate, error) {
		var c matchCandidate
		err := row.Scan(&c.ordinal, &c.pinned.queryID, &c.pinned.queryVersionID, &c.pinned.destination, &c.corpusID, &c.owner, &c.refused, &c.duplicate, &c.prior, &c.now)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	if err = results.Close(); err != nil {
		return nil, err
	}
	if len(candidates) != len(matches) {
		return nil, errors.New("match group eligibility count differs from inputs")
	}
	outcomes := make([]string, len(matches))
	newMatches := 0
	created := make(map[string]bool, len(matches))
	for i, c := range candidates {
		if c.ordinal != i+1 {
			return nil, errors.New("match group eligibility order differs from inputs")
		}
		fallback = fallback || (c.prior && c.refused == "")
		switch {
		case c.refused != "":
			outcomes[i] = c.refused
		case c.duplicate || created[matches[i].Intent.SubscriptionID]:
			outcomes[i] = monitoring.OutcomeDuplicate
		default:
			outcomes[i] = monitoring.OutcomeMatched
			// Only an eligible new positive suppresses later inputs for this
			// Subscription. Refusals and retirement retain their own outcomes.
			created[matches[i].Intent.SubscriptionID] = true
			newMatches++
		}
	}
	if fallback {
		for i, match := range matches {
			if outcomes[i] == monitoring.OutcomeEvaluatorRetired {
				continue
			}
			outcomes[i], err = commitMatch(ctx, tx, match.Intent, match.Evidence)
			if err != nil {
				return nil, err
			}
			if _, err = tx.Exec(ctx, completeIntentSQL, first.Organization, match.Intent.SubscriptionVersionID, match.Intent.Sequence, outcomes[i]); err != nil {
				return nil, err
			}
		}
	} else {
		if err = writeNewMatches(ctx, tx, matches, candidates, matchIDs, outcomes, newMatches); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return outcomes, nil
}

func writeNewMatches(ctx context.Context, tx pgx.Tx, matches []monitoring.MatchCommit, candidates []matchCandidate, matchIDs, outcomes []string, count int) error {
	org := matches[0].Intent.Organization
	writes := &pgx.Batch{}
	if count > 0 {
		// The journal head and every allocated event are visible together only
		// after commit. Later statements in this packet read the allocated head
		// and add the input ordinal to its base; no allocation result round trip
		// is needed. A failed packet rolls the allocation back as well.
		writes.Queue(`UPDATE organization_journals SET last_sequence=last_sequence+$2 WHERE organization=$1`, org, count)
	}
	var ids, subscriptionIDs, subscriptionVersions, queryIDs, queryVersions, corpusIDs, deliveryIDs, destinations, eventIDs, evidenceJSON []string
	var bodies [][]byte
	var ordinals []int64
	var ordinal int64
	var completeVersions, completeOutcomes []string
	var completeSequences []int64
	for i, match := range matches {
		if outcomes[i] == monitoring.OutcomeEvaluatorRetired {
			// An explicit retirement never completes or materializes a late answer.
			continue
		}
		in, c := match.Intent, candidates[i]
		if outcomes[i] == monitoring.OutcomeMatched {
			evidence, err := json.Marshal(match.Evidence)
			if err != nil {
				return err
			}
			r := monitoring.NoticeReferences{MatchID: matchIDs[i], RecordID: in.RecordID, RecordVersionID: in.VersionID, SubscriptionID: in.SubscriptionID, SubscriptionVersionID: in.SubscriptionVersionID, Owner: c.owner}
			if c.pinned.destination != "" {
				r.DeliveryID = content.StableID("delivery", org, r.MatchID, c.pinned.destination, monitoring.NoticeCreated)
			}
			event := eventInput{Organization: org, CorpusID: c.corpusID, Kind: monitoring.NoticeCreated, Resource: "match", ResourceID: r.MatchID, MutationID: r.MatchID}
			body, err := json.Marshal(monitoring.Notice{EventID: eventID(event), Type: monitoring.NoticeCreated, SchemaVersion: "1", OccurredAt: c.now.UTC(), References: r})
			if err != nil {
				return err
			}
			ordinal++
			ids, subscriptionIDs, subscriptionVersions = append(ids, r.MatchID), append(subscriptionIDs, in.SubscriptionID), append(subscriptionVersions, in.SubscriptionVersionID)
			queryIDs, queryVersions, corpusIDs = append(queryIDs, c.pinned.queryID), append(queryVersions, c.pinned.queryVersionID), append(corpusIDs, c.corpusID)
			deliveryIDs, destinations, eventIDs = append(deliveryIDs, r.DeliveryID), append(destinations, c.pinned.destination), append(eventIDs, eventID(event))
			evidenceJSON, bodies, ordinals = append(evidenceJSON, string(evidence)), append(bodies, body), append(ordinals, ordinal)
		}
		completeVersions, completeSequences, completeOutcomes = append(completeVersions, in.SubscriptionVersionID), append(completeSequences, in.Sequence), append(completeOutcomes, outcomes[i])
	}
	if len(ids) > 0 {
		writes.Queue(`INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id,trace_context)
SELECT $1,(SELECT last_sequence FROM organization_journals WHERE organization=$1)-$6::bigint+x.position,x.event_id,x.corpus_id,'match.created','match',x.match_id,NULL,$7
FROM unnest($2::bigint[],$3::text[],$4::text[],$5::text[]) WITH ORDINALITY AS x(position,event_id,corpus_id,match_id,ordinal) ORDER BY x.ordinal`, org, ordinals, eventIDs, corpusIDs, ids, count, telemetry.Encode(ctx))
		writes.Queue(`INSERT INTO matches(organization,id,subscription_id,subscription_version_id,saved_query_id,saved_query_version_id,corpus_id,record_id,record_version_id,previous_match_id,evidence,position)
SELECT $1,x.match_id,x.subscription_id,x.subscription_version_id,x.query_id,x.query_version_id,x.corpus_id,$2,$3,NULL,x.evidence,(SELECT last_sequence FROM organization_journals WHERE organization=$1)-$12::bigint+x.position
FROM unnest($4::text[],$5::text[],$6::text[],$7::text[],$8::text[],$9::text[],$10::jsonb[],$11::bigint[])
  WITH ORDINALITY AS x(match_id,subscription_id,subscription_version_id,query_id,query_version_id,corpus_id,evidence,position,ordinal) ORDER BY x.ordinal`, org, matches[0].Intent.RecordID, matches[0].Intent.VersionID, ids, subscriptionIDs, subscriptionVersions, queryIDs, queryVersions, corpusIDs, evidenceJSON, ordinals, count)
		writes.Queue(`INSERT INTO deliveries(organization,id,match_id,destination_id,event_kind,event_id,window_start)
SELECT $1,x.delivery_id,x.match_id,x.destination_id,'match.created',x.event_id,NULL
FROM unnest($2::text[],$3::text[],$4::text[],$5::text[]) WITH ORDINALITY AS x(delivery_id,match_id,destination_id,event_id,ordinal)
WHERE x.destination_id<>'' ORDER BY x.ordinal`, org, deliveryIDs, ids, destinations, eventIDs)
		writes.Queue(`INSERT INTO monitoring_notices(organization,event_id,kind,match_id,record_id,record_version_id,subscription_id,subscription_version_id,delivery_id,previous_match_id,body,position)
SELECT $1,x.event_id,'match.created',x.match_id,$2,$3,x.subscription_id,x.subscription_version_id,NULLIF(x.delivery_id,''),NULL,x.body,(SELECT last_sequence FROM organization_journals WHERE organization=$1)-$11::bigint+x.position
FROM unnest($4::text[],$5::text[],$6::text[],$7::text[],$8::text[],$9::bytea[],$10::bigint[])
  WITH ORDINALITY AS x(event_id,match_id,subscription_id,subscription_version_id,delivery_id,body,position,ordinal) ORDER BY x.ordinal`, org, matches[0].Intent.RecordID, matches[0].Intent.VersionID, eventIDs, ids, subscriptionIDs, subscriptionVersions, deliveryIDs, bodies, ordinals, count)
		writes.Queue(`INSERT INTO delivery_outbox(organization,delivery_id,trace_context)
SELECT $1,x.delivery_id,$3 FROM unnest($2::text[]) WITH ORDINALITY AS x(delivery_id,ordinal)
WHERE x.delivery_id<>'' ORDER BY x.ordinal`, org, deliveryIDs, telemetry.Encode(ctx))
	}
	if len(completeVersions) > 0 {
		writes.Queue(completeMatchGroupSQL, org, completeVersions, completeSequences, completeOutcomes)
	}
	if writes.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, writes).Close()
}

// completeMatchGroupSQL models the state after ordered single completions.
// The first occurrence of a requested key owns its outcome; later occurrences
// would already be done. Only actual pending rows returned by done can record
// an evaluated step or resolve an earlier not_ready answer. Table subqueries
// see the pre-update snapshot, so they explicitly exclude every actual done key
// and include its newly completed non-not_ready evaluation decision.
const completeMatchGroupSQL = `WITH requested AS (
  SELECT DISTINCT ON (subscription_version_id,sequence) subscription_version_id,sequence,outcome
  FROM unnest($2::text[],$3::bigint[],$4::text[])
    WITH ORDINALITY AS x(subscription_version_id,sequence,outcome,ordinal)
  ORDER BY subscription_version_id,sequence,ordinal),
done AS (
  UPDATE evaluation_intents i SET state='done',outcome=x.outcome,error_code='',lease_until='-infinity'
  FROM requested x WHERE i.organization=$1 AND i.subscription_version_id=x.subscription_version_id
    AND i.sequence=x.sequence AND i.state='pending'
  RETURNING i.organization,i.record_version_id,i.kind,i.subscription_version_id,i.sequence,i.outcome),
evaluated AS (
  SELECT DISTINCT organization,record_version_id FROM done WHERE kind='evaluation' AND outcome<>'not_ready')
UPDATE record_versions v SET evaluated_at=clock_timestamp() FROM evaluated
WHERE v.organization=evaluated.organization AND v.id=evaluated.record_version_id
  AND v.evaluated_at IS NULL AND v.materialized_at IS NOT NULL
  AND NOT EXISTS(SELECT 1 FROM evaluation_intents p WHERE p.organization=v.organization AND p.record_version_id=v.id
    AND p.kind='evaluation' AND p.state='pending'
    AND NOT EXISTS(SELECT 1 FROM done d WHERE d.organization=p.organization
      AND d.subscription_version_id=p.subscription_version_id AND d.sequence=p.sequence))
  AND NOT EXISTS(SELECT 1 FROM evaluation_intents n WHERE n.organization=v.organization AND n.record_version_id=v.id
    AND n.outcome='not_ready'
    AND NOT EXISTS(SELECT 1 FROM subscription_versions e JOIN evaluation_intents d ON d.organization=e.organization AND d.subscription_version_id=e.id
      WHERE e.organization=n.organization AND e.subscription_id=n.subscription_id
      AND d.record_version_id=n.record_version_id AND d.kind='evaluation' AND d.state='done' AND d.outcome<>'not_ready')
    AND NOT EXISTS(SELECT 1 FROM subscription_versions e JOIN done d ON d.organization=e.organization AND d.subscription_version_id=e.id
      WHERE e.organization=n.organization AND e.subscription_id=n.subscription_id
      AND d.record_version_id=n.record_version_id AND d.kind='evaluation' AND d.outcome<>'not_ready'))`
