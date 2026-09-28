// Package monitoring owns immutable Saved Query and Subscription Versions,
// Subscription activation from a committed journal boundary, and disabling.
// Evaluation, Matches and Deliveries are later slices built on these facts.
package monitoring

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

var (
	ErrForbidden            = errors.New("forbidden")
	ErrNotFound             = errors.New("not_found")
	ErrConflict             = errors.New("idempotency_conflict")
	ErrUnsupportedProfile   = errors.New("unsupported_profile")
	ErrUnsupportedEvaluator = errors.New("unsupported_evaluator")
	ErrUnknownDestination   = errors.New("unknown_destination")
	ErrUnknownSavedQuery    = errors.New("unknown_saved_query")
	ErrTooLarge             = errors.New("definition_too_large")
)

// The deterministic fixture evaluator is the only installed evaluator. It is
// a test double for notification mechanics, not a relevance algorithm.
const (
	FixtureEvaluator        = "quivr.fixture"
	FixtureEvaluatorVersion = "1"
)

const (
	// maxCorpora bounds a Saved Query scope like a search request.
	maxCorpora = 16
	// maxPinnedBytes bounds each plugin-interpreted object pinned in a Version.
	maxPinnedBytes = 16 << 10
)

// Definition is the immutable Saved Query Version content.
type Definition struct {
	CorpusIDs        []string       `json:"corpus_ids"`
	Expression       map[string]any `json:"expression"`
	RetrievalProfile string         `json:"retrieval_profile"`
	TemporalPolicy   string         `json:"temporal_policy"`
}

type SavedQueryVersion struct {
	SavedQueryID string
	VersionID    string
	Definition   Definition
}

type SavedQuery struct {
	ID      string
	Name    string
	Current SavedQueryVersion
}

// Evaluator pins an installed evaluator implementation, version and configuration.
type Evaluator struct {
	PluginID      string         `json:"plugin_id"`
	Version       string         `json:"version"`
	Configuration map[string]any `json:"configuration"`
}

type SubscriptionVersion struct {
	SubscriptionID      string
	VersionID           string
	SavedQueryID        string
	SavedQueryVersionID string
	Evaluator           Evaluator
	DestinationID       string
	// CorpusIDs is the pinned Saved Query scope, used for authorization.
	CorpusIDs []string
	// ActivationPosition is the journal position of the activation commit.
	// Only later eligible changes are evaluated; nothing earlier is scanned.
	ActivationPosition int64
}

// Subscription separates mutable enabled state from its immutable Version.
type Subscription struct {
	ID      string
	Name    string
	Enabled bool
	Current SubscriptionVersion
}

// Destination is one deployment-configured webhook receiver bound to an
// Organization. Its URL and secret are never accepted or returned by the API.
type Destination struct {
	Organization string `json:"organization"`
	URL          string `json:"url"`
	Secret       string `json:"secret,omitempty"`
	SecretEnv    string `json:"secret_env,omitempty"`
}

type SavedQueryInput struct {
	Key        string     `json:"idempotency_key"`
	Name       string     `json:"name"`
	Definition Definition `json:"definition"`
}

type SubscriptionInput struct {
	Key                 string    `json:"idempotency_key"`
	Name                string    `json:"name"`
	SavedQueryID        string    `json:"saved_query_id"`
	SavedQueryVersionID string    `json:"saved_query_version_id"`
	Evaluator           Evaluator `json:"evaluator"`
	DestinationID       string    `json:"destination_id"`
}

// Store persists definitions. Creation and disable are idempotent per
// Organization, route family and key: the same canonical input returns the
// same resource in its current state, changed input returns ErrConflict.
// Each commit appends its public events to the Organization journal.
type Store interface {
	CreateSavedQuery(ctx context.Context, org string, in SavedQueryInput) (SavedQuery, error)
	SavedQuery(ctx context.Context, org, id string) (SavedQuery, error)
	CreateSubscription(ctx context.Context, org string, in SubscriptionInput, query SavedQueryVersion) (Subscription, error)
	Subscription(ctx context.Context, org, id string) (Subscription, error)
	DisableSubscription(ctx context.Context, org, key, id string) (Subscription, error)
}

// CorpusAuthorizer confirms that every Corpus belongs to the Organization.
type CorpusAuthorizer interface {
	Authorize(ctx context.Context, scope corpus.Scope, ids []string) error
}

type Service struct {
	Store        Store
	Corpora      CorpusAuthorizer
	Destinations map[string]Destination
}

