package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/app"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5/pgxpool"
)

type retirementFixture struct {
	ctx          context.Context
	pool         *pgxpool.Pool
	store        postgres.ContentStore
	scope        corpus.Scope
	live         *monitoring.LiveEvaluators
	service      monitoring.Service
	subscription monitoring.Subscription
}

func newRetirementFixture(t *testing.T) retirementFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	scope := corpus.Scope{Organization: "org_a", Corpora: []string{"*"}, Actions: []string{"corpora:write", "content:write", "monitoring:write", "plugins:admin"}}
	collection, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "C"})
	if err != nil {
		t.Fatal(err)
	}
	live := &monitoring.LiveEvaluators{}
	live.Store(monitoring.PlanEvaluators{Served: monitoring.Evaluators{"alert-rules@0.1.0": monitoring.Fixture{}}})
	service := monitoring.Service{Store: store, Corpora: store, Moves: store, Evaluations: store, Evaluators: live, Destinations: map[string]monitoring.Destination{"dest": {Organization: scope.Organization}}}
	query, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{collection.ID}, Expression: map[string]any{}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: "s", Name: "S", SavedQueryID: query.ID, SavedQueryVersionID: query.Current.VersionID, DestinationID: "dest", Evaluator: monitoring.Evaluator{PluginID: "alert-rules", Version: "0.1.0", Configuration: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	contents := content.Service{Repository: store, Baseline: store}
	command := content.Command{Key: "article", Source: content.Source{CorpusID: collection.ID, Namespace: "wire", RecordKey: "article"}, Content: content.Text{Kind: "text", Text: "Article"}}
	receipt, err := contents.Accept(ctx, scope, command)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, scope.Organization, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/text", SHA256: "text", Size: 7}, content.Blob{Key: "fixture/manifest", SHA256: "manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	version := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(command)}
	segmentation := wholeBodySegmentation(scope.Organization, version)
	if err = contents.SaveSegmentation(ctx, scope.Organization, version, segmentation); err != nil {
		t.Fatal(err)
	}
	generation, err := store.Generation(ctx, scope.Organization, collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = contents.Promote(ctx, scope.Organization, segmentation, generation); err != nil {
		t.Fatal(err)
	}
	evaluations := postgres.EvaluationStore{ContentStore: store}
	if _, err = evaluations.FanOut(ctx); err != nil {
		t.Fatal(err)
	}
	return retirementFixture{ctx: ctx, pool: pool, store: store, scope: scope, live: live, service: service, subscription: subscription}
}

