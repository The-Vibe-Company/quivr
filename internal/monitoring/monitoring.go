// Package monitoring owns immutable Saved Query and Subscription Versions,
// Subscription activation from a committed journal boundary, editing by new
// Versions, disabling and logical deletion. Evaluation turns later eligible
// Versions into unique Matches with their pending logical Deliveries.
package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

var (
	ErrForbidden            = publicerr.New("forbidden")
	ErrNotFound             = publicerr.New("not_found")
	ErrConflict             = publicerr.New("idempotency_conflict")
	ErrUnsupportedProfile   = publicerr.New("unsupported_profile")
	ErrUnsupportedEvaluator = publicerr.New("unsupported_evaluator")
	ErrUnknownDestination   = publicerr.New("unknown_destination")
	ErrUnknownSavedQuery    = publicerr.New("unknown_saved_query")
	ErrTooLarge             = publicerr.New("definition_too_large")
	// ErrSubscriptionDeleted and ErrSavedQueryDeleted refuse commands that
	// would change a logically deleted resource; deletion is never undone.
	ErrSubscriptionDeleted = publicerr.New("subscription_deleted")
	ErrSavedQueryDeleted   = publicerr.New("saved_query_deleted")
	// ErrSavedQueryInUse refuses deleting a Saved Query that a Subscription
	// which is not deleted still belongs to.
	ErrSavedQueryInUse = publicerr.New("saved_query_in_use")
	// ErrInvalidOwner refuses a Subscription Owner that is not a bounded
	// printable reference, or the reserved "none".
	ErrInvalidOwner = publicerr.New("invalid_owner")
)

const (
	// maxCorpora bounds a Saved Query scope like a search request.
	maxCorpora = 16
	// maxPinnedBytes bounds each plugin-interpreted object pinned in a Version.
	maxPinnedBytes = 16 << 10
	// maxOwner bounds a Subscription Owner, in characters.
	maxOwner = 128
	// NoOwner is the reserved owner filter value naming global Subscriptions.
	NoOwner = "none"
)

// Definition is the immutable Saved Query Version content.
type Definition struct {
	CorpusIDs        []string       `json:"corpus_ids"`
	Expression       map[string]any `json:"expression"`
	RetrievalProfile string         `json:"retrieval_profile"`
	TemporalPolicy   string         `json:"temporal_policy"`
}

type SavedQueryVersion struct {
	QueryVectors []QueryVector
	SavedQueryID string
	VersionID    string
	Definition   Definition
}

// SavedQuery is a stable query identity whose current Version changes only
// by publishing a new immutable Version. A deleted one stays readable.
type SavedQuery struct {
	ID      string
	Name    string
	Deleted bool
	Current SavedQueryVersion
}

// Evaluator pins an installed evaluator implementation, version and configuration.
type Evaluator struct {
	PluginID      string         `json:"plugin_id"`
	Version       string         `json:"version"`
	Configuration map[string]any `json:"configuration"`
}

type SubscriptionVersion struct {
	SubscriptionID string
	VersionID      string
	// Owner is the Subscription's owner, fixed for every Version ("" when global).
	Owner               string
	SavedQueryID        string
	SavedQueryVersionID string
	Evaluator           Evaluator
	DestinationID       string
	// CorpusIDs is the pinned Saved Query scope, used for authorization.
	CorpusIDs []string
	// ActivationPosition is the journal position of the commit that created
	// this Version. It judges the changes after it, until a later Version's
	// activation; nothing earlier is scanned.
	ActivationPosition int64
}

// Subscription separates mutable enabled and deleted state from its immutable
// Versions. A deleted Subscription is disabled for good and stays readable.
type Subscription struct {
	ID   string
	Name string
	// Owner is the Subscription Owner, an opaque client-defined end-user
	// reference fixed at creation; "" for a global Subscription.
	Owner   string
	Enabled bool
	Deleted bool
	Current SubscriptionVersion
	// PinnedCorpusIDs are the Corpora of every Version, current or earlier.
	// Its Match history can concern any of them, so a key sees the
	// Subscription only when it grants them all.
	PinnedCorpusIDs []string
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
	PrepareVectors func(context.Context) ([]QueryVector, error) `json:"-"`
	QueryVectors   []QueryVector                                `json:"-"`
	Key            string                                       `json:"idempotency_key"`
	Name           string                                       `json:"name"`
	Definition     Definition                                   `json:"definition"`
}

