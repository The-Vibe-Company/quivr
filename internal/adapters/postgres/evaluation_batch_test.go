package postgres_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// acceptAll stands for a pinned subscription plugin at creation time.
type acceptAll struct{}

func (acceptAll) MaxBatch() int                                 { return 8 }
func (acceptAll) Validate(map[string]any, map[string]any) error { return nil }
func (acceptAll) Evaluate(context.Context, monitoring.Batch) ([]monitoring.Outcome, error) {
	return nil, monitoring.ErrEvaluatorUnavailable
}

// TestClaimRelatedGroupsOneVersionAndEvaluator proves the batching claim: it
// leases the other due intents of the same Record Version whose Subscription
// Version pins the same evaluator, never the first intent, another Version,
// another evaluator or an intent already leased; and it reads the metadata a
// rule may test.
func TestClaimRelatedGroupsOneVersionAndEvaluator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	run := fmt.Sprint(time.Now().UnixNano())
	org := "adapter-batch-" + run
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	a, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	contents := content.Service{Repository: store, Baseline: store}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE evaluation_intents SET state='done',outcome='test_cleanup' WHERE organization=$1 AND state='pending'`, org)
	})
	publish := func(cmd content.Command) (string, string) {
		t.Helper()
		r, err := contents.Accept(ctx, scope, cmd)
		if err != nil {
			t.Fatal(err)
		}
		work, _, err := store.Work(ctx, org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/" + cmd.Key, SHA256: "text-" + run + cmd.Key, Size: 10}, content.Blob{Key: "fixture/m-" + cmd.Key, SHA256: "manifest-" + run + cmd.Key, Size: 2})); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1`, org); err != nil {
			t.Fatal(err)
		}
		return work.RecordID, work.VersionID
	}
	trigger := func(corpusID, recordID, versionID string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `WITH s AS (UPDATE organization_journals SET last_sequence=last_sequence+1 WHERE organization=$1 RETURNING last_sequence)
INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id) SELECT $1,s.last_sequence,$2||s.last_sequence,$3,'record.retrieval_ready','record',$4,$5 FROM s`, org, "event_batch_"+run+"_", corpusID, recordID, versionID); err != nil {
			t.Fatal(err)
		}
	}

	evaluators := monitoring.FixtureEvaluators()
	evaluators["acme.alerts@0.1.0"] = acceptAll{}
	service := monitoring.Service{Evaluators: evaluators, Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"dest": {Organization: org}}}
	q, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{a.ID}, Expression: map[string]any{"text": "strike"}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	subscribe := func(key string, evaluator monitoring.Evaluator) string {
		t.Helper()
		s, err := service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: key, Name: key, SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, Evaluator: evaluator, DestinationID: "dest"})
		if err != nil {
			t.Fatal(err)
		}
		return s.Current.VersionID
	}
	alerts := monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0", Configuration: map[string]any{}}
	fixture := monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{}}
	alertVersions := []string{subscribe("a1", alerts), subscribe("a2", alerts), subscribe("a3", alerts)}
	subscribe("f1", fixture)

	source := content.Source{CorpusID: a.ID, Namespace: "wire", RecordKey: "story-" + run}
	record, version := publish(content.Command{Key: "client-" + run, Source: source, Position: "42", Content: content.Text{Kind: "text", Text: "Harbour strike"},
		Provenance: map[string]any{"producer": "newsdesk", "producer_version": "2"}})
	otherRecord, otherVersion := publish(content.Command{Key: "other-" + run, Source: content.Source{CorpusID: a.ID, Namespace: "wire", RecordKey: "other-" + run}, Content: content.Text{Kind: "text", Text: "Other"}})
	trigger(a.ID, record, version)
	trigger(a.ID, otherRecord, otherVersion)
	evaluation := postgres.EvaluationStore{ContentStore: store}
	for i := 0; i < 20; i++ {
		n, err := evaluation.FanOut(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}

	var first monitoring.Intent
	if err := pool.QueryRow(ctx, `SELECT kind,organization,subscription_id,subscription_version_id,sequence,corpus_id,record_id,record_version_id,attempts FROM evaluation_intents
WHERE organization=$1 AND record_version_id=$2 AND subscription_version_id=$3`, org, version, alertVersions[0]).Scan(
		&first.Kind, &first.Organization, &first.SubscriptionID, &first.SubscriptionVersionID, &first.Sequence, &first.CorpusID, &first.RecordID, &first.VersionID, &first.Attempts); err != nil {
		t.Fatal(err)
	}
	related, err := evaluation.ClaimRelated(ctx, first, alerts, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, in := range related {
		if in.VersionID != version || in.Kind != monitoring.IntentEvaluation {
			t.Fatalf("claimed another Version or kind: %+v", in)
		}
		got = append(got, in.SubscriptionVersionID)
	}
	sort.Strings(got)
	want := append([]string(nil), alertVersions[1:]...)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("related %v, want %v (same evaluator only, first excluded)", got, want)
	}
	if again, err := evaluation.ClaimRelated(ctx, first, alerts, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("leased intents claimed again: %v %v", again, err)
	}
	if limited, err := evaluation.ClaimRelated(ctx, first, fixture, 0, time.Minute); err != nil || len(limited) != 0 {
		t.Fatalf("zero limit: %v %v", limited, err)
	}

	metadata, err := evaluation.RecordMetadata(ctx, org, record, version)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Source != (monitoring.SourceIdentity{Namespace: "wire", RecordKey: source.RecordKey, Position: "42"}) || metadata.AcceptedAt.IsZero() ||
		metadata.Provenance.Origin != monitoring.OriginClient || metadata.Provenance.Producer != "newsdesk" || metadata.Provenance.Connector != nil {
		t.Fatalf("client metadata %+v", metadata)
	}
	// A revision accepted under the Connector Instance key family is connector-originated.
	connRecord, connVersion := publish(content.Command{Key: connectors.KeyPrefix + run, Source: content.Source{CorpusID: a.ID, Namespace: "feed", RecordKey: "item-" + run}, Content: content.Text{Kind: "text", Text: "Feed item"},
		Extensions: content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{"author": "Jane Doe"}}},
		Provenance: map[string]any{"producer": "conn_1", "producer_version": "rss/1"}})
	metadata, err = evaluation.RecordMetadata(ctx, org, connRecord, connVersion)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Provenance.Origin != monitoring.OriginConnector || metadata.Provenance.Connector == nil || *metadata.Provenance.Connector != (monitoring.ConnectorOrigin{InstanceID: "conn_1", Kind: "rss"}) {
		t.Fatalf("connector metadata %+v", metadata.Provenance)
	}
	editorial, _ := metadata.Extensions["example.editorial"].(map[string]any)
	if data, _ := editorial["data"].(map[string]any); data["author"] != "Jane Doe" {
		t.Fatalf("extensions %+v", metadata.Extensions)
	}
}

