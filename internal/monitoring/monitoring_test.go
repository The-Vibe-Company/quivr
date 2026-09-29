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
	versions      map[string]any // org/resource/version
	disabled      []string
	// writes records every edit and delete that reached the store.
	writes []string
}

func newStore() *memoryStore {
	return &memoryStore{queries: map[string]monitoring.SavedQuery{}, subscriptions: map[string]monitoring.Subscription{}, versions: map[string]any{}}
}

func (m *memoryStore) version(org, id, versionID string) (any, error) {
	v, ok := m.versions[org+"/"+id+"/"+versionID]
	if !ok {
		return nil, monitoring.ErrNotFound
	}
	return v, nil
}
func (m *memoryStore) SavedQueryVersion(_ context.Context, org, id, versionID string) (monitoring.SavedQueryVersion, error) {
	v, err := m.version(org, id, versionID)
	if err != nil {
		return monitoring.SavedQueryVersion{}, err
	}
	return v.(monitoring.SavedQueryVersion), nil
}
func (m *memoryStore) CreateSavedQueryVersion(_ context.Context, org, id string, in monitoring.SavedQueryVersionInput) (monitoring.SavedQueryVersion, error) {
	m.writes = append(m.writes, "query version "+id)
	q := m.queries[org+"/"+id]
	q.Current = monitoring.SavedQueryVersion{SavedQueryID: id, VersionID: "sqv_" + in.Key, Definition: in.Definition}
	m.queries[org+"/"+id] = q
	m.versions[org+"/"+id+"/"+q.Current.VersionID] = q.Current
	return q.Current, nil
}
func (m *memoryStore) DeleteSavedQuery(_ context.Context, org, key, id string) (monitoring.SavedQuery, error) {
	m.writes = append(m.writes, "query delete "+id)
	q := m.queries[org+"/"+id]
	q.Deleted = true
	m.queries[org+"/"+id] = q
	return q, nil
}
func (m *memoryStore) SubscriptionVersion(_ context.Context, org, id, versionID string) (monitoring.SubscriptionVersion, error) {
	v, err := m.version(org, id, versionID)
	if err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	return v.(monitoring.SubscriptionVersion), nil
}
func (m *memoryStore) CreateSubscriptionVersion(_ context.Context, org, id string, in monitoring.SubscriptionVersionInput, query monitoring.SavedQueryVersion) (monitoring.SubscriptionVersion, error) {
	m.writes = append(m.writes, "subscription version "+id)
	s := m.subscriptions[org+"/"+id]
	s.Current = monitoring.SubscriptionVersion{SubscriptionID: id, VersionID: "subv_" + in.Key, SavedQueryID: query.SavedQueryID, SavedQueryVersionID: query.VersionID, Evaluator: in.Evaluator, DestinationID: in.DestinationID, CorpusIDs: query.Definition.CorpusIDs}
	m.subscriptions[org+"/"+id] = s
	m.versions[org+"/"+id+"/"+s.Current.VersionID] = s.Current
	return s.Current, nil
}
func (m *memoryStore) DeleteSubscription(_ context.Context, org, key, id string) (monitoring.Subscription, error) {
	m.writes = append(m.writes, "subscription delete "+id)
	s := m.subscriptions[org+"/"+id]
	s.Enabled, s.Deleted = false, true
	m.subscriptions[org+"/"+id] = s
	return s, nil
}