// SavedQueryVersionInput publishes a new Version of an existing Saved Query.
type SavedQueryVersionInput struct {
	PrepareVectors func(context.Context) ([]QueryVector, error) `json:"-"`
	QueryVectors   []QueryVector                                `json:"-"`
	Key            string                                       `json:"idempotency_key"`
	Definition     Definition                                   `json:"definition"`
}

type SubscriptionInput struct {
	Key  string `json:"idempotency_key"`
	Name string `json:"name"`
	// Owner is optional; it is part of the idempotency-canonical request.
	Owner               string    `json:"owner,omitempty"`
	SavedQueryID        string    `json:"saved_query_id"`
	SavedQueryVersionID string    `json:"saved_query_version_id"`
	Evaluator           Evaluator `json:"evaluator"`
	DestinationID       string    `json:"destination_id"`
}

// SubscriptionVersionInput publishes a new Version of an existing
// Subscription. It may pin a newer Version of the same Saved Query.
type SubscriptionVersionInput struct {
	Key                 string    `json:"idempotency_key"`
	SavedQueryVersionID string    `json:"saved_query_version_id"`
	Evaluator           Evaluator `json:"evaluator"`
	DestinationID       string    `json:"destination_id"`
}

// Store persists definitions. Every command is idempotent per Organization,
// route family and key: the same canonical input returns the same result
// (the resource in its current state, or the Version it created), changed
// input returns ErrConflict. Each commit appends its public events to the
// Organization journal.
type Store interface {
	CreateSavedQuery(ctx context.Context, org string, in SavedQueryInput) (SavedQuery, error)
	SavedQuery(ctx context.Context, org, id string) (SavedQuery, error)
	// SavedQueryVersion reads any Version of a Saved Query.
	SavedQueryVersion(ctx context.Context, org, id, versionID string) (SavedQueryVersion, error)
	// CreateSavedQueryVersion makes a new Version current. It moves no
	// Subscription. A deleted Saved Query returns ErrSavedQueryDeleted.
	CreateSavedQueryVersion(ctx context.Context, org, id string, in SavedQueryVersionInput) (SavedQueryVersion, error)
	// DeleteSavedQuery logically deletes a Saved Query that no Subscription
	// which is not deleted belongs to, else returns ErrSavedQueryInUse.
	DeleteSavedQuery(ctx context.Context, org, key, id string) (SavedQuery, error)
	// RenameSavedQuery changes the display name; a deleted Saved Query
	// returns ErrSavedQueryDeleted.
	RenameSavedQuery(ctx context.Context, org, key, id, name string) (SavedQuery, error)
	// CreateSubscription pins query, which must still be its Saved Query's
	// current Version for a new Subscription (ErrUnknownSavedQuery otherwise).
	CreateSubscription(ctx context.Context, org string, in SubscriptionInput, query SavedQueryVersion) (Subscription, error)
	Subscription(ctx context.Context, org, id string) (Subscription, error)
	// SubscriptionVersion reads any Version of a Subscription.
	SubscriptionVersion(ctx context.Context, org, id, versionID string) (SubscriptionVersion, error)
	// CreateSubscriptionVersion makes a new Version current from its commit:
	// it judges only later changes. query must be the current Version of the
	// Subscription's Saved Query. A deleted Subscription returns
	// ErrSubscriptionDeleted.
	CreateSubscriptionVersion(ctx context.Context, org, id string, in SubscriptionVersionInput, query SavedQueryVersion) (SubscriptionVersion, error)
	DisableSubscription(ctx context.Context, org, key, id string) (Subscription, error)
	// EnableSubscription returns ErrSubscriptionDeleted for a deleted Subscription.
	EnableSubscription(ctx context.Context, org, key, id string) (Subscription, error)
	// DeleteSubscription logically deletes and disables a Subscription.
	DeleteSubscription(ctx context.Context, org, key, id string) (Subscription, error)
	// RenameSubscription changes the display name; a deleted Subscription
	// returns ErrSubscriptionDeleted.
	RenameSubscription(ctx context.Context, org, key, id, name string) (Subscription, error)
	// Subscriptions lists up to limit active (enabled, not deleted)
	// Subscriptions of owner with an ID after after, in ID order. A non-nil
	// corpora keeps only those whose every pinned Corpus it contains.
	Subscriptions(ctx context.Context, org string, owner OwnerFilter, corpora []string, after string, limit int) ([]Subscription, error)
}