// TestConcurrentClaimsTakeOneArticleOnce races many workers' claims on one
// Record Version with several due intents: at most one claim may win it, so
// its Subscriptions are decided in one batch by the winner (ClaimRelated)
// and never split between workers' plugin calls.
func TestConcurrentClaimsTakeOneArticleOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	run := fmt.Sprint(time.Now().UnixNano())
	org := "adapter-claim-race-" + run
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	a, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	contents := content.Service{Repository: store, Baseline: store}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE evaluation_intents SET state='done',outcome='test_cleanup' WHERE organization=$1 AND state='pending'`, org)
	})
	r, err := contents.Accept(ctx, scope, content.Command{Key: "race-" + run, Source: content.Source{CorpusID: a.ID, Namespace: "wire", RecordKey: "race-" + run}, Content: content.Text{Kind: "text", Text: "Harbour strike"}})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/race-" + run, SHA256: "text-race-" + run, Size: 10}, content.Blob{Key: "fixture/m-race-" + run, SHA256: "manifest-race-" + run, Size: 2})); err != nil {
		t.Fatal(err)
	}
	evaluators := monitoring.FixtureEvaluators()
	evaluators["acme.alerts@0.1.0"] = acceptAll{}
	service := monitoring.Service{Evaluators: evaluators, Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"dest": {Organization: org}}}
	q, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{a.ID}, Expression: map[string]any{"text": "strike"}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err = service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: fmt.Sprint("s", i), Name: fmt.Sprint("S", i), SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID,
			Evaluator: monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0", Configuration: map[string]any{}}, DestinationID: "dest"}); err != nil {
			t.Fatal(err)
		}
	}
	// Six due intents of one Record Version, as a fan-out writes them.
	if _, err = pool.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,kind)
SELECT $1,v.id,1,v.subscription_id,$2,$3,$4,'evaluation' FROM subscription_versions v WHERE v.organization=$1`, org, a.ID, work.RecordID, work.VersionID); err != nil {
		t.Fatal(err)
	}
	evaluation := postgres.EvaluationStore{ContentStore: store}
	const workers, rounds = 8, 40
	for round := 0; round < rounds; round++ {
		// Due before any other test's work, so the claims race for this Version.
		if _, err = pool.Exec(ctx, `UPDATE evaluation_intents SET lease_until='-infinity',available_at='2000-01-01' WHERE organization=$1`, org); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		won := make(chan monitoring.Intent, workers)
		failed := make(chan error, workers)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				in, err := evaluation.Claim(ctx, time.Minute)
				switch {
				case err != nil && err != monitoring.ErrNoWork:
					failed <- err
				case err == nil && in.Organization == org:
					won <- in
				}
			}()
		}
		close(start)
		wg.Wait()
		close(won)
		close(failed)
		for err := range failed {
			t.Fatal(err)
		}
		var claimed []string
		for in := range won {
			claimed = append(claimed, in.SubscriptionVersionID)
		}
		if len(claimed) != 1 {
			t.Fatalf("round %d: %d workers claimed the same Record Version (%v); want exactly one", round, len(claimed), claimed)
		}
	}
}
