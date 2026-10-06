package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// TestConnectorInstancesPersistSecretsSealedAndScheduleOneRunAtATime drives
// the real store: idempotent create, namespace ownership, sealed credentials,
// leased claims, stale-run fencing and transactional health events.
func TestConnectorInstancesPersistSecretsSealedAndScheduleOneRunAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-connectors-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:read", "changes:read"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Connectors"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{Pool: pool}
	registry, _ := connectors.NewRegistry(fakeplugin.FixtureConnector{})
	sealer, _ := connectors.NewSealer("adapter-test-credential-key-0123456789")
	service := connectors.Service{Store: store, Registry: registry, Sealer: sealer, MinInterval: time.Second}
	one := 1
	input := connectors.CreateInput{Key: "k1", CorpusID: c.ID, Namespace: "wire", Kind: "fixture", Config: json.RawMessage(`{"script":[]}`), IntervalSeconds: &one, Secret: json.RawMessage(`{"token":"fixture-test-secret-adapter"}`)}
	created, err := service.Create(ctx, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Credential == nil || created.Credential.Version != 1 || created.Health.State != connectors.HealthActive {
		t.Fatalf("created %+v", created)
	}
	replay, err := service.Create(ctx, scope, input)
	if err != nil || replay.ID != created.ID {
		t.Fatalf("replay %v %+v", err, replay)
	}
	changed := input
	changed.Namespace = "other"
	if _, err = service.Create(ctx, scope, changed); !errors.Is(err, connectors.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	second := input
	second.Key = "k2"
	if _, err = service.Create(ctx, scope, second); !errors.Is(err, connectors.ErrNamespaceInUse) {
		t.Fatalf("namespace: %v", err)
	}
	var ciphertext []byte
	if err = pool.QueryRow(ctx, "SELECT ciphertext FROM connector_credentials WHERE organization=$1 AND connector_id=$2", scope.Organization, created.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("fixture-test-secret")) {
		t.Fatal("secret stored in plaintext")
	}

	// Exactly one claimant owns a due run; a released lease can be claimed again.
	claim := func() []connectors.ConnectorRun {
		runs, err := store.ClaimConnectorRuns(ctx, time.Minute, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var mine []connectors.ConnectorRun
		for _, r := range runs {
			if r.Organization == scope.Organization {
				mine = append(mine, r)
			} else if err := store.ReleaseConnectorRun(ctx, r); err != nil {
				t.Fatal(err) // never hold another Organization's run
			}
		}
		return mine
	}
	first := claim()
	if len(first) != 1 || first[0].ConnectorID != created.ID {
		t.Fatalf("claim %+v", first)
	}
	if again := claim(); len(again) != 0 {
		t.Fatalf("leased run claimed twice: %+v", again)
	}
	if err = store.ReleaseConnectorRun(ctx, first[0]); err != nil {
		t.Fatal(err)
	}
	run := claim()
	if len(run) != 1 || run[0].Run != first[0].Run {
		t.Fatalf("re-dispatch must target the same run: %+v", run)
	}
	target, err := store.LoadRun(ctx, scope.Organization, created.ID)
	if err != nil || target.Sealed == nil {
		t.Fatalf("load %v %+v", err, target)
	}
	if plain, err := sealer.Open(scope.Organization, created.ID, *target.Sealed); err != nil || string(plain) != `{"token":"fixture-test-secret-adapter"}` {
		t.Fatalf("open %v %s", err, plain)
	}
	if ok, err := store.CommitCheckpoint(ctx, scope.Organization, created.ID, run[0].Run+1, connectors.Progress{Checkpoint: json.RawMessage(`{"step":9}`), Items: true, Reads: 50}); ok || err != nil {
		t.Fatalf("stale checkpoint committed: %v %v", ok, err)
	}
	if fresh, _ := store.ReadConnector(ctx, scope.Organization, created.ID); fresh.Health.Usage != nil || fresh.Health.Diagnostics != nil {
		t.Fatalf("a kind that never reported reads has no usage: %+v", fresh.Health)
	}
	if ok, err := store.CommitCheckpoint(ctx, scope.Organization, created.ID, run[0].Run, connectors.Progress{Checkpoint: json.RawMessage(`{"step":0}`), Reads: 30, Diagnostics: json.RawMessage(`{"window":1}`)}); !ok || err != nil {
		t.Fatalf("checkpoint: %v %v", ok, err)
	}
	if ok, err := store.CommitCheckpoint(ctx, scope.Organization, created.ID, run[0].Run, connectors.Progress{Checkpoint: json.RawMessage(`{"step":1}`), Items: true, Reads: 12}); !ok || err != nil {
		t.Fatalf("checkpoint: %v %v", ok, err)
	}
	// Reads accumulate per UTC day; a page without diagnostics keeps the last ones.
	counted, err := store.LoadRun(ctx, scope.Organization, created.ID)
	if err != nil || counted.ReadsToday != 42 || counted.Health.Usage == nil || counted.Health.Usage.ItemsRead != 42 || counted.Health.Usage.PreviousDayItemsRead != 0 || string(counted.Health.Diagnostics) != `{"window": 1}` {
		t.Fatalf("usage %v %+v %s", err, counted.Health.Usage, counted.Health.Diagnostics)
	}
	// A new UTC day rolls today's reads over to the previous day.
	if _, err = pool.Exec(ctx, "UPDATE connector_instances SET usage_day=usage_day-1 WHERE organization=$1 AND id=$2", scope.Organization, created.ID); err != nil {
		t.Fatal(err)
	}
	if rolled, _ := store.ReadConnector(ctx, scope.Organization, created.ID); rolled.Health.Usage == nil || rolled.Health.Usage.ItemsRead != 0 || rolled.Health.Usage.PreviousDayItemsRead != 42 {
		t.Fatalf("rollover on read %+v", rolled.Health.Usage)
	}
	if ok, err := store.CommitCheckpoint(ctx, scope.Organization, created.ID, run[0].Run, connectors.Progress{Checkpoint: json.RawMessage(`{"step":1}`), Reads: 5}); !ok || err != nil {
		t.Fatalf("checkpoint: %v %v", ok, err)
	}
	if rolled, _ := store.ReadConnector(ctx, scope.Organization, created.ID); rolled.Health.Usage.ItemsRead != 5 || rolled.Health.Usage.PreviousDayItemsRead != 42 {
		t.Fatalf("rollover on commit %+v", rolled.Health.Usage)
	}
	feed := changes.Service{Journal: postgres.ChangeStore{Pool: store.Pool}, Key: []byte("adapter-cursor-key-0123456789abcdef")}
	start, err := feed.Start(ctx, scope, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRun(ctx, scope.Organization, created.ID, run[0].Run, &connectors.RunError{Class: connectors.ClassAccess, Code: "unauthorized"}); err != nil {
		t.Fatal(err)
	}
	// A duplicate finish of the same run is fenced.
	if err = store.FinishRun(ctx, scope.Organization, created.ID, run[0].Run, nil); err != nil {
		t.Fatal(err)
	}
	after, err := store.LoadRun(ctx, scope.Organization, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RunSequence != run[0].Run+1 || string(after.Checkpoint) != `{"step": 1}` || after.Health.State != connectors.HealthAccessError || after.Health.LastError == nil || after.Health.LastError.Code != "unauthorized" {
		t.Fatalf("after finish %+v %s", after.Health, after.Checkpoint)
	}
	if len(claim()) != 0 {
		t.Fatal("next run must wait for its interval")
	}
	// A later transient failure keeps the unresolved access error.
	if _, err := pool.Exec(ctx, "UPDATE connector_instances SET next_run_at=now()-interval '1 second' WHERE organization=$1 AND id=$2", scope.Organization, created.ID); err != nil {
		t.Fatal(err)
	}
	next := claim()
	if len(next) != 1 || next[0].Run != run[0].Run+1 {
		t.Fatalf("next run %+v", next)
	}
	if err = store.FinishRun(ctx, scope.Organization, created.ID, next[0].Run, &connectors.RunError{Class: connectors.ClassTransient, Code: "source_unavailable", RetryAfter: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	// RetryAfter longer than the interval defers the next run past the source's reset.
	var deferredFor float64
	if err = pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM next_run_at-now()) FROM connector_instances WHERE organization=$1 AND id=$2", scope.Organization, created.ID).Scan(&deferredFor); err != nil || deferredFor < 590 {
		t.Fatalf("next run not deferred: %v %v", err, deferredFor)
	}
	if still, _ := store.ReadConnector(ctx, scope.Organization, created.ID); still.Health.State != connectors.HealthAccessError || still.Health.LastError.Code != "source_unavailable" {
		t.Fatalf("transient failure cleared the access error: %+v", still.Health)
	}
	// A skipped run (source not due) polled nothing: no success, no error, no health change.
	if err = store.FinishRun(ctx, scope.Organization, created.ID, next[0].Run+1, &connectors.RunError{Skipped: true}); err != nil {
		t.Fatal(err)
	}
	skipped, err := store.LoadRun(ctx, scope.Organization, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.RunSequence != next[0].Run+2 || skipped.Health.LastSuccessAt != nil || skipped.Health.State != connectors.HealthAccessError || skipped.Health.LastError.Code != "source_unavailable" {
		t.Fatalf("skipped run changed health: %+v", skipped.Health)
	}
	disabled, err := service.Disable(ctx, scope, created.ID)
	if err != nil || disabled.Enabled || disabled.Health.State != connectors.HealthDisabled {
		t.Fatalf("disable %v %+v", err, disabled)
	}
	page, err := feed.Read(ctx, scope, c.ID, start, 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range page.Items {
		kinds = append(kinds, e.Type)
	}
	want := []string{"connector.health_changed", "connector.disabled", "connector.health_changed"}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("events %v want %v", kinds, want)
	}
	// Disabling released the Source Namespace for a replacement instance.
	replacement, err := service.Create(ctx, scope, second)
	if err != nil {
		t.Fatalf("replacement: %v", err)
	}
	// Leave nothing scheduled behind for the rest of the verification run.
	defer func() {
		if _, err := service.Disable(context.Background(), scope, replacement.ID); err != nil {
			t.Error(err)
		}
	}()
	if _, err = service.ReplaceCredential(ctx, scope, created.ID, connectors.CredentialInput{Key: "r1", Secret: json.RawMessage(`{"token":"fixture-test-token"}`)}); !errors.Is(err, connectors.ErrDisabled) {
		t.Fatalf("replace on disabled: %v", err)
	}
}

// TestReceiptProbeRecognisesOnlyTheAcceptedIdempotencyKey backs the connector
// pre-download skip: a key is known only once its command was accepted.
func TestReceiptProbeRecognisesOnlyTheAcceptedIdempotencyKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-probe-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "content:write"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Probe"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	key := connectors.KeyPrefix + "probe-r1"
	if known, err := store.HasReceipt(ctx, scope.Organization, key); err != nil || known {
		t.Fatalf("before acceptance: %v %v", known, err)
	}
	command := content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "mail", RecordKey: "<m@example.org>"}, Revision: "r1", Content: content.Text{Kind: "text", Text: "Bonjour"}}
	if _, err = (content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Catalog: store, BlobSource: store}).Accept(ctx, scope, command); err != nil {
		t.Fatal(err)
	}
	if known, err := store.HasReceipt(ctx, scope.Organization, key); err != nil || !known {
		t.Fatalf("after acceptance: %v %v", known, err)
	}
	if known, _ := store.HasReceipt(ctx, scope.Organization, connectors.KeyPrefix+"probe-r2"); known {
		t.Fatal("another revision's key must not be known")
	}
	if known, _ := store.HasReceipt(ctx, "another-org", key); known {
		t.Fatal("receipts are Organization-scoped")
	}
}

// TestScheduleChangesCommitOnlyActualChangesAndPullShorterRunsIn drives the
// schedule command against the real store.
func TestScheduleChangesCommitOnlyActualChangesAndPullShorterRunsIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-schedule-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:read", "changes:read"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Schedules"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{Pool: pool}
	registry, _ := connectors.NewRegistry(fakeplugin.FixtureConnector{})
	sealer, _ := connectors.NewSealer("adapter-test-credential-key-0123456789")
	service := connectors.Service{Store: store, Registry: registry, Sealer: sealer, MinInterval: time.Second}
	hour := 3600
	created, err := service.Create(ctx, scope, connectors.CreateInput{Key: "k", CorpusID: c.ID, Namespace: "wire", Kind: "fixture", Config: json.RawMessage(`{"script":[]}`), IntervalSeconds: &hour})
	if err != nil {
		t.Fatal(err)
	}
	// Push the pending run an hour out, as after a completed run.
	if _, err = pool.Exec(ctx, "UPDATE connector_instances SET next_run_at=now()+interval '1 hour' WHERE organization=$1 AND id=$2", scope.Organization, created.ID); err != nil {
		t.Fatal(err)
	}
	feed := changes.Service{Journal: postgres.ChangeStore{Pool: store.Pool}, Key: []byte("adapter-cursor-key-0123456789abcdef")}
	start, err := feed.Start(ctx, scope, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	nextRunIn := func() float64 {
		var seconds float64
		if err := pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM next_run_at-now()) FROM connector_instances WHERE organization=$1 AND id=$2", scope.Organization, created.ID).Scan(&seconds); err != nil {
			t.Fatal(err)
		}
		return seconds
	}
	shorter, err := service.ChangeSchedule(ctx, scope, created.ID, 60)
	if err != nil || shorter.Interval != time.Minute {
		t.Fatalf("shorten %v %+v", err, shorter)
	}
	if in := nextRunIn(); in > 61 {
		t.Fatalf("shorter interval did not pull the next run in: %v s", in)
	}
	// Repeating the same value commits nothing.
	if _, err = service.ChangeSchedule(ctx, scope, created.ID, 60); err != nil {
		t.Fatal(err)
	}
	longer, err := service.ChangeSchedule(ctx, scope, created.ID, 7200)
	if err != nil || longer.Interval != 2*time.Hour {
		t.Fatalf("lengthen %v %+v", err, longer)
	}
	if in := nextRunIn(); in > 61 {
		t.Fatalf("longer interval moved the pending run: %v s", in)
	}
	if read, _ := store.ReadConnector(ctx, scope.Organization, created.ID); read.Interval != 2*time.Hour {
		t.Fatalf("persisted %v", read.Interval)
	}
	page, err := feed.Read(ctx, scope, c.ID, start, 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range page.Items {
		kinds = append(kinds, e.Type)
	}
	if want := []string{"connector.schedule_changed", "connector.schedule_changed"}; fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("events %v want %v", kinds, want)
	}
	if _, err = service.Disable(ctx, scope, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ChangeSchedule(ctx, scope, created.ID, 120); !errors.Is(err, connectors.ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}

// TestRunRequestsPullTheNextRunInWithinTheFloorAndRetryAfter drives the
// check-now command against the real store: it pulls the next run in, never
// before the interval floor after the last run nor before the source's
// Retry-After, repeats harmlessly and is refused once disabled.
func TestRunRequestsPullTheNextRunInWithinTheFloorAndRetryAfter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-run-request-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:read"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Run requests"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{Pool: pool}
	registry, _ := connectors.NewRegistry(fakeplugin.FixtureConnector{})
	sealer, _ := connectors.NewSealer("adapter-test-credential-key-0123456789")
	service := connectors.Service{Store: store, Registry: registry, Sealer: sealer, MinInterval: time.Minute}
	hour := 3600
	created, err := service.Create(ctx, scope, connectors.CreateInput{Key: "k", CorpusID: c.ID, Namespace: "wire", Kind: "fixture", Config: json.RawMessage(`{"script":[]}`), IntervalSeconds: &hour})
	if err != nil {
		t.Fatal(err)
	}
	nextRunIn := func() float64 {
		var seconds float64
		if err := pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM next_run_at-now()) FROM connector_instances WHERE organization=$1 AND id=$2", scope.Organization, created.ID).Scan(&seconds); err != nil {
			t.Fatal(err)
		}
		return seconds
	}
	request := func(want float64) {
		t.Helper()
		if _, err := service.RequestRun(ctx, scope, created.ID, "now"); err != nil {
			t.Fatal(err)
		}
		if in := nextRunIn(); in < want-5 || in > want+5 {
			t.Fatalf("next run in %v s, want about %v s", in, want)
		}
	}
	finish := func(failure *connectors.RunError) {
		t.Helper()
		target, err := store.LoadRun(ctx, scope.Organization, created.ID)
		if err == nil {
			err = store.FinishRun(ctx, scope.Organization, created.ID, target.RunSequence, failure)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// Never run yet, scheduled an hour out: due now, and repeating changes nothing.
	if _, err = pool.Exec(ctx, "UPDATE connector_instances SET next_run_at=now()+interval '1 hour' WHERE organization=$1 AND id=$2", scope.Organization, created.ID); err != nil {
		t.Fatal(err)
	}
	request(0)
	request(0)
	// Just after a run: not before the one-minute floor.
	finish(nil)
	request(60)
	// The source asked to wait ten minutes: the request honours it.
	finish(&connectors.RunError{Class: connectors.ClassTransient, Code: "rate_limited", RetryAfter: 10 * time.Minute})
	request(600)
	if _, err = service.Disable(ctx, scope, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RequestRun(ctx, scope, created.ID, "now"); !errors.Is(err, connectors.ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}
