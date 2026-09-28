package monitoring_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// memoryStore keeps definitions in memory; idempotency and journal behavior
// are proven against PostgreSQL and the public API.
type memoryStore struct {
	queries       map[string]monitoring.SavedQuery
	subscriptions map[string]monitoring.Subscription
	disabled      []string
}

func newStore() *memoryStore {
	return &memoryStore{queries: map[string]monitoring.SavedQuery{}, subscriptions: map[string]monitoring.Subscription{}}
}

func (m *memoryStore) CreateSavedQuery(_ context.Context, org string, in monitoring.SavedQueryInput) (monitoring.SavedQuery, error) {
	q := monitoring.SavedQuery{ID: "sq_" + in.Key, Name: in.Name, Current: monitoring.SavedQueryVersion{SavedQueryID: "sq_" + in.Key, VersionID: "sqv_" + in.Key, Definition: in.Definition}}
	m.queries[org+"/"+q.ID] = q
	return q, nil
}
func (m *memoryStore) SavedQuery(_ context.Context, org, id string) (monitoring.SavedQuery, error) {
	q, ok := m.queries[org+"/"+id]
	if !ok {
		return q, monitoring.ErrNotFound
	}
	return q, nil
}
func (m *memoryStore) CreateSubscription(_ context.Context, org string, in monitoring.SubscriptionInput, query monitoring.SavedQueryVersion) (monitoring.Subscription, error) {
	s := monitoring.Subscription{ID: "sub_" + in.Key, Name: in.Name, Enabled: true, Current: monitoring.SubscriptionVersion{SubscriptionID: "sub_" + in.Key, VersionID: "subv_" + in.Key, SavedQueryID: in.SavedQueryID, SavedQueryVersionID: in.SavedQueryVersionID, Evaluator: in.Evaluator, DestinationID: in.DestinationID, CorpusIDs: query.Definition.CorpusIDs}}
	m.subscriptions[org+"/"+s.ID] = s
	return s, nil
}
func (m *memoryStore) Subscription(_ context.Context, org, id string) (monitoring.Subscription, error) {
	s, ok := m.subscriptions[org+"/"+id]
	if !ok {
		return s, monitoring.ErrNotFound
	}
	return s, nil
}
func (m *memoryStore) DisableSubscription(_ context.Context, org, key, id string) (monitoring.Subscription, error) {
	s := m.subscriptions[org+"/"+id]
	s.Enabled = false
	m.subscriptions[org+"/"+id] = s
	m.disabled = append(m.disabled, id)
	return s, nil
}

type corpora map[string]bool

func (c corpora) Authorize(_ context.Context, _ corpus.Scope, ids []string) error {
	for _, id := range ids {
		if !c[id] {
			return corpus.ErrForbidden
		}
	}
	return nil
}

