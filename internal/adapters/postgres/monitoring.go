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
	"github.com/jackc/pgx/v5/pgxpool"
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

// monitoringCommand runs one idempotent monitoring command under the
// Organization journal lock. apply runs only for a new command; any error
// rolls the claim back with it. It returns the claimed resource ID.
func (s MonitoringStore) monitoringCommand(ctx context.Context, org, family, key string, canonical any, resourceID string, apply func(pgx.Tx) error) (string, error) {
	request, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return "", err
	}
	existing, err := claimRequest(ctx, tx, org, family, key, request, resourceID)
	if err != nil {
		return "", err
	}
	if existing == "" {
		if err = apply(tx); err != nil {
			return "", err
		}
		existing = resourceID
	}
	return existing, tx.Commit(ctx)
}

// appendScopedEvents appends one public event per pinned Corpus, because the
// change feed is read per Corpus. They share the resource and differ only in
// their stable per-Corpus event ID, derived from mutation. It returns the last position.
func appendScopedEvents(ctx context.Context, tx pgx.Tx, org, kind, resource, id, mutation string, corpusIDs []string) (int64, error) {
	var last int64
	for _, corpusID := range corpusIDs {
		position, err := appendEventAt(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: kind, Resource: resource, ResourceID: id, MutationID: mutation + "/" + corpusID})
		if err != nil {
			return 0, err
		}
		last = position
	}
	return last, nil
}

// union returns the sorted distinct Corpus IDs of both scopes: an edit is
// announced in every Corpus it leaves or enters.
func union(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

func (s MonitoringStore) CreateSavedQuery(ctx context.Context, org string, in monitoring.SavedQueryInput) (monitoring.SavedQuery, error) {
	definition, err := json.Marshal(in.Definition)
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	id := content.StableID("saved_query", org, in.Key)
	versionID := content.StableID("saved_query_version", org, id, "1")
	vectors, err := s.prepareQueryVectors(ctx, org, "saved_queries", in.Key, in, in.QueryVectors, in.PrepareVectors)
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	existing, err := s.monitoringCommand(ctx, org, "saved_queries", in.Key, in, id, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO saved_queries(organization,id,name,current_version_id) VALUES($1,$2,$3,$4)", org, id, in.Name, versionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO saved_query_versions(organization,saved_query_id,id,definition,corpus_ids,query_vectors) VALUES($1,$2,$3,$4,$5,$6)", org, id, versionID, definition, in.Definition.CorpusIDs, vectors); err != nil {
			return err
		}
		_, err := appendScopedEvents(ctx, tx, org, "saved_query.created", "saved_query", id, id, in.Definition.CorpusIDs)
		return err
	})
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	return s.SavedQuery(ctx, org, existing)
}

func (s MonitoringStore) SavedQuery(ctx context.Context, org, id string) (monitoring.SavedQuery, error) {
	q := monitoring.SavedQuery{}
	var definition, vectors []byte
	err := s.Pool.QueryRow(ctx, `SELECT q.id,q.name,q.deleted,v.id,v.definition,v.query_vectors FROM saved_queries q JOIN saved_query_versions v ON v.organization=q.organization AND v.id=q.current_version_id WHERE q.organization=$1 AND q.id=$2`, org, id).Scan(&q.ID, &q.Name, &q.Deleted, &q.Current.VersionID, &definition, &vectors)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, monitoring.ErrNotFound
	}
	if err != nil {
		return q, err
	}
	q.Current.SavedQueryID = q.ID
	if err := json.Unmarshal(vectors, &q.Current.QueryVectors); err != nil {
		return q, err
	}
	err = unmarshalNumbers(definition, &q.Current.Definition)
	return q, err
}