func TestRetireEvaluationsAfterEvaluatorMigration(t *testing.T) {
	fixture := newRetirementFixture(t)
	ctx, pool, scope, live, service, subscription := fixture.ctx, fixture.pool, fixture.scope, fixture.live, fixture.service, fixture.subscription
	evaluations := postgres.EvaluationStore{ContentStore: fixture.store}
	live.Store(monitoring.PlanEvaluators{Served: monitoring.Evaluators{"alert-rules@0.2.0": monitoring.Fixture{}}})
	migration, err := service.MigrateEvaluator(ctx, scope, monitoring.EvaluatorMigrationInput{PluginID: "alert-rules", FromVersion: "0.1.0"})
	if err != nil || len(migration.Moved) != 1 {
		t.Fatalf("migration: %+v, %v", migration, err)
	}
	engine := monitoring.Engine{Store: evaluations, Evaluators: live}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err = pool.Exec(ctx, `UPDATE evaluation_intents SET available_at='-infinity' WHERE organization=$1`, scope.Organization); err != nil {
			t.Fatal(err)
		}
		if progressed, err := engine.Step(ctx); err != nil || !progressed {
			t.Fatalf("attempt %d: progressed=%v, err=%v", attempt, progressed, err)
		}
	}
	var state, outcome, code, pinned string
	var attempts int
	if err = pool.QueryRow(ctx, `SELECT state,outcome,error_code,subscription_version_id,attempts FROM evaluation_intents WHERE organization=$1`, scope.Organization).Scan(&state, &outcome, &code, &pinned, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || code != "evaluator_unavailable" || pinned != subscription.Current.VersionID || attempts != 2 {
		t.Fatalf("old evaluation after migration: state=%s error=%s pinned=%s attempts=%d", state, code, pinned, attempts)
	}
	input := monitoring.EvaluationRetirementInput{Key: "retire-old", PluginID: "alert-rules", Version: "0.1.0", Reason: "The old build is no longer available", Limit: 100}
	backlog, err := service.EvaluationBacklog(ctx, scope, "", 100)
	if err != nil || len(backlog.Items) != 1 || backlog.Items[0].Pending != 1 || backlog.Items[0].Unavailable != 1 || backlog.Items[0].Version != "0.1.0" {
		t.Fatalf("backlog before retirement: %+v, %v", backlog, err)
	}
	input.DryRun = true
	preview, err := service.RetireEvaluations(ctx, scope, input)
	if err != nil || len(preview.Items) != 1 || preview.CreatedAt != nil || preview.Items[0].EventID != "" {
		t.Fatalf("dry run: %+v, %v", preview, err)
	}
	var receipts, events int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM evaluation_retirements),(SELECT count(*) FROM change_events WHERE event_type='evaluation.retired')`).Scan(&receipts, &events); err != nil || receipts != 0 || events != 0 {
		t.Fatalf("dry run wrote receipts=%d events=%d: %v", receipts, events, err)
	}
	input.DryRun = false
	retired, err := service.RetireEvaluations(ctx, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT state,outcome FROM evaluation_intents WHERE organization=$1`, scope.Organization).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "done" || outcome != monitoring.OutcomeEvaluatorRetired {
		t.Fatalf("retirement left the migrated backlog at state=%s outcome=%s; want done/evaluator_retired", state, outcome)
	}
	if retired.CreatedAt == nil || len(retired.Items) != 1 || retired.Items[0].SubscriptionVersionID != subscription.Current.VersionID || retired.Items[0].EventID == "" || retired.Remaining != 0 {
		t.Fatalf("retirement audit: %+v", retired)
	}
	read, err := service.EvaluationRetirement(ctx, scope, retired.ID)
	if err != nil || !reflect.DeepEqual(read, retired) {
		t.Fatalf("durable receipt: %+v, %v; want %+v", read, err, retired)
	}
	var eventID, resourceID string
	if err = pool.QueryRow(ctx, `SELECT event_id,resource_id FROM change_events WHERE event_type='evaluation.retired'`).Scan(&eventID, &resourceID); err != nil || eventID != retired.Items[0].EventID || resourceID != retired.ID {
		t.Fatalf("audit event: event=%s resource=%s, %v", eventID, resourceID, err)
	}
	item := retired.Items[0]
	stale := monitoring.Intent{Organization: scope.Organization, Kind: monitoring.IntentEvaluation, SubscriptionID: item.SubscriptionID, SubscriptionVersionID: item.SubscriptionVersionID, Sequence: item.Sequence, CorpusID: item.CorpusID, RecordID: item.RecordID, VersionID: item.RecordVersionID}
	if outcome, err := evaluations.CommitMatch(ctx, stale, monitoring.MatchEvidence{Evaluator: subscription.Current.Evaluator, Explanation: "A late evaluator answer"}); err != nil || outcome != monitoring.OutcomeEvaluatorRetired {
		t.Fatalf("stale worker commit after retirement: %s, %v", outcome, err)
	}
	var matches, deliveries int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM matches),(SELECT count(*) FROM deliveries)`).Scan(&matches, &deliveries); err != nil || matches != 0 || deliveries != 0 {
		t.Fatalf("retirement produced matches=%d deliveries=%d: %v", matches, deliveries, err)
	}
	backlog, err = service.EvaluationBacklog(ctx, scope, "", 100)
	if err != nil || len(backlog.Items) != 1 || backlog.Items[0].Pending != 0 || backlog.Items[0].Retired != 1 {
		t.Fatalf("terminal backlog: %+v, %v", backlog, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,error_code)
VALUES($1,$2,$3,$4,$5,$6,$7,'evaluator_unavailable')`, scope.Organization, item.SubscriptionVersionID, item.Sequence+100, item.SubscriptionID, item.CorpusID, item.RecordID, item.RecordVersionID); err != nil {
		t.Fatal(err)
	}
	service.Evaluations = postgres.ContentStore{Pool: pool}
	replay, err := service.RetireEvaluations(ctx, scope, input)
	if err != nil || !reflect.DeepEqual(replay, retired) {
		t.Fatalf("key replay after new work: %+v, %v; want original %+v", replay, err, retired)
	}
	input.Reason = "Another reason"
	if _, err = service.RetireEvaluations(ctx, scope, input); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatalf("changed reason reused key: %v, want idempotency_conflict", err)
	}
	backlog, err = service.EvaluationBacklog(ctx, scope, "", 100)
	if err != nil || backlog.Items[0].Pending != 1 || backlog.Items[0].Retired != 1 {
		t.Fatalf("replay retired additional work: %+v, %v", backlog, err)
	}
}

