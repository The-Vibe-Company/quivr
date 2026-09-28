package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5/pgxpool"
)

func adapterPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("real PostgreSQL suite runs inside make verify")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSubscriptionActivationBoundaryAndDisable proves the monitoring commit
// boundaries against the real journal: activation position ordering, one
// public event per pinned Corpus, idempotent replay and disable, and that
// replaying creation never reenables a disabled Subscription.
func TestSubscriptionActivationBoundaryAndDisable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-monitoring-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "content:write", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	contents := content.Service{Repository: store}
	accept := func(key string) {
		t.Helper()
		if _, err := contents.Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: a.ID, Namespace: "monitoring", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Monitoring " + key}}); err != nil {
			t.Fatal(err)
		}
	}
	events := func(corpusID string) []changes.Event {
		t.Helper()
		w, err := store.ReadChanges(ctx, scope.Organization, corpusID, 0, 100, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return w.Events
	}
	count := func(corpusID, kind, id string) int {
		n := 0
		for _, e := range events(corpusID) {
			if e.Type == kind && e.ResourceID == id {
				n++
			}
		}
		return n
	}
	position := func(corpusID, kind, id string) int64 {
		for _, e := range events(corpusID) {
			if e.Type == kind && e.ResourceID == id {
				return e.Position
			}
		}
		t.Fatalf("no %s for %s", kind, id)
		return 0
	}

	service := monitoring.Service{Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"receiver": {Organization: scope.Organization, URL: "http://127.0.0.1:9/hook", Secret: "whsec_test"}}}
	definition := monitoring.Definition{CorpusIDs: []string{a.ID, b.ID}, Expression: map[string]any{"fixture": map[string]any{"decision": "match"}}, RetrievalProfile: "balanced", TemporalPolicy: "from_activation"}
	query, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: definition})
	if err != nil || replayed.ID != query.ID || replayed.Current.VersionID != query.Current.VersionID {
		t.Fatal("saved query replay", replayed, err)
	}
	changed := definition
	changed.Expression = map[string]any{"fixture": "other"}
	if _, err = service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: changed}); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatal("changed saved query replay", err)
	}
	for _, c := range []string{a.ID, b.ID} {
		if n := count(c, "saved_query.created", query.ID); n != 1 {
			t.Fatalf("saved_query.created in %s: %d", c, n)
		}
	}

	accept("before")
	input := monitoring.SubscriptionInput{Key: "s", Name: "S", SavedQueryID: query.ID, SavedQueryVersionID: query.Current.VersionID, Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"mode": "fixture"}}, DestinationID: "receiver"}
	sub, err := service.CreateSubscription(ctx, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	accept("after")
	before := position(a.ID, "record.accepted", content.StableID("record", scope.Organization, a.ID, "monitoring", "before"))
	after := position(a.ID, "record.accepted", content.StableID("record", scope.Organization, a.ID, "monitoring", "after"))
	activated := position(a.ID, "subscription.created", sub.ID)
	boundary := sub.Current.ActivationPosition
	if !(before < activated && activated <= boundary && boundary < after) {
		t.Fatalf("activation boundary not ordered: before=%d event=%d boundary=%d after=%d", before, activated, boundary, after)
	}
	if count(b.ID, "subscription.created", sub.ID) != 1 {
		t.Fatal("subscription.created must be visible once per pinned Corpus")
	}
	read, err := service.Subscription(ctx, scope, sub.ID)
	if err != nil || !read.Enabled || read.Current.ActivationPosition != boundary || read.Current.Evaluator.Configuration["mode"] != "fixture" || read.Current.DestinationID != "receiver" {
		t.Fatal("pinned subscription read", read, err)
	}

	disabled, err := service.DisableSubscription(ctx, scope, "d1", sub.ID)
	if err != nil || disabled.Enabled {
		t.Fatal("disable", disabled, err)
	}
	if again, err := service.DisableSubscription(ctx, scope, "d2", sub.ID); err != nil || again.Enabled {
		t.Fatal("repeat disable", again, err)
	}
	if again, err := service.DisableSubscription(ctx, scope, "d1", sub.ID); err != nil || again.Enabled {
		t.Fatal("replayed disable", again, err)
	}
	for _, c := range []string{a.ID, b.ID} {
		if n := count(c, "subscription.disabled", sub.ID); n != 1 {
			t.Fatalf("subscription.disabled in %s: %d", c, n)
		}
	}
	recreated, err := service.CreateSubscription(ctx, scope, input)
	if err != nil || recreated.ID != sub.ID || recreated.Enabled || recreated.Current.VersionID != sub.Current.VersionID {
		t.Fatal("creation replay reenabled or changed the Subscription", recreated, err)
	}
	if n := count(a.ID, "subscription.created", sub.ID); n != 1 {
		t.Fatalf("creation replay emitted another activation: %d", n)
	}
	changedInput := input
	changedInput.Name = "Renamed"
	if _, err = service.CreateSubscription(ctx, scope, changedInput); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatal("changed subscription replay", err)
	}

	other, err := service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: "s2", Name: "S2", SavedQueryID: query.ID, SavedQueryVersionID: query.Current.VersionID, Evaluator: input.Evaluator, DestinationID: "receiver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.DisableSubscription(ctx, scope, "d1", other.ID); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatal("disable key reused for another Subscription", err)
	}
	if still, err := service.Subscription(ctx, scope, other.ID); err != nil || !still.Enabled {
		t.Fatal("conflicting disable changed state", still, err)
	}
	if _, err = service.Subscription(ctx, corpus.Scope{Organization: "adapter-monitoring-other", Actions: scope.Actions, Corpora: []string{"*"}}, sub.ID); !errors.Is(err, monitoring.ErrNotFound) {
		t.Fatal("cross-Organization read", err)
	}
}