func (s MonitoringStore) SavedQueryVersion(ctx context.Context, org, id, versionID string) (monitoring.SavedQueryVersion, error) {
	v := monitoring.SavedQueryVersion{SavedQueryID: id, VersionID: versionID}
	var definition, vectors []byte
	err := s.Pool.QueryRow(ctx, `SELECT definition,query_vectors FROM saved_query_versions WHERE organization=$1 AND saved_query_id=$2 AND id=$3`, org, id, versionID).Scan(&definition, &vectors)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, monitoring.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(vectors, &v.QueryVectors); err != nil {
		return v, err
	}
	return v, unmarshalNumbers(definition, &v.Definition)
}

// CreateSavedQueryVersion commits a new immutable Version, makes it current
// and announces saved_query.updated in every Corpus of the previous and new
// scope. Subscriptions keep the Version they pin.
func (s MonitoringStore) CreateSavedQueryVersion(ctx context.Context, org, id string, in monitoring.SavedQueryVersionInput) (monitoring.SavedQueryVersion, error) {
	definition, err := json.Marshal(in.Definition)
	if err != nil {
		return monitoring.SavedQueryVersion{}, err
	}
	versionID := content.StableID("saved_query_version", org, id, "edit", in.Key)
	canonical := struct {
		SavedQueryID string `json:"saved_query_id"`
		monitoring.SavedQueryVersionInput
	}{id, in}
	vectors, err := s.prepareQueryVectors(ctx, org, "saved_query_versions", in.Key, canonical, in.QueryVectors, in.PrepareVectors)
	if err != nil {
		return monitoring.SavedQueryVersion{}, err
	}
	existing, err := s.monitoringCommand(ctx, org, "saved_query_versions", in.Key, canonical, versionID, func(tx pgx.Tx) error {
		var deleted bool
		var previous []string
		err := tx.QueryRow(ctx, `SELECT q.deleted,v.corpus_ids FROM saved_queries q JOIN saved_query_versions v ON (v.organization,v.id)=(q.organization,q.current_version_id) WHERE q.organization=$1 AND q.id=$2`, org, id).Scan(&deleted, &previous)
		if errors.Is(err, pgx.ErrNoRows) {
			return monitoring.ErrNotFound
		}
		if err != nil {
			return err
		}
		if deleted {
			return monitoring.ErrSavedQueryDeleted
		}
		if _, err = tx.Exec(ctx, "INSERT INTO saved_query_versions(organization,saved_query_id,id,definition,corpus_ids,query_vectors) VALUES($1,$2,$3,$4,$5,$6)", org, id, versionID, definition, in.Definition.CorpusIDs, vectors); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE saved_queries SET current_version_id=$3 WHERE organization=$1 AND id=$2", org, id, versionID); err != nil {
			return err
		}
		_, err = appendScopedEvents(ctx, tx, org, "saved_query.updated", "saved_query", id, versionID, union(previous, in.Definition.CorpusIDs))
		return err
	})
	if err != nil {
		return monitoring.SavedQueryVersion{}, err
	}
	return s.SavedQueryVersion(ctx, org, id, existing)
}