func (s Service) CreateSavedQuery(ctx context.Context, scope corpus.Scope, in SavedQueryInput) (SavedQuery, error) {
	if !scope.Allows("monitoring:write") {
		return SavedQuery{}, ErrForbidden
	}
	d := in.Definition
	if len(d.CorpusIDs) == 0 || len(d.CorpusIDs) > maxCorpora {
		return SavedQuery{}, ErrTooLarge
	}
	// The whole requested scope must be granted; a Corpus is never silently dropped.
	for _, id := range d.CorpusIDs {
		if !scope.Contains(id) {
			return SavedQuery{}, ErrForbidden
		}
	}
	if d.RetrievalProfile != "balanced" {
		return SavedQuery{}, ErrUnsupportedProfile
	}
	if tooLarge(d.Expression) {
		return SavedQuery{}, ErrTooLarge
	}
	if err := s.Corpora.Authorize(ctx, scope, d.CorpusIDs); err != nil {
		if errors.Is(err, corpus.ErrForbidden) {
			return SavedQuery{}, ErrForbidden
		}
		return SavedQuery{}, err
	}
	return s.Store.CreateSavedQuery(ctx, scope.Organization, in)
}

func (s Service) SavedQuery(ctx context.Context, scope corpus.Scope, id string) (SavedQuery, error) {
	if !scope.Allows("monitoring:read") {
		return SavedQuery{}, ErrForbidden
	}
	q, err := s.Store.SavedQuery(ctx, scope.Organization, id)
	if err != nil {
		return SavedQuery{}, err
	}
	if !covers(scope, q.Current.Definition.CorpusIDs) {
		return SavedQuery{}, ErrNotFound
	}
	return q, nil
}

// SavedQueryVersion reads a pinned definition. Versions are immutable and a
// Saved Query has exactly one Version until an editing flow exists.
func (s Service) SavedQueryVersion(ctx context.Context, scope corpus.Scope, id, versionID string) (SavedQueryVersion, error) {
	q, err := s.SavedQuery(ctx, scope, id)
	if err != nil {
		return SavedQueryVersion{}, err
	}
	if q.Current.VersionID != versionID {
		return SavedQueryVersion{}, ErrNotFound
	}
	return q.Current, nil
}

func (s Service) CreateSubscription(ctx context.Context, scope corpus.Scope, in SubscriptionInput) (Subscription, error) {
	if !scope.Allows("monitoring:write") {
		return Subscription{}, ErrForbidden
	}
	if in.Evaluator.PluginID != FixtureEvaluator || in.Evaluator.Version != FixtureEvaluatorVersion {
		return Subscription{}, ErrUnsupportedEvaluator
	}
	if tooLarge(in.Evaluator.Configuration) {
		return Subscription{}, ErrTooLarge
	}
	if d, ok := s.Destinations[in.DestinationID]; !ok || d.Organization != scope.Organization {
		return Subscription{}, ErrUnknownDestination
	}
	q, err := s.Store.SavedQuery(ctx, scope.Organization, in.SavedQueryID)
	if errors.Is(err, ErrNotFound) || (err == nil && q.Current.VersionID != in.SavedQueryVersionID) {
		return Subscription{}, ErrUnknownSavedQuery
	}
	if err != nil {
		return Subscription{}, err
	}
	for _, id := range q.Current.Definition.CorpusIDs {
		if !scope.Contains(id) {
			return Subscription{}, ErrForbidden
		}
	}
	return s.Store.CreateSubscription(ctx, scope.Organization, in, q.Current)
}

func (s Service) Subscription(ctx context.Context, scope corpus.Scope, id string) (Subscription, error) {
	if !scope.Allows("monitoring:read") {
		return Subscription{}, ErrForbidden
	}
	return s.visible(ctx, scope, id)
}

// SubscriptionVersion reads the immutable configuration used by historical Matches.
func (s Service) SubscriptionVersion(ctx context.Context, scope corpus.Scope, id, versionID string) (SubscriptionVersion, error) {
	sub, err := s.Subscription(ctx, scope, id)
	if err != nil {
		return SubscriptionVersion{}, err
	}
	if sub.Current.VersionID != versionID {
		return SubscriptionVersion{}, ErrNotFound
	}
	return sub.Current, nil
}

// DisableSubscription stops future activity. Repeating it is idempotent and
// replaying the original creation never reenables the Subscription.
func (s Service) DisableSubscription(ctx context.Context, scope corpus.Scope, key, id string) (Subscription, error) {
	if !scope.Allows("monitoring:write") {
		return Subscription{}, ErrForbidden
	}
	if _, err := s.visible(ctx, scope, id); err != nil {
		return Subscription{}, err
	}
	return s.Store.DisableSubscription(ctx, scope.Organization, key, id)
}

func (s Service) visible(ctx context.Context, scope corpus.Scope, id string) (Subscription, error) {
	sub, err := s.Store.Subscription(ctx, scope.Organization, id)
	if err != nil {
		return Subscription{}, err
	}
	if !covers(scope, sub.Current.CorpusIDs) {
		return Subscription{}, ErrNotFound
	}
	return sub, nil
}

func covers(scope corpus.Scope, ids []string) bool {
	for _, id := range ids {
		if !scope.Contains(id) {
			return false
		}
	}
	return true
}

func tooLarge(v map[string]any) bool {
	b, err := json.Marshal(v)
	return err != nil || len(b) > maxPinnedBytes
}