// OwnerFilter selects the Subscriptions of one owner, or the global ones.
type OwnerFilter struct {
	Owner  string
	Global bool
}

// CorpusAuthorizer confirms that every Corpus belongs to the Organization.
type CorpusAuthorizer interface {
	Authorize(ctx context.Context, scope corpus.Scope, ids []string) error
}

// SearchProfiles reports whether the deployment answers a search profile,
// as GET /v0/search/profiles lists them.
type SearchProfiles interface {
	Serves(profile string) bool
}

type Service struct {
	QueryEncoder SavedQueryEncoder
	Store        Store
	Corpora      CorpusAuthorizer
	Destinations map[string]Destination
	// Profiles are the search profiles a definition may name; nil serves the
	// built-in default only.
	Profiles SearchProfiles
	// MatchStore reads Match history and Deliveries.
	MatchStore MatchStore
	// Evaluators are the installed evaluators: a new Subscription Version
	// pins one they serve, or keeps the one its Subscription already pins.
	Evaluators EvaluatorSet
	// Moves lists and moves the Subscriptions pinning an evaluator, for an
	// operator migration; without it migrations are not served.
	Moves       EvaluatorMoves
	Evaluations EvaluationAdministration
	// Recent and Versions read what a preview judges; without them previews
	// are not served.
	Recent   RecentReader
	Versions VersionReader
	// PreviewBudget bounds the evaluator calls of one preview (default 6 s).
	PreviewBudget time.Duration
}

func (s Service) CreateSavedQuery(ctx context.Context, scope corpus.Scope, in SavedQueryInput) (SavedQuery, error) {
	if !scope.Allows("monitoring:write") {
		return SavedQuery{}, ErrForbidden
	}
	if err := s.validDefinition(ctx, scope, in.Definition); err != nil {
		return SavedQuery{}, err
	}
	if s.QueryEncoder != nil {
		in.PrepareVectors = func(ctx context.Context) ([]QueryVector, error) {
			return s.QueryEncoder.EncodeSavedQuery(ctx, scope.Organization, in.Definition)
		}
	}
	return s.Store.CreateSavedQuery(ctx, scope.Organization, in)
}

// validDefinition checks a Saved Query Version definition written by scope.
func (s Service) validDefinition(ctx context.Context, scope corpus.Scope, d Definition) error {
	if len(d.CorpusIDs) == 0 || len(d.CorpusIDs) > maxCorpora {
		return ErrTooLarge
	}
	// The whole requested scope must be granted; a Corpus is never silently dropped.
	if !covers(scope, d.CorpusIDs) {
		return ErrForbidden
	}
	// Only a new definition must name a served profile: evaluation never reads
	// it, so a Version recorded under a profile since unpinned keeps running.
	if !s.serves(d.RetrievalProfile) {
		return ErrUnsupportedProfile
	}
	if tooLarge(d.Expression) {
		return ErrTooLarge
	}
	if err := s.Corpora.Authorize(ctx, scope, d.CorpusIDs); err != nil {
		if errors.Is(err, corpus.ErrForbidden) {
			return ErrForbidden
		}
		return err
	}
	return nil
}

func (s Service) serves(profile string) bool {
	if s.Profiles == nil {
		// balanced is the deprecated name of default until engine 0.2.0.
		return profile == "default" || profile == "balanced"
	}
	return s.Profiles.Serves(profile)
}

func (s Service) SavedQuery(ctx context.Context, scope corpus.Scope, id string) (SavedQuery, error) {
	if !scope.Allows("monitoring:read") {
		return SavedQuery{}, ErrForbidden
	}
	return s.visibleQuery(ctx, scope, id)
}