func (s MonitoringStore) prepareQueryVectors(ctx context.Context, org, family, key string, canonical any, vectors []monitoring.QueryVector, prepare func(context.Context) ([]monitoring.QueryVector, error)) ([]byte, error) {
	if prepare != nil {
		request, err := json.Marshal(canonical)
		if err != nil {
			return nil, err
		}
		var previous []byte
		err = s.Pool.QueryRow(ctx, "SELECT canonical_request FROM monitoring_requests WHERE organization=$1 AND route_family=$2 AND request_key=$3", org, family, key).Scan(&previous)
		if err == nil {
			if !bytes.Equal(previous, request) {
				return nil, monitoring.ErrConflict
			}
			return nil, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		vectors, err = prepare(ctx)
		if err != nil {
			return nil, err
		}
	}
	if vectors == nil {
		vectors = []monitoring.QueryVector{}
	}
	return json.Marshal(vectors)
}

// DeleteSavedQuery logically deletes a Saved Query once, when no Subscription
// that is not deleted belongs to it. Subscription creation and editing take
// the same journal lock, so none can pin it concurrently.
func (s MonitoringStore) DeleteSavedQuery(ctx context.Context, org, key, id string) (monitoring.SavedQuery, error) {
	existing, err := s.monitoringCommand(ctx, org, "saved_query_delete", key, map[string]string{"saved_query_id": id}, id, func(tx pgx.Tx) error {
		var deleted, used bool
		var corpora []string
		err := tx.QueryRow(ctx, `SELECT q.deleted,v.corpus_ids,
  EXISTS(SELECT 1 FROM subscriptions s JOIN subscription_versions sv ON (sv.organization,sv.id)=(s.organization,s.current_version_id) WHERE s.organization=q.organization AND sv.saved_query_id=q.id AND NOT s.deleted)
FROM saved_queries q JOIN saved_query_versions v ON (v.organization,v.id)=(q.organization,q.current_version_id) WHERE q.organization=$1 AND q.id=$2`, org, id).Scan(&deleted, &corpora, &used)
		if errors.Is(err, pgx.ErrNoRows) {
			return monitoring.ErrNotFound
		}
		if err != nil || deleted {
			return err
		}
		if used {
			return monitoring.ErrSavedQueryInUse
		}
		if _, err = tx.Exec(ctx, "UPDATE saved_queries SET deleted=true WHERE organization=$1 AND id=$2", org, id); err != nil {
			return err
		}
		_, err = appendScopedEvents(ctx, tx, org, "saved_query.deleted", "saved_query", id, id, corpora)
		return err
	})
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	return s.SavedQuery(ctx, org, existing)
}

// RenameSavedQuery changes a Saved Query's display name once per key. A new
// name commits saved_query.renamed per Corpus of the current Version, whose
// identity names the request; the same name commits nothing.
func (s MonitoringStore) RenameSavedQuery(ctx context.Context, org, key, id, name string) (monitoring.SavedQuery, error) {
	existing, err := s.monitoringCommand(ctx, org, "saved_query_rename", key, map[string]string{"saved_query_id": id, "name": name}, id, func(tx pgx.Tx) error {
		var deleted bool
		var current string
		var corpora []string
		err := tx.QueryRow(ctx, `SELECT q.deleted,q.name,v.corpus_ids FROM saved_queries q JOIN saved_query_versions v ON (v.organization,v.id)=(q.organization,q.current_version_id) WHERE q.organization=$1 AND q.id=$2`, org, id).Scan(&deleted, &current, &corpora)
		if errors.Is(err, pgx.ErrNoRows) {
			return monitoring.ErrNotFound
		}
		if err != nil {
			return err
		}
		if deleted {
			return monitoring.ErrSavedQueryDeleted
		}
		if current == name {
			return nil
		}
		if _, err = tx.Exec(ctx, "UPDATE saved_queries SET name=$3 WHERE organization=$1 AND id=$2", org, id, name); err != nil {
			return err
		}
		_, err = appendScopedEvents(ctx, tx, org, "saved_query.renamed", "saved_query", id, id+"/rename/"+key, corpora)
		return err
	})
	if err != nil {
		return monitoring.SavedQuery{}, err
	}
	return s.SavedQuery(ctx, org, existing)
}

// pinnableQuery checks, under the journal lock, that a new Subscription
// Version may pin query: its Saved Query is not deleted and query is current.
func pinnableQuery(ctx context.Context, tx pgx.Tx, org string, query monitoring.SavedQueryVersion) error {
	var deleted bool
	var current string
	err := tx.QueryRow(ctx, `SELECT deleted,current_version_id FROM saved_queries WHERE organization=$1 AND id=$2`, org, query.SavedQueryID).Scan(&deleted, &current)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (deleted || current != query.VersionID)) {
		return monitoring.ErrUnknownSavedQuery
	}
	return err
}