func TestEvaluationRetirementScopeAndLeases(t *testing.T) {
	fixture := newRetirementFixture(t)
	ctx, pool, scope, service := fixture.ctx, fixture.pool, fixture.scope, fixture.service
	old := fixture.subscription.Current
	if _, err := pool.Exec(ctx, `UPDATE evaluation_intents SET error_code='evaluator_unavailable',lease_until='infinity'`); err != nil {
		t.Fatal(err)
	}
	collection := old.CorpusIDs[0]
	for _, row := range []struct {
		sequence                         int
		corpus, kind, code, state, lease string
	}{
		{101, collection, "evaluation", "evaluator_unavailable", "pending", "-infinity"},
		{102, "private", "evaluation", "evaluator_unavailable", "pending", "-infinity"},
		{103, collection, "withdrawal", "evaluator_unavailable", "pending", "-infinity"},
		{104, collection, "evaluation", "evaluator_error", "pending", "-infinity"},
		{105, collection, "evaluation", "", "pending", "-infinity"},
		{106, collection, "evaluation", "evaluator_unavailable", "done", "-infinity"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,kind,error_code,state,lease_until)
VALUES($1,$2,$3,$4,$5,'record','version',$6,$7,$8,$9::timestamptz)`, scope.Organization, old.VersionID, row.sequence, old.SubscriptionID, row.corpus, row.kind, row.code, row.state, row.lease); err != nil {
			t.Fatal(err)
		}
	}
	otherScope := scope
	otherScope.Organization = "org_b"
	otherCorpus, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, otherScope, corpus.CreateInput{Key: "other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	otherService := service
	otherService.Destinations = map[string]monitoring.Destination{"dest": {Organization: otherScope.Organization}}
	query, err := otherService.CreateSavedQuery(ctx, otherScope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{otherCorpus.ID}, Expression: map[string]any{}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	otherSub, err := otherService.CreateSubscription(ctx, otherScope, monitoring.SubscriptionInput{Key: "s", Name: "S", SavedQueryID: query.ID, SavedQueryVersionID: query.Current.VersionID, DestinationID: "dest", Evaluator: old.Evaluator})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,error_code)
VALUES($1,$2,101,$3,$4,'other-record','other-version','evaluator_unavailable')`, otherScope.Organization, otherSub.Current.VersionID, otherSub.ID, otherCorpus.ID); err != nil {
		t.Fatal(err)
	}
	newVersion, err := fixture.store.MoveEvaluator(ctx, scope.Organization, old, monitoring.Evaluator{PluginID: "alert-rules", Version: "0.2.0", Configuration: old.Evaluator.Configuration})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,error_code)