// visibleQuery reads a Saved Query whose current Corpora the key all grants.
func (s Service) visibleQuery(ctx context.Context, scope corpus.Scope, id string) (SavedQuery, error) {
	q, err := s.Store.SavedQuery(ctx, scope.Organization, id)
	if err != nil {
		return SavedQuery{}, err
	}
	if !covers(scope, q.Current.Definition.CorpusIDs) {
		return SavedQuery{}, ErrNotFound
	}
	return q, nil
}

// SavedQueryVersion reads any immutable Version of a visible Saved Query whose
// own Corpora the key also grants.
func (s Service) SavedQueryVersion(ctx context.Context, scope corpus.Scope, id, versionID string) (SavedQueryVersion, error) {
	if _, err := s.SavedQuery(ctx, scope, id); err != nil {
		return SavedQueryVersion{}, err
	}
	v, err := s.Store.SavedQueryVersion(ctx, scope.Organization, id, versionID)
	if err != nil {
		return SavedQueryVersion{}, err
	}
	if !covers(scope, v.Definition.CorpusIDs) {
		return SavedQueryVersion{}, ErrNotFound
	}
	return v, nil
}

// CreateSavedQueryVersion edits a Saved Query by publishing a new immutable
// Version that becomes current. Subscriptions keep the Version they pin until
// a new Subscription Version moves them. Replaying the same request returns
// the same Version.
func (s Service) CreateSavedQueryVersion(ctx context.Context, scope corpus.Scope, id string, in SavedQueryVersionInput) (SavedQueryVersion, error) {
	if !scope.Allows("monitoring:write") {
		return SavedQueryVersion{}, ErrForbidden
	}
	if _, err := s.visibleQuery(ctx, scope, id); err != nil {
		return SavedQueryVersion{}, err
	}
	if err := s.validDefinition(ctx, scope, in.Definition); err != nil {
		return SavedQueryVersion{}, err
	}
	if s.QueryEncoder != nil {
		in.PrepareVectors = func(ctx context.Context) ([]QueryVector, error) {
			return s.QueryEncoder.EncodeSavedQuery(ctx, scope.Organization, in.Definition)
		}
	}
	return s.Store.CreateSavedQueryVersion(ctx, scope.Organization, id, in)
}

type SavedQueryEncoder interface {
	EncodeSavedQuery(context.Context, string, Definition) ([]QueryVector, error)
}

// DeleteSavedQuery logically deletes a Saved Query no Subscription uses any
// more. Its Versions stay readable. Repeating it is idempotent.
func (s Service) DeleteSavedQuery(ctx context.Context, scope corpus.Scope, key, id string) (SavedQuery, error) {
	if !scope.Allows("monitoring:write") {
		return SavedQuery{}, ErrForbidden
	}
	if _, err := s.visibleQuery(ctx, scope, id); err != nil {
		return SavedQuery{}, err
	}
	return s.Store.DeleteSavedQuery(ctx, scope.Organization, key, id)
}

// RenameSavedQuery changes the display name of a Saved Query. The name is not
// part of its immutable Versions, so no Version is created and no
// Subscription moves. Repeating it is idempotent.
func (s Service) RenameSavedQuery(ctx context.Context, scope corpus.Scope, key, id, name string) (SavedQuery, error) {
	if !scope.Allows("monitoring:write") {
		return SavedQuery{}, ErrForbidden
	}
	if _, err := s.visibleQuery(ctx, scope, id); err != nil {
		return SavedQuery{}, err
	}
	return s.Store.RenameSavedQuery(ctx, scope.Organization, key, id, name)
}

// validSubscription checks the evaluator and destination of a Subscription
// Version. The evaluator must be served, or be kept, the one the
// Subscription's current Version pins ("" for a new Subscription), while it
// is still installed: an edit never has to move a Subscription to another
// version of its rule.
func (s Service) validSubscription(scope corpus.Scope, evaluator Evaluator, destination, kept string) error {
	key := EvaluatorKey(evaluator)
	if !s.Evaluators.Serves(key) {
		if _, installed := s.Evaluators.Evaluator(key); !installed || key != kept {
			return ErrUnsupportedEvaluator
		}
	}
	if tooLarge(evaluator.Configuration) {
		return ErrTooLarge
	}
	if d, ok := s.Destinations[destination]; !ok || d.Organization != scope.Organization {
		return ErrUnknownDestination
	}
	return nil
}

