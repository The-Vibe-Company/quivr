package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

// claimRequest records a monitoring command under its idempotency key. It
// returns the resource of an identical earlier command, or "" when this
// command is new. Callers hold the Organization journal lock.
func claimRequest(ctx context.Context, tx pgx.Tx, org, family, key string, canonical []byte, resourceID string) (string, error) {
	var previous []byte
	var existing string
	err := tx.QueryRow(ctx, "SELECT canonical_request,resource_id FROM monitoring_requests WHERE organization=$1 AND route_family=$2 AND request_key=$3", org, family, key).Scan(&previous, &existing)
	if err == nil {
		if !bytes.Equal(previous, canonical) {
			return "", monitoring.ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	_, err = tx.Exec(ctx, "INSERT INTO monitoring_requests(organization,route_family,request_key,canonical_request,resource_id) VALUES($1,$2,$3,$4,$5)", org, family, key, canonical, resourceID)
	return "", err
}

// appendScopedEvents appends one public event per pinned Corpus, because the
// change feed is read per Corpus. They share the resource and differ only in
// their stable per-Corpus event ID. It returns the last position.
func appendScopedEvents(ctx context.Context, tx pgx.Tx, org, kind, resource, id string, corpusIDs []string) (int64, error) {
	var last int64
	for _, corpusID := range corpusIDs {
		position, err := appendEventAt(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: kind, Resource: resource, ResourceID: id, MutationID: id + "/" + corpusID})
		if err != nil {
			return 0, err
		}
		last = position
	}
	return last, nil
}

func (s ContentStore) CreateSavedQuery(ctx context.Context, org string, in monitoring.SavedQueryInput) (monitoring.SavedQuery, error) {
	canonical, err := json.Marshal(in)
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	definition, err := json.Marshal(in.Definition)
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	id := content.StableID("saved_query", org, in.Key)
	versionID := content.StableID("saved_query_version", org, id, "1")
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return monitoring.SavedQuery{}, err
	}
	existing, err := claimRequest(ctx, tx, org, "saved_queries", in.Key, canonical, id)
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	if existing == "" {
		if _, err = tx.Exec(ctx, "INSERT INTO saved_queries(organization,id,name,current_version_id) VALUES($1,$2,$3,$4)", org, id, in.Name, versionID); err != nil {
			return monitoring.SavedQuery{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO saved_query_versions(organization,saved_query_id,id,definition,corpus_ids) VALUES($1,$2,$3,$4,$5)", org, id, versionID, definition, in.Definition.CorpusIDs); err != nil {
			return monitoring.SavedQuery{}, err
		}
		if _, err = appendScopedEvents(ctx, tx, org, "saved_query.created", "saved_query", id, in.Definition.CorpusIDs); err != nil {
			return monitoring.SavedQuery{}, err
		}
		existing = id
	}
	if err = tx.Commit(ctx); err != nil {
		return monitoring.SavedQuery{}, err
	}
	return s.SavedQuery(ctx, org, existing)
}

func (s ContentStore) SavedQuery(ctx context.Context, org, id string) (monitoring.SavedQuery, error) {
	q := monitoring.SavedQuery{}
	var definition []byte
	err := s.Pool.QueryRow(ctx, `SELECT q.id,q.name,v.id,v.definition FROM saved_queries q JOIN saved_query_versions v ON v.organization=q.organization AND v.id=q.current_version_id WHERE q.organization=$1 AND q.id=$2`, org, id).Scan(&q.ID, &q.Name, &q.Current.VersionID, &definition)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, monitoring.ErrNotFound
	}
	if err != nil {
		return q, err
	}
	q.Current.SavedQueryID = q.ID
	err = unmarshalNumbers(definition, &q.Current.Definition)
	return q, err
}