VALUES($1,$2,107,$3,$4,'new-record','new-version','evaluator_unavailable')`, scope.Organization, newVersion.VersionID, old.SubscriptionID, collection); err != nil {
		t.Fatal(err)
	}
	scope.Corpora = []string{collection}
	input := monitoring.EvaluationRetirementInput{Key: "scoped", PluginID: "alert-rules", Version: "0.1.0", Reason: "Retire a scoped batch", Limit: 1}
	retired, err := service.RetireEvaluations(ctx, scope, input)
	if err != nil || len(retired.Items) != 1 || retired.Items[0].Sequence != 101 || retired.Leased != 1 || retired.Remaining != 1 {
		t.Fatalf("scope/lease selection: %+v, %v", retired, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM evaluation_intents WHERE outcome='evaluator_retired'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retired ungranted, withdrawal, healthy, complete or leased work: %d, %v", count, err)
	}
	backlog, err := service.EvaluationBacklog(ctx, scope, "", 100)
	if err != nil || len(backlog.Items) != 2 || backlog.Items[0].Pending != 3 || backlog.Items[0].Unavailable != 1 || backlog.Items[0].Erroring != 2 || backlog.Items[0].Retired != 1 || backlog.Items[1].Version != "0.2.0" || backlog.Items[1].Unavailable != 1 {
		t.Fatalf("scoped diagnostics: %+v, %v", backlog, err)
	}
	first, err := service.EvaluationBacklog(ctx, scope, "", 1)
	if err != nil || len(first.Items) != 1 || first.Next != "alert-rules@0.1.0" {
		t.Fatalf("backlog first page: %+v, %v", first, err)
	}
	last, err := service.EvaluationBacklog(ctx, scope, first.Next, 1)
	if err != nil || len(last.Items) != 1 || last.Items[0].Version != "0.2.0" || last.Next != "" {
		t.Fatalf("backlog continuation: %+v, %v", last, err)
	}
	for _, hidden := range []corpus.Scope{
		{Organization: "org_b", Corpora: []string{"*"}, Actions: []string{"plugins:admin"}},
		{Organization: "org_a", Corpora: []string{"private"}, Actions: []string{"plugins:admin"}},
	} {
		if _, err := service.EvaluationRetirement(ctx, hidden, retired.ID); !errors.Is(err, monitoring.ErrNotFound) {
			t.Fatalf("hidden receipt for %+v: %v", hidden, err)
		}
	}
	if _, err = service.RetireEvaluations(ctx, fixture.scope, input); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatalf("broader scope reused key: %v, want idempotency_conflict", err)
	}
	input.Key = "empty"
	retired, err = service.RetireEvaluations(ctx, scope, input)
	if err != nil || len(retired.Items) != 0 || retired.Leased != 1 {
		t.Fatalf("leased-only batch: %+v, %v", retired, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE evaluation_intents SET lease_until='-infinity' WHERE organization=$1`, scope.Organization); err != nil {
		t.Fatal(err)
	}
	replay, err := service.RetireEvaluations(ctx, scope, input)
	if err != nil || !reflect.DeepEqual(replay, retired) {
		t.Fatalf("empty receipt replay: %+v, %v", replay, err)
	}
	input.Key = "lease-expired"
	if retired, err = service.RetireEvaluations(ctx, scope, input); err != nil || len(retired.Items) != 1 || retired.Remaining != 0 {
		t.Fatalf("new key after lease expiry: %+v, %v", retired, err)
	}
}

func TestEmptyRetirementBeforeOrganizationInitialization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	scope := corpus.Scope{Organization: "new-org", Corpora: []string{"*"}, Actions: []string{"plugins:admin", "corpora:write"}}
	service := monitoring.Service{Evaluations: postgres.ContentStore{Pool: pool}}
	input := monitoring.EvaluationRetirementInput{Key: "empty", PluginID: "alert-rules", Version: "0.1.0", Reason: "Retire an empty batch", DryRun: true}
	preview, err := service.RetireEvaluations(ctx, scope, input)
	if err != nil || len(preview.Items) != 0 || preview.CreatedAt != nil {
		t.Fatalf("uninitialized dry run: %+v, %v", preview, err)
	}
	var journals int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM organization_journals WHERE organization=$1`, scope.Organization).Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("dry run initialized a journal: %d, %v", journals, err)
	}
	input.DryRun = false
	retired, err := service.RetireEvaluations(ctx, scope, input)
	if err != nil || len(retired.Items) != 0 || retired.CreatedAt == nil {
		t.Fatalf("uninitialized real action has no durable receipt: %+v, %v", retired, err)
	}
	read, err := service.EvaluationRetirement(ctx, scope, retired.ID)
	if err != nil || !reflect.DeepEqual(read, retired) {
		t.Fatalf("uninitialized receipt lookup: %+v, %v; want %+v", read, err, retired)
	}
	if _, _, err = (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "C"}); err != nil {
		t.Fatal(err)
	}
	replay, err := service.RetireEvaluations(ctx, scope, input)
	if err != nil || !reflect.DeepEqual(replay, retired) {
		t.Fatalf("empty receipt after initialization: %+v, %v; want %+v", replay, err, retired)
	}
	input.Reason = "Another reason"
	if _, err = service.RetireEvaluations(ctx, scope, input); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatalf("uninitialized key not reserved: %v", err)
	}
}