// validPair checks the Saved Query Version expression and the evaluator
// configuration against the schemas the installed evaluator declares.
func (s Service) validPair(evaluator Evaluator, q SavedQueryVersion) error {
	expression, configuration := q.Definition.Expression, evaluator.Configuration
	if expression == nil {
		expression = map[string]any{}
	}
	if configuration == nil {
		configuration = map[string]any{}
	}
	port, _ := s.Evaluators.Evaluator(EvaluatorKey(evaluator))
	return port.Validate(expression, configuration)
}

// pinnable reads the Saved Query Version a Subscription Version would pin and
// checks that the key grants its whole scope.
func (s Service) pinnable(ctx context.Context, scope corpus.Scope, queryID, versionID string) (SavedQueryVersion, error) {
	v, err := s.Store.SavedQueryVersion(ctx, scope.Organization, queryID, versionID)
	if errors.Is(err, ErrNotFound) {
		return SavedQueryVersion{}, ErrUnknownSavedQuery
	}
	if err != nil {
		return SavedQueryVersion{}, err
	}
	if !covers(scope, v.Definition.CorpusIDs) {
		return SavedQueryVersion{}, ErrForbidden
	}
	return v, nil
}

func (s Service) CreateSubscription(ctx context.Context, scope corpus.Scope, in SubscriptionInput) (Subscription, error) {
	if !scope.Allows("monitoring:write") {
		return Subscription{}, ErrForbidden
	}
	if in.Owner != "" && !validOwner(in.Owner) {
		return Subscription{}, ErrInvalidOwner
	}
	if err := s.validSubscription(scope, in.Evaluator, in.DestinationID, ""); err != nil {
		return Subscription{}, err
	}
	// Replaying a creation stays valid after its Saved Query moved on; the
	// store requires the current Version only for a new Subscription.
	q, err := s.pinnable(ctx, scope, in.SavedQueryID, in.SavedQueryVersionID)
	if err != nil {
		return Subscription{}, err
	}
	if err = s.validPair(in.Evaluator, q); err != nil {
		return Subscription{}, err
	}
	return s.Store.CreateSubscription(ctx, scope.Organization, in, q)
}

// Subscriptions lists the active Subscriptions of one owner, or the global
// ones, that the key sees: it must grant every Corpus any of their Versions
// pinned. Paging is by Subscription ID after after. Quivr enforces no
// per-owner rule; a layer above can use this listing to apply its own.
func (s Service) Subscriptions(ctx context.Context, scope corpus.Scope, owner OwnerFilter, after string, limit int) ([]Subscription, error) {
	if !scope.Allows("monitoring:read") {
		return nil, ErrForbidden
	}
	if !owner.Global && !validOwner(owner.Owner) {
		return nil, ErrInvalidOwner
	}
	// Only an all-Corpora key lists without a Corpus filter; any other key,
	// even one granting no Corpus, filters (never nil: nil means unfiltered).
	var corpora []string
	if !scope.AllCorpora() {
		corpora = append([]string{}, scope.Corpora...)
	}
	subs, err := s.Store.Subscriptions(ctx, scope.Organization, owner, corpora, after, limit)
	if err != nil {
		return nil, err
	}
	// The store filters; the same rule as every Subscription read is applied
	// again here so the listing can never show what a read would conceal.
	visible := subs[:0]
	for _, sub := range subs {
		if covers(scope, sub.Current.CorpusIDs) && covers(scope, sub.PinnedCorpusIDs) {
			visible = append(visible, sub)
		}
	}
	return visible, nil
}