// scopeCorpora records Corpora pinned by a Subscription Version. The rows are
// never removed: they cover every Corpus of every Version, so dispatch finds
// triggers that an earlier Version still judges and withdrawals of Records it
// alerted about.
func scopeCorpora(ctx context.Context, tx pgx.Tx, org, id string, corpusIDs []string) error {
	for _, corpusID := range corpusIDs {
		if _, err := tx.Exec(ctx, "INSERT INTO subscription_corpora(organization,corpus_id,subscription_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", org, corpusID, id); err != nil {
			return err
		}
	}
	return nil
}

// nullable stores "" as NULL.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// CreateSubscription commits a new enabled Subscription with its fixed owner
// (NULL when global).
func (s MonitoringStore) CreateSubscription(ctx context.Context, org string, in monitoring.SubscriptionInput, query monitoring.SavedQueryVersion) (monitoring.Subscription, error) {
	evaluator, err := json.Marshal(in.Evaluator)
	if err != nil {
		return monitoring.Subscription{}, err
	}
	id := content.StableID("subscription", org, in.Key)
	versionID := content.StableID("subscription_version", org, id, "1")
	existing, err := s.monitoringCommand(ctx, org, "subscriptions", in.Key, in, id, func(tx pgx.Tx) error {
		if err := pinnableQuery(ctx, tx, org, query); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO subscriptions(organization,id,name,current_version_id,owner) VALUES($1,$2,$3,$4,$5)", org, id, in.Name, versionID, nullable(in.Owner)); err != nil {
			return err
		}
		if err := scopeCorpora(ctx, tx, org, id, query.Definition.CorpusIDs); err != nil {
			return err
		}
		// Activation commits its public event and boundary together; the journal
		// lock orders every earlier change below it and every later one above it.
		boundary, err := appendScopedEvents(ctx, tx, org, "subscription.created", "subscription", id, id, query.Definition.CorpusIDs)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO subscription_versions(organization,subscription_id,id,saved_query_id,saved_query_version_id,evaluator,destination_id,activation_position) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", org, id, versionID, query.SavedQueryID, query.VersionID, evaluator, in.DestinationID, boundary)
		return err
	})
	if err != nil {
		return monitoring.Subscription{}, err
	}
	return s.Subscription(ctx, org, existing)
}

// subscriptionVersionColumns reads a Version v of the Subscription s.
const subscriptionVersionColumns = `v.id,coalesce(s.owner,''),v.saved_query_id,v.saved_query_version_id,v.evaluator,v.destination_id,v.activation_position,q.corpus_ids`

func scanSubscriptionVersion(v *monitoring.SubscriptionVersion, evaluator *[]byte) []any {
	return []any{&v.VersionID, &v.Owner, &v.SavedQueryID, &v.SavedQueryVersionID, evaluator, &v.DestinationID, &v.ActivationPosition, &v.CorpusIDs}
}

// subscriptionSelect reads Subscriptions s with their current Version and
// every pinned Corpus; callers append the WHERE clause.
const subscriptionSelect = `SELECT s.id,s.name,s.enabled,s.deleted,
  coalesce((SELECT array_agg(sc.corpus_id ORDER BY sc.corpus_id) FROM subscription_corpora sc WHERE sc.organization=s.organization AND sc.subscription_id=s.id),'{}'),` + subscriptionVersionColumns + `
FROM subscriptions s
JOIN subscription_versions v ON v.organization=s.organization AND v.id=s.current_version_id
JOIN saved_query_versions q ON q.organization=v.organization AND q.id=v.saved_query_version_id
`

func scanSubscription(row pgx.Row) (monitoring.Subscription, error) {
	sub := monitoring.Subscription{}
	v := &sub.Current
	var evaluator []byte
	if err := row.Scan(append([]any{&sub.ID, &sub.Name, &sub.Enabled, &sub.Deleted, &sub.PinnedCorpusIDs}, scanSubscriptionVersion(v, &evaluator)...)...); err != nil {
		return sub, err
	}
	v.SubscriptionID, sub.Owner = sub.ID, v.Owner
	return sub, unmarshalNumbers(evaluator, &v.Evaluator)
}

