package monitoring_test

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"slices"
	"strings"
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
	// created records the owner of every creation that reached the store.
	created []string
	listed  listing
}

// listing records the last Subscription listing request.
type listing struct {
	Owner   monitoring.OwnerFilter
	Corpora []string
	After   string
	Limit   int
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
func (m *memoryStore) RenameSavedQuery(_ context.Context, org, key, id, name string) (monitoring.SavedQuery, error) {
	m.writes = append(m.writes, "query rename "+id)
	q := m.queries[org+"/"+id]
	q.Name = name
	m.queries[org+"/"+id] = q
	return q, nil
}
func (m *memoryStore) RenameSubscription(_ context.Context, org, key, id, name string) (monitoring.Subscription, error) {
	m.writes = append(m.writes, "subscription rename "+id)
	s := m.subscriptions[org+"/"+id]
	s.Name = name
	m.subscriptions[org+"/"+id] = s
	return s, nil
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
	m.created = append(m.created, in.Owner)
	s := monitoring.Subscription{ID: "sub_" + in.Key, Name: in.Name, Owner: in.Owner, Enabled: true, Current: monitoring.SubscriptionVersion{SubscriptionID: "sub_" + in.Key, VersionID: "subv_" + in.Key, Owner: in.Owner, SavedQueryID: in.SavedQueryID, SavedQueryVersionID: in.SavedQueryVersionID, Evaluator: in.Evaluator, DestinationID: in.DestinationID, CorpusIDs: query.Definition.CorpusIDs}}
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

// Subscriptions lists the Organization's enabled Subscriptions of an owner,
// ignoring the Corpus filter it records.
func (m *memoryStore) Subscriptions(_ context.Context, org string, owner monitoring.OwnerFilter, corpora []string, after string, limit int) ([]monitoring.Subscription, error) {
	m.listed = listing{owner, corpora, after, limit}
	var out []monitoring.Subscription
	for key, s := range m.subscriptions {
		if strings.HasPrefix(key, org+"/") && s.Enabled && !s.Deleted && s.Owner == owner.Owner && (s.Owner == "") == owner.Global {
			out = append(out, s)
		}
	}
	return out, nil
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

// schemaEvaluator stands for a pinned plugin whose declared schemas require
// an expression text and allow only a boolean case_sensitive configuration.
type schemaEvaluator struct{}

func (schemaEvaluator) MaxBatch() int { return 4 }
func (schemaEvaluator) Validate(expression, configuration map[string]any) error {
	if text, ok := expression["text"].(string); !ok || text == "" {
		return monitoring.Invalid(monitoring.ErrInvalidExpression, "/saved_query_version_id", "/expression: missing property 'text'")
	}
	for key, value := range configuration {
		if _, ok := value.(bool); key != "case_sensitive" || !ok {
			return monitoring.Invalid(monitoring.ErrInvalidEvaluatorConfiguration, "/evaluator/configuration/"+key, "not allowed")
		}
	}
	return nil
}
func (schemaEvaluator) Evaluate(context.Context, monitoring.Batch) ([]monitoring.Outcome, error) {
	return nil, monitoring.ErrEvaluatorUnavailable
}

func evaluators() monitoring.Evaluators {
	installed := fakeplugin.FixtureEvaluators()
	installed["acme.alerts@0.1.0"] = schemaEvaluator{}
	return installed
}

func service() (monitoring.Service, *memoryStore) {
	store := newStore()
	return monitoring.Service{
		Store:      store,
		Evaluators: evaluators(),
		Corpora:    corpora{"corpus_a": true, "corpus_b": true},
		Destinations: map[string]monitoring.Destination{
			"receiver_a": {Organization: "org_a", URL: "http://127.0.0.1:9/hook", Secret: "whsec_test"},
			"receiver_b": {Organization: "org_b", URL: "http://127.0.0.1:9/hook", Secret: "whsec_test"},
		},
	}, store
}

func query(key string, corpora ...string) monitoring.SavedQueryInput {
	return monitoring.SavedQueryInput{Key: key, Name: "Query " + key, Definition: monitoring.Definition{CorpusIDs: corpora, Expression: map[string]any{"fixture": "match"}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}}
}

func fixture() monitoring.Evaluator {
	return monitoring.Evaluator{PluginID: fakeplugin.FixtureEvaluator, Version: fakeplugin.FixtureEvaluatorVersion, Configuration: map[string]any{}}
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
	}
	for _, c := range cases {
		if _, err := s.CreateSavedQuery(ctx, c.scope, c.input); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	q, err := s.CreateSavedQuery(ctx, narrow, query("ok", "corpus_a"))
	if err != nil || q.Current.Definition.RetrievalProfile != "default" {
		t.Fatalf("authorized creation: %v %+v", err, q)
	}
}

// servedProfiles are the profiles a pinned retrieval plugin declares.
type servedProfiles []string

func (p servedProfiles) Serves(profile string) bool { return slices.Contains(p, profile) }

// A definition names a profile the deployment answers: the built-in default
// without a retrieval plugin, the plugin's profiles
// with one. The profile is recorded as sent.
func TestSavedQueryNamesAServedProfile(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		profile string
		served  monitoring.SearchProfiles
		want    error
	}{
		{profile: "default"},
		{profile: "deep", want: monitoring.ErrUnsupportedProfile},
		{profile: "deep", served: servedProfiles{"default", "deep"}},
		{profile: "fast", served: servedProfiles{"default", "deep"}, want: monitoring.ErrUnsupportedProfile},
	} {
		s, _ := service()
		s.Profiles = c.served
		in := query("q", "corpus_a")
		in.Definition.RetrievalProfile = c.profile
		q, err := s.CreateSavedQuery(ctx, writer, in)
		if !errors.Is(err, c.want) || (err == nil && q.Current.Definition.RetrievalProfile != c.profile) {
			t.Errorf("profile %q (served %v): %v %+v, want %v", c.profile, c.served, err, q.Current.Definition, c.want)
		}
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
	if !sub.Enabled || sub.Current.Evaluator.PluginID != "quivr.fixture" || sub.Current.Evaluator.Version != fakeplugin.FixtureEvaluatorVersion || sub.Current.DestinationID != "receiver_a" {
		t.Fatalf("pinned subscription: %+v", sub)
	}
}

// A Subscription Version pins only an installed evaluator, and the evaluator's
// declared schemas judge the pinned expression and configuration at creation
// and at every new Version.
func TestSubscriptionValidatesExpressionAndConfiguration(t *testing.T) {
	ctx := context.Background()
	s, _ := service()
	plain, err := s.CreateSavedQuery(ctx, writer, query("plain", "corpus_a"))
	if err != nil {
		t.Fatal(err)
	}
	withText := query("text", "corpus_a")
	withText.Definition.Expression = map[string]any{"text": "harbour"}
	text, err := s.CreateSavedQuery(ctx, writer, withText)
	if err != nil {
		t.Fatal(err)
	}
	alerts := func(key string, q monitoring.SavedQuery, configuration map[string]any) monitoring.SubscriptionInput {
		return monitoring.SubscriptionInput{Key: key, Name: key, SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, DestinationID: "receiver_a",
			Evaluator: monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0", Configuration: configuration}}
	}
	_, err = s.CreateSubscription(ctx, writer, alerts("bad-expression", plain, nil))
	if field, message := monitoring.Field(err); !errors.Is(err, monitoring.ErrInvalidExpression) || field != "/saved_query_version_id" || !strings.Contains(message, "text") {
		t.Fatalf("invalid expression: %v", err)
	}
	_, err = s.CreateSubscription(ctx, writer, alerts("bad-configuration", text, map[string]any{"fuzzy": true}))
	if field, _ := monitoring.Field(err); !errors.Is(err, monitoring.ErrInvalidEvaluatorConfiguration) || field != "/evaluator/configuration/fuzzy" {
		t.Fatalf("invalid configuration: %v", err)
	}
	sub, err := s.CreateSubscription(ctx, writer, alerts("ok", text, map[string]any{"case_sensitive": true}))
	if err != nil || sub.Current.Evaluator.PluginID != "acme.alerts" {
		t.Fatalf("valid plugin subscription: %v %+v", err, sub)
	}
	edit := monitoring.SubscriptionVersionInput{Key: "edit", SavedQueryVersionID: text.Current.VersionID, DestinationID: "receiver_a",
		Evaluator: monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0", Configuration: map[string]any{"case_sensitive": "yes"}}}
	if _, err = s.CreateSubscriptionVersion(ctx, writer, sub.ID, edit); !errors.Is(err, monitoring.ErrInvalidEvaluatorConfiguration) {
		t.Fatalf("invalid configuration on edit: %v", err)
	}
	// Without an installed fixture, the fixture is an unsupported evaluator.
	s.Evaluators = monitoring.Evaluators{"acme.alerts@0.1.0": schemaEvaluator{}}
	fixtureInput := alerts("fixture", text, nil)
	fixtureInput.Evaluator = fixture()
	if _, err = s.CreateSubscription(ctx, writer, fixtureInput); !errors.Is(err, monitoring.ErrUnsupportedEvaluator) {
		t.Fatalf("fixture without installation: %v", err)
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
		{"read-only query rename", func() error { _, err := s.RenameSavedQuery(ctx, reader, "r", q.ID, "x"); return err }(), monitoring.ErrForbidden},
		{"query rename in another Organization", func() error { _, err := s.RenameSavedQuery(ctx, outside, "r", q.ID, "x"); return err }(), monitoring.ErrNotFound},
		{"read-only subscription rename", func() error { _, err := s.RenameSubscription(ctx, reader, "r", sub.ID, "x"); return err }(), monitoring.ErrForbidden},
		{"subscription rename in another Organization", func() error { _, err := s.RenameSubscription(ctx, outside, "r", sub.ID, "x"); return err }(), monitoring.ErrNotFound},
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

// TestSubscriptionOwner covers the Subscription Owner rules the service
// enforces: a bounded opaque reference or none (global), and a listing
// limited to visible Subscriptions.
func TestSubscriptionOwner(t *testing.T) {
	ctx := context.Background()
	s, store := service()
	q, _ := s.CreateSavedQuery(ctx, writer, query("q", "corpus_a"))
	input := func(key, owner string) monitoring.SubscriptionInput {
		return monitoring.SubscriptionInput{Key: key, Name: "n", Owner: owner, SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, Evaluator: fixture(), DestinationID: "receiver_a"}
	}
	long := make([]rune, 129)
	for i := range long {
		long[i] = 'é'
	}
	for name, owner := range map[string]string{"reserved none": "none", "too long": string(long), "control character": "user\n1", "invalid UTF-8": "user\xff"} {
		if _, err := s.CreateSubscription(ctx, writer, input("bad-"+name, owner)); !errors.Is(err, monitoring.ErrInvalidOwner) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(store.created) != 0 {
		t.Fatalf("invalid owners reached the store: %v", store.created)
	}
	owned, err := s.CreateSubscription(ctx, writer, input("owned", "user-123"))
	if err != nil || owned.Owner != "user-123" || owned.Current.Owner != "user-123" {
		t.Fatalf("owned creation: %v %+v", err, owned)
	}
	if _, err = s.CreateSubscription(ctx, writer, input("max", string(long[:128]))); err != nil {
		t.Fatalf("128-character owner: %v", err)
	}
	global, err := s.CreateSubscription(ctx, writer, input("global", ""))
	if err != nil || global.Owner != "" {
		t.Fatalf("global creation: %v %+v", err, global)
	}
	if want := []string{"user-123", string(long[:128]), ""}; !slices.Equal(store.created, want) {
		t.Fatalf("owners handed to the store: %q", store.created)
	}

	page, err := s.Subscriptions(ctx, writer, monitoring.OwnerFilter{Owner: "user-123"}, "", 10)
	if err != nil || len(page) != 1 || page[0].ID != owned.ID {
		t.Fatalf("list by owner: %v %+v", err, page)
	}
	if page, err = s.Subscriptions(ctx, writer, monitoring.OwnerFilter{Global: true}, "", 10); err != nil || len(page) != 1 || page[0].ID != global.ID {
		t.Fatalf("list global: %v %+v", err, page)
	}
	if store.listed.Corpora != nil || store.listed.Owner != (monitoring.OwnerFilter{Global: true}) || store.listed.Limit != 10 {
		t.Fatalf("an all-Corpora key lists without a Corpus filter: %+v", store.listed)
	}
	if _, err = s.Subscriptions(ctx, narrow, monitoring.OwnerFilter{Owner: "user-123"}, "after", 5); err != nil || !slices.Equal(store.listed.Corpora, []string{"corpus_a"}) || store.listed.After != "after" {
		t.Fatalf("a narrow key lists only within its Corpora: %v %+v", err, store.listed)
	}
	// A key granting no Corpus filters on none rather than listing everything,
	// and the service conceals what a read would, whatever the store returns.
	if _, err = s.Subscriptions(ctx, corpus.Scope{Organization: "org_a", Actions: []string{"monitoring:read"}}, monitoring.OwnerFilter{Owner: "user-123"}, "", 10); err != nil || store.listed.Corpora == nil {
		t.Fatalf("a key without Corpora must filter: %v %+v", err, store.listed)
	}
	qb, _ := s.CreateSavedQuery(ctx, writer, query("qb", "corpus_b"))
	if _, err = s.CreateSubscription(ctx, writer, monitoring.SubscriptionInput{Key: "on-b", Name: "n", Owner: "user-b", SavedQueryID: qb.ID, SavedQueryVersionID: qb.Current.VersionID, Evaluator: fixture(), DestinationID: "receiver_a"}); err != nil {
		t.Fatal(err)
	}
	if page, err = s.Subscriptions(ctx, narrow, monitoring.OwnerFilter{Owner: "user-b"}, "", 10); err != nil || len(page) != 0 {
		t.Fatalf("a narrow key never lists a Subscription on an ungranted Corpus: %v %+v", err, page)
	}
	if _, err = s.Subscriptions(ctx, corpus.Scope{Organization: "org_a", Actions: []string{"monitoring:write"}, Corpora: []string{"*"}}, monitoring.OwnerFilter{Global: true}, "", 10); !errors.Is(err, monitoring.ErrForbidden) {
		t.Errorf("listing without monitoring:read: %v", err)
	}
	if _, err = s.Subscriptions(ctx, writer, monitoring.OwnerFilter{Owner: "none"}, "", 10); !errors.Is(err, monitoring.ErrInvalidOwner) {
		t.Errorf("listing an invalid owner: %v", err)
	}
}
