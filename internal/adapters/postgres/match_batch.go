package postgres

import (
	"context"
	"encoding/json"
	"errors"
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
    WHEN ` + supersededSQL("sv", "x.sequence") + ` THEN 'subscription_version_superseded'
    WHEN r.id IS NULL OR v.id IS NULL OR NOT coalesce(r.current_version_id IS NOT DISTINCT FROM v.id
      AND ` + eligibleVersionSQL + ` AND r.corpus_id=ANY(q.corpus_ids),false) THEN 'ineligible'
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
	if err = lockJournal(ctx, tx, first.Organization); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, matchCandidatesSQL, first.Organization, first.RecordID, first.VersionID, ids, versions, sequences, matchIDs)
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
	var position int64
	if count > 0 {
		// The journal head and every allocated event are visible together only
		// after commit. A failed packet rolls the allocation back as well.
		if err := tx.QueryRow(ctx, `UPDATE organization_journals SET last_sequence=last_sequence+$2 WHERE organization=$1 RETURNING last_sequence-$2`, org, count).Scan(&position); err != nil {
			return err
		}
	}
	writes := &pgx.Batch{}
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
			r.DeliveryID = content.StableID("delivery", org, r.MatchID, c.pinned.destination, monitoring.NoticeCreated)
			event := eventInput{Organization: org, CorpusID: c.corpusID, Kind: monitoring.NoticeCreated, Resource: "match", ResourceID: r.MatchID, MutationID: r.MatchID}
			body, err := json.Marshal(monitoring.Notice{EventID: eventID(event), Type: monitoring.NoticeCreated, SchemaVersion: "1", OccurredAt: c.now.UTC(), References: r})
			if err != nil {
				return err
			}
			position++
			writes.Queue(`INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id) VALUES($1,$2,$3,$4,$5,$6,$7,NULL)`, org, position, eventID(event), c.corpusID, event.Kind, event.Resource, r.MatchID)
			writes.Queue(`INSERT INTO matches(organization,id,subscription_id,subscription_version_id,saved_query_id,saved_query_version_id,corpus_id,record_id,record_version_id,previous_match_id,evidence,position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULL,$10,$11)`, org, r.MatchID, in.SubscriptionID, in.SubscriptionVersionID, c.pinned.queryID, c.pinned.queryVersionID, c.corpusID, in.RecordID, in.VersionID, evidence, position)
			writes.Queue(`INSERT INTO deliveries(organization,id,match_id,destination_id,event_kind,event_id,window_start) VALUES($1,$2,$3,$4,$5,$6,NULL)`, org, r.DeliveryID, r.MatchID, c.pinned.destination, event.Kind, eventID(event))
			writes.Queue(`INSERT INTO monitoring_notices(organization,event_id,kind,match_id,record_id,record_version_id,subscription_id,subscription_version_id,delivery_id,previous_match_id,body,position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULL,$10,$11)`, org, eventID(event), event.Kind, r.MatchID, in.RecordID, in.VersionID, in.SubscriptionID, in.SubscriptionVersionID, r.DeliveryID, body, position)
			writes.Queue(`INSERT INTO delivery_outbox(organization,delivery_id) VALUES($1,$2)`, org, r.DeliveryID)
		}
		// Keep individual completions ordered: the last one observes the earlier
		// completed intents and retains completeIntentSQL's evaluated_at rules.
		writes.Queue(completeIntentSQL, org, in.SubscriptionVersionID, in.Sequence, outcomes[i])
	}
	if writes.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, writes).Close()
}