func (s MonitoringStore) Subscription(ctx context.Context, org, id string) (monitoring.Subscription, error) {
	sub, err := scanSubscription(s.Pool.QueryRow(ctx, subscriptionSelect+`WHERE s.organization=$1 AND s.id=$2`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return sub, monitoring.ErrNotFound
	}
	return sub, err
}

func (s MonitoringStore) SubscriptionVersion(ctx context.Context, org, id, versionID string) (monitoring.SubscriptionVersion, error) {
	v := monitoring.SubscriptionVersion{SubscriptionID: id}
	var evaluator []byte
	err := s.Pool.QueryRow(ctx, `SELECT `+subscriptionVersionColumns+`
FROM subscription_versions v
JOIN subscriptions s ON s.organization=v.organization AND s.id=v.subscription_id
JOIN saved_query_versions q ON q.organization=v.organization AND q.id=v.saved_query_version_id
WHERE v.organization=$1 AND v.subscription_id=$2 AND v.id=$3`, org, id, versionID).Scan(scanSubscriptionVersion(&v, &evaluator)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, monitoring.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	return v, unmarshalNumbers(evaluator, &v.Evaluator)
}

// CreateSubscriptionVersion commits a new immutable Version and makes it
// current, with subscription.updated in every Corpus of the previous and new
// scope. Like creation, the Version records the journal position of that
// commit as its activation: every later change is judged by it, every earlier
// one by the Version effective before. Nothing is backfilled and Matches keep
// the Version that produced them. Enabled state is unchanged.
func (s MonitoringStore) CreateSubscriptionVersion(ctx context.Context, org, id string, in monitoring.SubscriptionVersionInput, query monitoring.SavedQueryVersion) (monitoring.SubscriptionVersion, error) {
	evaluator, err := json.Marshal(in.Evaluator)
	if err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	versionID := content.StableID("subscription_version", org, id, "edit", in.Key)
	canonical := struct {
		SubscriptionID string `json:"subscription_id"`
		monitoring.SubscriptionVersionInput
	}{id, in}
	existing, err := s.monitoringCommand(ctx, org, "subscription_versions", in.Key, canonical, versionID, func(tx pgx.Tx) error {
		deleted, previous, err := subscriptionScope(ctx, tx, org, id)
		if err != nil {
			return err
		}
		if deleted {
			return monitoring.ErrSubscriptionDeleted
		}
		if err = pinnableQuery(ctx, tx, org, query); err != nil {
			return err
		}
		if err = scopeCorpora(ctx, tx, org, id, query.Definition.CorpusIDs); err != nil {
			return err
		}
		boundary, err := appendScopedEvents(ctx, tx, org, "subscription.updated", "subscription", id, versionID, union(previous, query.Definition.CorpusIDs))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO subscription_versions(organization,subscription_id,id,saved_query_id,saved_query_version_id,evaluator,destination_id,activation_position) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", org, id, versionID, query.SavedQueryID, query.VersionID, evaluator, in.DestinationID, boundary); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE subscriptions SET current_version_id=$3 WHERE organization=$1 AND id=$2", org, id, versionID)
		return err
	})
	if err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	return s.SubscriptionVersion(ctx, org, id, existing)
}

// subscriptionScope locks a Subscription row and reads whether it is deleted
// and the sorted Corpora of its current Version.
func subscriptionScope(ctx context.Context, tx pgx.Tx, org, id string) (bool, []string, error) {
	var deleted bool
	var corpora []string
	err := tx.QueryRow(ctx, `SELECT s.deleted,q.corpus_ids FROM subscriptions s
JOIN subscription_versions v ON (v.organization,v.id)=(s.organization,s.current_version_id)
JOIN saved_query_versions q ON (q.organization,q.id)=(v.organization,v.saved_query_version_id)
WHERE s.organization=$1 AND s.id=$2 FOR UPDATE OF s`, org, id).Scan(&deleted, &corpora)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, monitoring.ErrNotFound
	}
	slices.Sort(corpora)
	return deleted, corpora, err
}