// validOwner accepts an opaque Subscription Owner of 1 to maxOwner printable
// UTF-8 characters other than the reserved filter value NoOwner.
func validOwner(owner string) bool {
	if owner == "" || owner == NoOwner || !utf8.ValidString(owner) || utf8.RuneCountInString(owner) > maxOwner {
		return false
	}
	for _, r := range owner {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s Service) Subscription(ctx context.Context, scope corpus.Scope, id string) (Subscription, error) {
	if !scope.Allows("monitoring:read") {
		return Subscription{}, ErrForbidden
	}
	return s.visible(ctx, scope, id)
}

// SubscriptionVersion reads any immutable Version of a visible Subscription,
// such as the one a historical Match names, when the key grants its scope.
func (s Service) SubscriptionVersion(ctx context.Context, scope corpus.Scope, id, versionID string) (SubscriptionVersion, error) {
	if _, err := s.Subscription(ctx, scope, id); err != nil {
		return SubscriptionVersion{}, err
	}
	v, err := s.Store.SubscriptionVersion(ctx, scope.Organization, id, versionID)
	if err != nil {
		return SubscriptionVersion{}, err
	}
	if !covers(scope, v.CorpusIDs) {
		return SubscriptionVersion{}, ErrNotFound
	}
	return v, nil
}

// CreateSubscriptionVersion edits a Subscription by publishing a new immutable
// Version pinning the current Version of its Saved Query. The new Version
// judges only changes committed after it; earlier changes, and the Matches
// they produced, keep the Version that was effective. Replaying the same
// request returns the same Version.
func (s Service) CreateSubscriptionVersion(ctx context.Context, scope corpus.Scope, id string, in SubscriptionVersionInput) (SubscriptionVersion, error) {
	if !scope.Allows("monitoring:write") {
		return SubscriptionVersion{}, ErrForbidden
	}
	sub, err := s.visible(ctx, scope, id)
	if err != nil {
		return SubscriptionVersion{}, err
	}
	if err = s.validSubscription(scope, in.Evaluator, in.DestinationID, EvaluatorKey(sub.Current.Evaluator)); err != nil {
		return SubscriptionVersion{}, err
	}
	q, err := s.pinnable(ctx, scope, sub.Current.SavedQueryID, in.SavedQueryVersionID)
	if err != nil {
		return SubscriptionVersion{}, err
	}
	if err = s.validPair(in.Evaluator, q); err != nil {
		return SubscriptionVersion{}, err
	}
	return s.Store.CreateSubscriptionVersion(ctx, scope.Organization, id, in, q)
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

// EnableSubscription re-enables a disabled Subscription from now: evaluation
// resumes after the re-enable, without backfilling the pause, and its parked
// Deliveries become claimable again under the unchanged admission rules and
// delivery window. Repeating it, or enabling an enabled Subscription, is
// idempotent and commits no event. A deleted Subscription is never re-enabled.
func (s Service) EnableSubscription(ctx context.Context, scope corpus.Scope, key, id string) (Subscription, error) {
	if !scope.Allows("monitoring:write") {
		return Subscription{}, ErrForbidden
	}
	if _, err := s.visible(ctx, scope, id); err != nil {
		return Subscription{}, err
	}
	return s.Store.EnableSubscription(ctx, scope.Organization, key, id)
}

// DeleteSubscription logically deletes a Subscription with every disable
// guarantee, for good: no new evaluation commit and no new Delivery Attempt
// admission. Its Versions, Matches and Deliveries stay readable. Repeating it
// is idempotent.
func (s Service) DeleteSubscription(ctx context.Context, scope corpus.Scope, key, id string) (Subscription, error) {
	if !scope.Allows("monitoring:write") {
		return Subscription{}, ErrForbidden
	}
	if _, err := s.visible(ctx, scope, id); err != nil {
		return Subscription{}, err
	}
	return s.Store.DeleteSubscription(ctx, scope.Organization, key, id)
}

// RenameSubscription changes the display name of a Subscription. The name is
// not part of its immutable Versions: evaluation, enabled state, Matches and
// Deliveries are unchanged. Repeating it is idempotent.
func (s Service) RenameSubscription(ctx context.Context, scope corpus.Scope, key, id, name string) (Subscription, error) {
	if !scope.Allows("monitoring:write") {
		return Subscription{}, ErrForbidden
	}
	if _, err := s.visible(ctx, scope, id); err != nil {
		return Subscription{}, err
	}
	return s.Store.RenameSubscription(ctx, scope.Organization, key, id, name)
}

func (s Service) visible(ctx context.Context, scope corpus.Scope, id string) (Subscription, error) {
	sub, err := s.Store.Subscription(ctx, scope.Organization, id)
	if err != nil {
		return Subscription{}, err
	}
	// An edit that narrows the scope must not expose Matches and Deliveries
	// on Corpora only an earlier Version pinned.
	if !covers(scope, sub.Current.CorpusIDs) || !covers(scope, sub.PinnedCorpusIDs) {
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