func (s ContentStore) CreateSubscription(ctx context.Context, org string, in monitoring.SubscriptionInput, query monitoring.SavedQueryVersion) (monitoring.Subscription, error) {
	canonical, err := json.Marshal(in)
	if err != nil {
		return monitoring.Subscription{}, err
	}
	evaluator, err := json.Marshal(in.Evaluator)
	if err != nil {
		return monitoring.Subscription{}, err
	}
	id := content.StableID("subscription", org, in.Key)
	versionID := content.StableID("subscription_version", org, id, "1")
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return monitoring.Subscription{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return monitoring.Subscription{}, err
	}
	existing, err := claimRequest(ctx, tx, org, "subscriptions", in.Key, canonical, id)
	if err != nil {
		return monitoring.Subscription{}, err
	}
	if existing == "" {
		if _, err = tx.Exec(ctx, "INSERT INTO subscriptions(organization,id,name,current_version_id) VALUES($1,$2,$3,$4)", org, id, in.Name, versionID); err != nil {
			return monitoring.Subscription{}, err
		}
		for _, corpusID := range query.Definition.CorpusIDs {
			if _, err = tx.Exec(ctx, "INSERT INTO subscription_corpora(organization,corpus_id,subscription_id) VALUES($1,$2,$3)", org, corpusID, id); err != nil {
				return monitoring.Subscription{}, err
			}
		}
		// Activation commits its public event and boundary together; the journal
		// lock orders every earlier change below it and every later one above it.
		boundary, err := appendScopedEvents(ctx, tx, org, "subscription.created", "subscription", id, query.Definition.CorpusIDs)
		if err != nil {
			return monitoring.Subscription{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO subscription_versions(organization,subscription_id,id,saved_query_id,saved_query_version_id,evaluator,destination_id,activation_position) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", org, id, versionID, query.SavedQueryID, query.VersionID, evaluator, in.DestinationID, boundary); err != nil {
			return monitoring.Subscription{}, err
		}
		existing = id
	}
	if err = tx.Commit(ctx); err != nil {
		return monitoring.Subscription{}, err
	}
	return s.Subscription(ctx, org, existing)
}

func (s ContentStore) Subscription(ctx context.Context, org, id string) (monitoring.Subscription, error) {
	sub := monitoring.Subscription{}
	v := &sub.Current
	var evaluator []byte
	err := s.Pool.QueryRow(ctx, `SELECT s.id,s.name,s.enabled,v.id,v.saved_query_id,v.saved_query_version_id,v.evaluator,v.destination_id,v.activation_position,q.corpus_ids
FROM subscriptions s
JOIN subscription_versions v ON v.organization=s.organization AND v.id=s.current_version_id
JOIN saved_query_versions q ON q.organization=v.organization AND q.id=v.saved_query_version_id
WHERE s.organization=$1 AND s.id=$2`, org, id).Scan(&sub.ID, &sub.Name, &sub.Enabled, &v.VersionID, &v.SavedQueryID, &v.SavedQueryVersionID, &evaluator, &v.DestinationID, &v.ActivationPosition, &v.CorpusIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return sub, monitoring.ErrNotFound
	}
	if err != nil {
		return sub, err
	}
	v.SubscriptionID = sub.ID
	err = unmarshalNumbers(evaluator, &v.Evaluator)
	return sub, err
}

// DisableSubscription commits the enabled-state change and its public event
// once. The row update serializes with later Match and Delivery admission.
func (s ContentStore) DisableSubscription(ctx context.Context, org, key, id string) (monitoring.Subscription, error) {
	canonical, err := json.Marshal(map[string]string{"subscription_id": id})
	if err != nil {
		return monitoring.Subscription{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return monitoring.Subscription{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return monitoring.Subscription{}, err
	}
	existing, err := claimRequest(ctx, tx, org, "subscription_disable", key, canonical, id)
	if err != nil {
		return monitoring.Subscription{}, err
	}
	if existing == "" {
		changed, err := tx.Exec(ctx, "UPDATE subscriptions SET enabled=false WHERE organization=$1 AND id=$2 AND enabled", org, id)
		if err != nil {
			return monitoring.Subscription{}, err
		}
		if changed.RowsAffected() == 1 {
			var corpora []string
			if err = tx.QueryRow(ctx, "SELECT coalesce(array_agg(corpus_id ORDER BY corpus_id),'{}') FROM subscription_corpora WHERE organization=$1 AND subscription_id=$2", org, id).Scan(&corpora); err != nil {
				return monitoring.Subscription{}, err
			}
			position, err := appendScopedEvents(ctx, tx, org, "subscription.disabled", "subscription", id, corpora)
			if err != nil {
				return monitoring.Subscription{}, err
			}
			if _, err = tx.Exec(ctx, "UPDATE subscriptions SET disabled_position=$3 WHERE organization=$1 AND id=$2", org, id, position); err != nil {
				return monitoring.Subscription{}, err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return monitoring.Subscription{}, err
	}
	return s.Subscription(ctx, org, id)
}

// unmarshalNumbers preserves pinned plugin-defined numbers exactly.
func unmarshalNumbers(data []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(v)
}
