package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

func (s ContentStore) EvaluationBacklog(ctx context.Context, org string, corpora []string, after string, limit int) ([]monitoring.EvaluationCounts, error) {
	rows, err := s.Pool.Query(ctx, `SELECT v.evaluator->>'plugin_id',v.evaluator->>'version',
count(*) FILTER (WHERE i.state='pending'),
count(*) FILTER (WHERE i.state='pending' AND i.error_code<>''),
count(*) FILTER (WHERE i.state='pending' AND i.error_code='evaluator_unavailable'),
count(*) FILTER (WHERE i.state='done' AND i.outcome='evaluator_retired')
FROM evaluation_intents i JOIN subscription_versions v ON (v.organization,v.id)=(i.organization,i.subscription_version_id)
WHERE i.organization=$1 AND i.kind='evaluation' AND ($2::text[] IS NULL OR i.corpus_id=ANY($2))
AND (i.state='pending' OR i.outcome='evaluator_retired')
AND (v.evaluator->>'plugin_id')||'@'||(v.evaluator->>'version')>$3
GROUP BY v.evaluator->>'plugin_id',v.evaluator->>'version'
ORDER BY (v.evaluator->>'plugin_id')||'@'||(v.evaluator->>'version') LIMIT $4`, org, corpora, after, limit)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitoring.EvaluationCounts, error) {
		var counts monitoring.EvaluationCounts
		err := row.Scan(&counts.PluginID, &counts.Version, &counts.Pending, &counts.Erroring, &counts.Unavailable, &counts.Retired)
		return counts, err
	})
	if items == nil {
		items = []monitoring.EvaluationCounts{}
	}
	return items, err
}

const unavailableEvaluationsSQL = `FROM evaluation_intents i
JOIN subscription_versions v ON (v.organization,v.id)=(i.organization,i.subscription_version_id)
WHERE i.organization=$1 AND ($2::text[] IS NULL OR i.corpus_id=ANY($2))
AND v.evaluator->>'plugin_id'=$3 AND v.evaluator->>'version'=$4
AND i.kind='evaluation' AND i.state='pending' AND i.error_code='evaluator_unavailable'`

type retirementRequest struct {
	Input   monitoring.EvaluationRetirementInput `json:"input"`
	Corpora []string                             `json:"corpora"`
}

func (s ContentStore) RetireEvaluations(ctx context.Context, org string, corpora []string, in monitoring.EvaluationRetirementInput) (monitoring.EvaluationRetirement, error) {
	out := monitoring.EvaluationRetirement{EvaluationRetirementInput: in, ID: content.StableID("evaluation_retirement", org, in.Key), Outcome: monitoring.OutcomeEvaluatorRetired, Items: []monitoring.RetiredEvaluation{}}
	canonical, err := json.Marshal(retirementRequest{Input: in, Corpora: corpora})
	if err != nil {
		return out, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if in.DryRun {
		var position int64
		err = tx.QueryRow(ctx, `SELECT last_sequence FROM organization_journals WHERE organization=$1 FOR UPDATE`, org).Scan(&position)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
	} else {
		err = lockJournal(ctx, tx, org)
	}
	if err != nil {
		return out, err
	}
	if !in.DryRun {
		var previous, result []byte
		err = tx.QueryRow(ctx, `SELECT canonical_request,result FROM evaluation_retirements WHERE organization=$1 AND request_key=$2`, org, in.Key).Scan(&previous, &result)
		if err == nil {
			if !bytes.Equal(previous, canonical) {
				return out, monitoring.ErrConflict
			}
			err = json.Unmarshal(result, &out)
			return out, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT i.subscription_id,i.subscription_version_id,i.sequence,i.corpus_id,i.record_id,i.record_version_id `+unavailableEvaluationsSQL+`
AND i.lease_until<now() ORDER BY i.sequence,i.subscription_version_id LIMIT $5 FOR UPDATE OF i SKIP LOCKED`, org, corpora, in.PluginID, in.Version, in.Limit)
	if err != nil {
		return out, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitoring.RetiredEvaluation, error) {
		var item monitoring.RetiredEvaluation
		err := row.Scan(&item.SubscriptionID, &item.SubscriptionVersionID, &item.Sequence, &item.CorpusID, &item.RecordID, &item.RecordVersionID)
		return item, err
	})
	if err != nil {
		return out, err
	}
	out.Items = append(out.Items, items...)
	for index := range out.Items {
		item := &out.Items[index]
		if in.DryRun {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE evaluation_intents SET state='done',outcome='evaluator_retired',error_code='',lease_until='-infinity' WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3`, org, item.SubscriptionVersionID, item.Sequence); err != nil {
			return out, err
		}
		event := eventInput{Organization: org, CorpusID: item.CorpusID, Kind: "evaluation.retired", Resource: "evaluation_retirement", ResourceID: out.ID, MutationID: fmt.Sprintf("%s/%s/%d", out.ID, item.SubscriptionVersionID, item.Sequence), VersionID: item.RecordVersionID}
		if err = appendEvent(ctx, tx, event); err != nil {
			return out, err
		}
		item.EventID = eventID(event)
	}
	if err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE i.lease_until>=now()) `+unavailableEvaluationsSQL, org, corpora, in.PluginID, in.Version).Scan(&out.Remaining, &out.Leased); err != nil {
		return out, err
	}
	if in.DryRun {
		out.Remaining -= len(out.Items)
		return out, nil
	}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.CreatedAt); err != nil {
		return out, err
	}
	created := out.CreatedAt.UTC()
	out.CreatedAt = &created
	result, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO evaluation_retirements(organization,id,request_key,canonical_request,result) VALUES($1,$2,$3,$4,$5)`, org, out.ID, in.Key, canonical, result); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s ContentStore) EvaluationRetirement(ctx context.Context, org string, corpora []string, id string) (monitoring.EvaluationRetirement, error) {
	var out monitoring.EvaluationRetirement
	var canonical, result []byte
	err := s.Pool.QueryRow(ctx, `SELECT canonical_request,result FROM evaluation_retirements WHERE organization=$1 AND id=$2`, org, id).Scan(&canonical, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, monitoring.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var request retirementRequest
	if err = json.Unmarshal(canonical, &request); err != nil {
		return out, err
	}
	if corpora != nil {
		if request.Corpora == nil {
			return out, monitoring.ErrNotFound
		}
		for _, corpusID := range request.Corpora {
			if !slices.Contains(corpora, corpusID) {
				return out, monitoring.ErrNotFound
			}
		}
	}
	err = json.Unmarshal(result, &out)
	return out, err
}