// DisableSubscription commits the enabled-state change and its public event
// once. The row update serializes with later Match and Delivery admission.
func (s MonitoringStore) DisableSubscription(ctx context.Context, org, key, id string) (monitoring.Subscription, error) {
	_, err := s.monitoringCommand(ctx, org, "subscription_disable", key, map[string]string{"subscription_id": id}, id, func(tx pgx.Tx) error {
		changed, err := tx.Exec(ctx, "UPDATE subscriptions SET enabled=false WHERE organization=$1 AND id=$2 AND enabled", org, id)
		if err != nil || changed.RowsAffected() != 1 {
			return err
		}
		// A disable after a re-enable is a distinct fact: its event
		// identity names the re-enable it follows. The first keeps its own.
		corpora, mutation, err := toggleScope(ctx, tx, org, id, "enabled_position")
		if err != nil {
			return err
		}
		position, err := appendScopedEvents(ctx, tx, org, "subscription.disabled", "subscription", id, mutation, corpora)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE subscriptions SET disabled_position=$3 WHERE organization=$1 AND id=$2", org, id, position)
		return err
	})
	if err != nil {
		return monitoring.Subscription{}, err
	}
	return s.Subscription(ctx, org, id)
}

// EnableSubscription re-enables a disabled Subscription once, under the
// journal lock that Match commits and Delivery admission also take. It commits
// subscription.enabled per pinned Corpus and records its position, after which
// evaluation resumes (the pause is never backfilled), and it makes the
// Subscription's parked Delivery work due now: every claim is admitted again
// through the unchanged canonical checks and delivery window, so refused work
// (superseded, withdrawn Record) parks again and elapsed work ends exhausted.
// A deleted Subscription is never re-enabled.
func (s MonitoringStore) EnableSubscription(ctx context.Context, org, key, id string) (monitoring.Subscription, error) {
	_, err := s.monitoringCommand(ctx, org, "subscription_enable", key, map[string]string{"subscription_id": id}, id, func(tx pgx.Tx) error {
		deleted, _, err := subscriptionScope(ctx, tx, org, id)
		if err != nil {
			return err
		}
		if deleted {
			return monitoring.ErrSubscriptionDeleted
		}
		changed, err := tx.Exec(ctx, "UPDATE subscriptions SET enabled=true WHERE organization=$1 AND id=$2 AND NOT enabled", org, id)
		if err != nil || changed.RowsAffected() != 1 {
			return err
		}
		// Each re-enable is a distinct fact: its event identity names the
		// disable it follows.
		corpora, mutation, err := toggleScope(ctx, tx, org, id, "disabled_position")
		if err != nil {
			return err
		}
		position, err := appendScopedEvents(ctx, tx, org, "subscription.enabled", "subscription", id, mutation, corpora)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE subscriptions SET enabled_position=$3 WHERE organization=$1 AND id=$2", org, id, position); err != nil {
			return err
		}
		// Notices committed during the pause (withdrawals) open their
		// window now; work parked by the disable keeps its window.
		if _, err = tx.Exec(ctx, `UPDATE deliveries d SET window_start=now() FROM matches m
WHERE d.organization=$1 AND d.window_start='infinity' AND (m.organization,m.id)=(d.organization,d.match_id) AND m.subscription_id=$2`, org, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE delivery_outbox o SET available_at=now(),lease_until='-infinity'
FROM deliveries d JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
WHERE o.organization=$1 AND o.available_at='infinity' AND (d.organization,d.id)=(o.organization,o.delivery_id) AND m.subscription_id=$2`, org, id)
		return err
	})
	if err != nil {
		return monitoring.Subscription{}, err
	}
	return s.Subscription(ctx, org, id)
}

// DeleteSubscription logically deletes a Subscription once: it is disabled
// for good in the same row update, which serializes with Match commits and
// Delivery admission under the journal lock, and subscription.deleted is
// committed per Corpus of its current Version. A withdrawal notice for one of
// its Matches is still committed, as for a disabled Subscription (THE-696),
// but admission never attempts it. Versions, Matches and Deliveries remain.
func (s MonitoringStore) DeleteSubscription(ctx context.Context, org, key, id string) (monitoring.Subscription, error) {
	_, err := s.monitoringCommand(ctx, org, "subscription_delete", key, map[string]string{"subscription_id": id}, id, func(tx pgx.Tx) error {
		deleted, corpora, err := subscriptionScope(ctx, tx, org, id)
		if err != nil || deleted {
			return err
		}
		position, err := appendScopedEvents(ctx, tx, org, "subscription.deleted", "subscription", id, id, corpora)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE subscriptions SET deleted=true,enabled=false,deleted_position=$3 WHERE organization=$1 AND id=$2", org, id, position)
		return err
	})
	if err != nil {
		return monitoring.Subscription{}, err
	}
	return s.Subscription(ctx, org, id)
}

// RenameSubscription changes a Subscription's display name once per key. The
// name is not part of any Version, so evaluation, Matches and Deliveries are
// untouched. A new name commits subscription.renamed per Corpus of the
// current Version, whose identity names the request; the same name commits
// nothing.
func (s MonitoringStore) RenameSubscription(ctx context.Context, org, key, id, name string) (monitoring.Subscription, error) {
	_, err := s.monitoringCommand(ctx, org, "subscription_rename", key, map[string]string{"subscription_id": id, "name": name}, id, func(tx pgx.Tx) error {
		deleted, corpora, err := subscriptionScope(ctx, tx, org, id)
		if err != nil {
			return err
		}
		if deleted {
			return monitoring.ErrSubscriptionDeleted
		}
		changed, err := tx.Exec(ctx, "UPDATE subscriptions SET name=$3 WHERE organization=$1 AND id=$2 AND name<>$3", org, id, name)
		if err != nil || changed.RowsAffected() != 1 {
			return err
		}
		_, err = appendScopedEvents(ctx, tx, org, "subscription.renamed", "subscription", id, id+"/rename/"+key, corpora)
		return err
	})
	if err != nil {
		return monitoring.Subscription{}, err
	}
	return s.Subscription(ctx, org, id)
}

// Subscriptions pages the active Subscriptions of one owner, or the global
// ones, by ID, in one statement so filters and rows share a snapshot. A
// non-nil corpora keeps those whose every pinned Corpus, of any Version, it
// contains: the rule every Subscription read applies.
func (s MonitoringStore) Subscriptions(ctx context.Context, org string, owner monitoring.OwnerFilter, corpora []string, after string, limit int) ([]monitoring.Subscription, error) {
	rows, err := s.Pool.Query(ctx, subscriptionSelect+`WHERE s.organization=$1 AND s.enabled AND NOT s.deleted AND s.id>$2
  AND (CASE WHEN $3 THEN s.owner IS NULL ELSE s.owner=$4 END)
  AND ($5::text[] IS NULL OR NOT EXISTS(SELECT 1 FROM subscription_corpora sc WHERE sc.organization=s.organization AND sc.subscription_id=s.id AND NOT sc.corpus_id=ANY($5::text[])))
ORDER BY s.id LIMIT $6`, org, after, owner.Global, owner.Owner, corpora, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []monitoring.Subscription{}
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

var _ monitoring.EvaluatorMoves = MonitoringStore{}

// PinningSubscriptions lists the Subscriptions of org that are not deleted,
// enabled or not, whose current Version pins pluginID@version (THE-805).
func (s MonitoringStore) PinningSubscriptions(ctx context.Context, org, pluginID, version string, corpora []string, after string, limit int) ([]monitoring.Subscription, error) {
	rows, err := s.Pool.Query(ctx, subscriptionSelect+`WHERE s.organization=$1 AND NOT s.deleted AND s.id>$2
  AND v.evaluator->>'plugin_id'=$3 AND v.evaluator->>'version'=$4
  AND ($5::text[] IS NULL OR NOT EXISTS(SELECT 1 FROM subscription_corpora sc WHERE sc.organization=s.organization AND sc.subscription_id=s.id AND NOT sc.corpus_id=ANY($5::text[])))
ORDER BY s.id LIMIT $6`, org, after, pluginID, version, corpora, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitoring.Subscription, error) { return scanSubscription(row) })
}

// MoveEvaluator commits a new immutable Version of from's Subscription that
// keeps its Saved Query Version, configuration and destination and pins
// evaluator, and makes it current, with subscription.updated in every Corpus
// of its scope. Like an edit, the Version activates at that commit: later
// changes are judged by it, earlier ones by from, and Matches keep the
// Version that produced them. Its id derives from from and evaluator, so a
// replay converges; from must still be current.
func (s MonitoringStore) MoveEvaluator(ctx context.Context, org string, from monitoring.SubscriptionVersion, evaluator monitoring.Evaluator) (monitoring.SubscriptionVersion, error) {
	pinned, err := json.Marshal(evaluator)
	if err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	id := from.SubscriptionID
	versionID := content.StableID("subscription_version", org, id, "evaluator_move", from.VersionID, monitoring.EvaluatorKey(evaluator))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	deleted, corpora, err := subscriptionScope(ctx, tx, org, id)
	if err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	var current string
	if err = tx.QueryRow(ctx, "SELECT current_version_id FROM subscriptions WHERE organization=$1 AND id=$2", org, id).Scan(&current); err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	switch {
	case current == versionID:
		return s.SubscriptionVersion(ctx, org, id, versionID)
	case deleted:
		return monitoring.SubscriptionVersion{}, monitoring.ErrSubscriptionDeleted
	case current != from.VersionID:
		return monitoring.SubscriptionVersion{}, monitoring.ErrConflict
	}
	boundary, err := appendScopedEvents(ctx, tx, org, "subscription.updated", "subscription", id, versionID, corpora)
	if err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO subscription_versions(organization,subscription_id,id,saved_query_id,saved_query_version_id,evaluator,destination_id,activation_position)
SELECT organization,subscription_id,$3,saved_query_id,saved_query_version_id,$4,destination_id,$5 FROM subscription_versions WHERE organization=$1 AND id=$2`, org, from.VersionID, versionID, pinned, boundary); err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE subscriptions SET current_version_id=$3 WHERE organization=$1 AND id=$2", org, id, versionID); err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	return s.SubscriptionVersion(ctx, org, id, versionID)
}

// toggleScope reads the Corpora of a Subscription's current Version whose
// enabled state changes and the event identity of that change: the
// Subscription ID, suffixed with the position of the previous opposite change
// (column) when there is one.
func toggleScope(ctx context.Context, tx pgx.Tx, org, id, column string) ([]string, string, error) {
	_, corpora, err := subscriptionScope(ctx, tx, org, id)
	if err != nil {
		return nil, "", err
	}
	var previous *int64
	if err = tx.QueryRow(ctx, `SELECT `+column+` FROM subscriptions WHERE organization=$1 AND id=$2`, org, id).Scan(&previous); err != nil || previous == nil {
		return corpora, id, err
	}
	return corpora, fmt.Sprint(id, "@", *previous), nil
}

// unmarshalNumbers preserves pinned plugin-defined numbers exactly.
func unmarshalNumbers(data []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(v)
}

// MonitoringStore persists monitoring state.
type MonitoringStore struct{ Pool *pgxpool.Pool }