// TestConcurrentMonitoringReplaysConverge races identical commands under the
// same idempotency key: every caller observes one resource and the journal
// records each fact once.
func TestConcurrentMonitoringReplaysConverge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-monitoring-race-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	a, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	service := monitoring.Service{Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"receiver": {Organization: scope.Organization, URL: "http://127.0.0.1:9/hook", Secret: "whsec_test"}}}
	race := func(n int, run func() (string, bool, error)) string {
		t.Helper()
		type outcome struct {
			id      string
			enabled bool
			err     error
		}
		results := make(chan outcome, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			go func() {
				<-start
				id, enabled, err := run()
				results <- outcome{id, enabled, err}
			}()
		}
		close(start)
		var first outcome
		for i := 0; i < n; i++ {
			r := <-results
			if r.err != nil {
				t.Fatal(r.err)
			}
			if i == 0 {
				first = r
			} else if r != first {
				t.Fatalf("concurrent replays diverged: %+v %+v", first, r)
			}
		}
		return first.id
	}
	definition := monitoring.Definition{CorpusIDs: []string{a.ID}, Expression: map[string]any{"fixture": "match"}, RetrievalProfile: "balanced", TemporalPolicy: "from_activation"}
	queryID := race(8, func() (string, bool, error) {
		q, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: definition})
		return q.ID, true, err
	})
	query, err := service.SavedQuery(ctx, scope, queryID)
	if err != nil {
		t.Fatal(err)
	}
	versionID := query.Current.VersionID
	input := monitoring.SubscriptionInput{Key: "s", Name: "S", SavedQueryID: queryID, SavedQueryVersionID: versionID, Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{}}, DestinationID: "receiver"}
	subID := race(8, func() (string, bool, error) {
		s, err := service.CreateSubscription(ctx, scope, input)
		return s.ID, s.Enabled, err
	})
	race(8, func() (string, bool, error) {
		s, err := service.DisableSubscription(ctx, scope, "d", subID)
		return s.ID, s.Enabled, err
	})
	w, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range w.Events {
		counts[e.Type]++
	}
	if counts["saved_query.created"] != 1 || counts["subscription.created"] != 1 || counts["subscription.disabled"] != 1 {
		t.Fatalf("concurrent replays duplicated journal facts: %v", counts)
	}
}