func (m *memoryStore) CreateSavedQuery(_ context.Context, org string, in monitoring.SavedQueryInput) (monitoring.SavedQuery, error) {
	q := monitoring.SavedQuery{ID: "sq_" + in.Key, Name: in.Name, Current: monitoring.SavedQueryVersion{SavedQueryID: "sq_" + in.Key, VersionID: "sqv_" + in.Key, Definition: in.Definition}}
	m.queries[org+"/"+q.ID] = q
	m.versions[org+"/"+q.ID+"/"+q.Current.VersionID] = q.Current
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
	m.versions[org+"/"+s.ID+"/"+s.Current.VersionID] = s.Current
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

func (m *memoryStore) EnableSubscription(_ context.Context, org, key, id string) (monitoring.Subscription, error) {
	s := m.subscriptions[org+"/"+id]
	s.Enabled = true
	m.subscriptions[org+"/"+id] = s
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
	// Re-enable has the same visibility and permission rules.
	for name, scope := range map[string]corpus.Scope{"partial Corpus grant": narrow, "other Organization": outside} {
		if _, err = s.EnableSubscription(ctx, scope, "e", sub.ID); !errors.Is(err, monitoring.ErrNotFound) {
			t.Errorf("%s enable: %v", name, err)
		}
	}
	if _, err = s.EnableSubscription(ctx, reader, "e", sub.ID); !errors.Is(err, monitoring.ErrForbidden) {
		t.Errorf("read-only enable: %v", err)
	}
	if got, err = s.EnableSubscription(ctx, writer, "e", sub.ID); err != nil || !got.Enabled || got.Current.VersionID != sub.Current.VersionID {
		t.Fatalf("enable: %v %+v", err, got)
	}
}

// TestEditsAndDeletesFollowScopeRules applies creation's rules to edits: the
// key must grant the edited resource and every Corpus of the new Version, a
// Subscription pins only a Version of its own Saved Query, and a Version read
// needs its own Corpora granted. Refused commands never reach the store.
func TestEditsAndDeletesFollowScopeRules(t *testing.T) {
	ctx := context.Background()
	s, store := service()
	q, _ := s.CreateSavedQuery(ctx, writer, query("q", "corpus_a"))
	otherQuery, _ := s.CreateSavedQuery(ctx, writer, query("other", "corpus_a"))
	sub, err := s.CreateSubscription(ctx, writer, monitoring.SubscriptionInput{Key: "k", Name: "n", SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, Evaluator: fixture(), DestinationID: "receiver_a"})
	if err != nil {
		t.Fatal(err)
	}
	wide := monitoring.SavedQueryVersionInput{Key: "wide", Definition: query("", "corpus_a", "corpus_b").Definition}
	subEdit := func(versionID string) monitoring.SubscriptionVersionInput {
		return monitoring.SubscriptionVersionInput{Key: "e-" + versionID, SavedQueryVersionID: versionID, Evaluator: fixture(), DestinationID: "receiver_a"}
	}
	foreign := subEdit(q.Current.VersionID)
	foreign.DestinationID = "receiver_b"
	checks := []struct {
		name string
		err  error
		want error
	}{
		{"read-only query edit", func() error { _, err := s.CreateSavedQueryVersion(ctx, reader, q.ID, wide); return err }(), monitoring.ErrForbidden},
		{"query edit to an ungranted Corpus", func() error { _, err := s.CreateSavedQueryVersion(ctx, narrow, q.ID, wide); return err }(), monitoring.ErrForbidden},
		{"query edit in another Organization", func() error { _, err := s.CreateSavedQueryVersion(ctx, outside, q.ID, wide); return err }(), monitoring.ErrNotFound},
		{"query delete in another Organization", func() error { _, err := s.DeleteSavedQuery(ctx, outside, "d", q.ID); return err }(), monitoring.ErrNotFound},
		{"subscription edit pinning another Saved Query", func() error {
			_, err := s.CreateSubscriptionVersion(ctx, writer, sub.ID, subEdit(otherQuery.Current.VersionID))
			return err
		}(), monitoring.ErrUnknownSavedQuery},
		{"subscription edit to a foreign destination", func() error { _, err := s.CreateSubscriptionVersion(ctx, writer, sub.ID, foreign); return err }(), monitoring.ErrUnknownDestination},
		{"read-only subscription delete", func() error { _, err := s.DeleteSubscription(ctx, reader, "d", sub.ID); return err }(), monitoring.ErrForbidden},
		{"subscription delete in another Organization", func() error { _, err := s.DeleteSubscription(ctx, outside, "d", sub.ID); return err }(), monitoring.ErrNotFound},
	}
	for _, c := range checks {
		if !errors.Is(c.err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, c.err, c.want)
		}
	}
	if len(store.writes) != 0 {
		t.Fatalf("refused commands reached the store: %v", store.writes)
	}

	v2, err := s.CreateSavedQueryVersion(ctx, writer, q.ID, wide)
	if err != nil {
		t.Fatal(err)
	}
	// The new Version covers a Corpus the narrow key lacks: the Saved Query
	// is concealed from it, while the first Version is not what it reads.
	if _, err = s.SavedQuery(ctx, narrow, q.ID); !errors.Is(err, monitoring.ErrNotFound) {
		t.Errorf("narrow read after widening: %v", err)
	}
	if _, err = s.SavedQueryVersion(ctx, writer, q.ID, q.Current.VersionID); err != nil {
		t.Errorf("earlier Version read: %v", err)
	}
	if _, err = s.SubscriptionVersion(ctx, writer, sub.ID, sub.Current.VersionID); err != nil {
		t.Errorf("Subscription Version read: %v", err)
	}
	moved, err := s.CreateSubscriptionVersion(ctx, writer, sub.ID, subEdit(v2.VersionID))
	if err != nil || moved.SavedQueryVersionID != v2.VersionID {
		t.Fatalf("subscription edit: %v %+v", err, moved)
	}
	if _, err = s.SubscriptionVersion(ctx, narrow, sub.ID, sub.Current.VersionID); !errors.Is(err, monitoring.ErrNotFound) {
		t.Errorf("narrow Version read of a widened Subscription: %v", err)
	}
	if _, err = s.SubscriptionVersion(ctx, writer, sub.ID, "subv_missing"); !errors.Is(err, monitoring.ErrNotFound) {
		t.Errorf("unknown Subscription Version: %v", err)
	}
	// A key must grant every Corpus any Version pinned, so narrowing the
	// scope never exposes earlier Matches.
	narrowed := store.subscriptions["org_a/"+sub.ID]
	narrowed.Current.CorpusIDs, narrowed.PinnedCorpusIDs = []string{"corpus_a"}, []string{"corpus_a", "corpus_b"}
	store.subscriptions["org_a/"+sub.ID] = narrowed
	if _, err = s.Subscription(ctx, narrow, sub.ID); !errors.Is(err, monitoring.ErrNotFound) {
		t.Errorf("narrow read after narrowing: %v", err)
	}
	// Saved Query commands need monitoring:write only.
	writeOnly := corpus.Scope{Organization: "org_a", Actions: []string{"monitoring:write"}, Corpora: []string{"*"}}
	if _, err = s.CreateSavedQueryVersion(ctx, writeOnly, q.ID, monitoring.SavedQueryVersionInput{Key: "w", Definition: query("", "corpus_a").Definition}); err != nil {
		t.Errorf("write-only query edit: %v", err)
	}
	if got, err := s.DeleteSubscription(ctx, writer, "d", sub.ID); err != nil || !got.Deleted || got.Enabled {
		t.Fatalf("delete: %v %+v", err, got)
	}
}