var (
	writer  = corpus.Scope{Organization: "org_a", Actions: []string{"monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	narrow  = corpus.Scope{Organization: "org_a", Actions: []string{"monitoring:read", "monitoring:write"}, Corpora: []string{"corpus_a"}}
	reader  = corpus.Scope{Organization: "org_a", Actions: []string{"monitoring:read"}, Corpora: []string{"*"}}
	outside = corpus.Scope{Organization: "org_b", Actions: []string{"monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
)

func service() (monitoring.Service, *memoryStore) {
	store := newStore()
	return monitoring.Service{
		Store:   store,
		Corpora: corpora{"corpus_a": true, "corpus_b": true},
		Destinations: map[string]monitoring.Destination{
			"receiver_a": {Organization: "org_a", URL: "http://127.0.0.1:9/hook", Secret: "whsec_test"},
			"receiver_b": {Organization: "org_b", URL: "http://127.0.0.1:9/hook", Secret: "whsec_test"},
		},
	}, store
}

func query(key string, corpora ...string) monitoring.SavedQueryInput {
	return monitoring.SavedQueryInput{Key: key, Name: "Query " + key, Definition: monitoring.Definition{CorpusIDs: corpora, Expression: map[string]any{"fixture": "match"}, RetrievalProfile: "balanced", TemporalPolicy: "from_activation"}}
}

func fixture() monitoring.Evaluator {
	return monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{}}
}

func TestSavedQueryCreationRequiresWriteAndEveryCorpus(t *testing.T) {
	ctx := context.Background()
	s, _ := service()
	cases := []struct {
		name  string
		scope corpus.Scope
		input monitoring.SavedQueryInput
		want  error
	}{
		{"read-only key", reader, query("r", "corpus_a"), monitoring.ErrForbidden},
		{"ungranted Corpus is never dropped", narrow, query("n", "corpus_a", "corpus_b"), monitoring.ErrForbidden},
		{"Corpus outside the Organization", writer, query("x", "corpus_a", "corpus_missing"), monitoring.ErrForbidden},
		{"unimplemented profile", writer, func() monitoring.SavedQueryInput {
			q := query("p", "corpus_a")
			q.Definition.RetrievalProfile = "deep"
			return q
		}(), monitoring.ErrUnsupportedProfile},
	}
	for _, c := range cases {
		if _, err := s.CreateSavedQuery(ctx, c.scope, c.input); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	q, err := s.CreateSavedQuery(ctx, narrow, query("ok", "corpus_a"))
	if err != nil || q.Current.Definition.RetrievalProfile != "balanced" {
		t.Fatalf("authorized creation: %v %+v", err, q)
	}
}

func TestSavedQueryReadsConcealOutsideScope(t *testing.T) {
	ctx := context.Background()
	s, _ := service()
	q, err := s.CreateSavedQuery(ctx, writer, query("both", "corpus_a", "corpus_b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SavedQuery(ctx, writer, q.ID); err != nil {
		t.Fatal(err)
	}
	for name, scope := range map[string]corpus.Scope{"partial Corpus grant": narrow, "other Organization": outside} {
		if _, err = s.SavedQuery(ctx, scope, q.ID); !errors.Is(err, monitoring.ErrNotFound) {
			t.Errorf("%s read: %v", name, err)
		}
		if _, err = s.SavedQueryVersion(ctx, scope, q.ID, q.Current.VersionID); !errors.Is(err, monitoring.ErrNotFound) {
			t.Errorf("%s version read: %v", name, err)
		}
	}
	if _, err = s.SavedQueryVersion(ctx, writer, q.ID, "sqv_other"); !errors.Is(err, monitoring.ErrNotFound) {
		t.Errorf("unknown version: %v", err)
	}
	if _, err = s.SavedQuery(ctx, corpus.Scope{Organization: "org_a", Actions: []string{"monitoring:write"}, Corpora: []string{"*"}}, q.ID); !errors.Is(err, monitoring.ErrForbidden) {
		t.Errorf("missing monitoring:read: %v", err)
	}
}

func TestSubscriptionPinsOnlyTheInstalledFixtureAndAnOwnedDestination(t *testing.T) {
	ctx := context.Background()
	s, _ := service()
	q, err := s.CreateSavedQuery(ctx, writer, query("q", "corpus_a"))
	if err != nil {
		t.Fatal(err)
	}
	input := func(key string) monitoring.SubscriptionInput {
		return monitoring.SubscriptionInput{Key: key, Name: "Sub " + key, SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, Evaluator: fixture(), DestinationID: "receiver_a"}
	}
	other := input("plugin")
	other.Evaluator.PluginID = "vendor.semantic"
	version := input("version")
	version.Evaluator.Version = "2"
	foreign := input("foreign")
	foreign.DestinationID = "receiver_b"
	unknown := input("unknown")
	unknown.DestinationID = "receiver_missing"
	stale := input("stale")
	stale.SavedQueryVersionID = "sqv_missing"
	cases := []struct {
		name  string
		scope corpus.Scope
		input monitoring.SubscriptionInput
		want  error
	}{
		{"other evaluator", writer, other, monitoring.ErrUnsupportedEvaluator},
		{"other fixture version", writer, version, monitoring.ErrUnsupportedEvaluator},
		{"destination of another Organization", writer, foreign, monitoring.ErrUnknownDestination},
		{"unconfigured destination", writer, unknown, monitoring.ErrUnknownDestination},
		{"unknown query version", writer, stale, monitoring.ErrUnknownSavedQuery},
		{"read-only key", reader, input("reader"), monitoring.ErrForbidden},
	}
	for _, c := range cases {
		if _, err := s.CreateSubscription(ctx, c.scope, c.input); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	sub, err := s.CreateSubscription(ctx, writer, input("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if !sub.Enabled || sub.Current.Evaluator.PluginID != "quivr.fixture" || sub.Current.Evaluator.Version != "1" || sub.Current.DestinationID != "receiver_a" {
		t.Fatalf("pinned subscription: %+v", sub)
	}
}

func TestSubscriptionOnUngrantedQueryCorpusIsForbidden(t *testing.T) {
	ctx := context.Background()
	s, _ := service()
	q, err := s.CreateSavedQuery(ctx, writer, query("wide", "corpus_a", "corpus_b"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateSubscription(ctx, narrow, monitoring.SubscriptionInput{Key: "k", Name: "n", SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, Evaluator: fixture(), DestinationID: "receiver_a"})
	if !errors.Is(err, monitoring.ErrForbidden) {
		t.Fatalf("got %v", err)
	}
}

func TestDisableConcealsOutsideScope(t *testing.T) {
	ctx := context.Background()
	s, store := service()
	q, _ := s.CreateSavedQuery(ctx, writer, query("q", "corpus_a", "corpus_b"))
	sub, err := s.CreateSubscription(ctx, writer, monitoring.SubscriptionInput{Key: "k", Name: "n", SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, Evaluator: fixture(), DestinationID: "receiver_a"})
	if err != nil {
		t.Fatal(err)
	}
	for name, scope := range map[string]corpus.Scope{"partial Corpus grant": narrow, "other Organization": outside} {
		if _, err = s.DisableSubscription(ctx, scope, "d", sub.ID); !errors.Is(err, monitoring.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err = s.Subscription(ctx, scope, sub.ID); !errors.Is(err, monitoring.ErrNotFound) {
			t.Errorf("%s read: %v", name, err)
		}
	}
	if _, err = s.DisableSubscription(ctx, reader, "d", sub.ID); !errors.Is(err, monitoring.ErrForbidden) {
		t.Errorf("read-only disable: %v", err)
	}
	if len(store.disabled) != 0 {
		t.Fatalf("unauthorized disable reached the store: %v", store.disabled)
	}
	got, err := s.DisableSubscription(ctx, writer, "d", sub.ID)
	if err != nil || got.Enabled {
		t.Fatalf("disable: %v %+v", err, got)
	}
	if v, err := s.SubscriptionVersion(ctx, writer, sub.ID, sub.Current.VersionID); err != nil || v.Evaluator.PluginID != "quivr.fixture" {
		t.Fatalf("pinned version survives disable: %v %+v", err, v)
	}
}
