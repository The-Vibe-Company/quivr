package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// TestPushHealthComesFromPullReportsAndDeliveriesAndRelaxesPull drives the
// real store: lookup by id alone, the push report of a pull run, the relaxed
// schedule while push is healthy, delivery errors that bring pull back to the
// instance's interval, missed deliveries cleared only by a delivery with
// items, and access refusals shown as Connector Health.
func TestPushHealthComesFromPullReportsAndDeliveriesAndRelaxesPull(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-push-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:read"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Push"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{Pool: pool}
	registry, _ := connectors.NewRegistry(fakeplugin.FixtureConnector{})
	sealer, _ := connectors.NewSealer("adapter-test-credential-key-0123456789")
	service := connectors.Service{Store: store, Registry: registry, Sealer: sealer, MinInterval: time.Second}
	minute := 60
	created, err := service.Create(ctx, scope, connectors.CreateInput{Key: "k", CorpusID: c.ID, Namespace: "alerts", Kind: "fixture", Config: json.RawMessage(`{"script":[]}`), IntervalSeconds: &minute})
	if err != nil {
		t.Fatal(err)
	}
	org, id := scope.Organization, created.ID

	target, err := store.LoadDelivery(ctx, id)
	if err != nil || target.Organization != org || target.Health.Push != nil {
		t.Fatalf("lookup by id: %v %+v", err, target)
	}
	if _, err = store.LoadDelivery(ctx, id+"-unknown"); err != corpus.ErrNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	read := func() connectors.Instance {
		t.Helper()
		in, err := store.ReadConnector(ctx, org, id)
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	nextRunIn := func() float64 {
		t.Helper()
		var seconds float64
		if err := pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM next_run_at-now()) FROM connector_instances WHERE organization=$1 AND id=$2", org, id).Scan(&seconds); err != nil {
			t.Fatal(err)
		}
		return seconds
	}
	run := func(p connectors.Progress) {
		t.Helper()
		loaded, err := store.LoadRun(ctx, org, id)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := store.CommitCheckpoint(ctx, org, id, loaded.RunSequence, p); err != nil || !ok {
			t.Fatalf("commit %v %v", ok, err)
		}
		if err := store.FinishRun(ctx, org, id, loaded.RunSequence, nil); err != nil {
			t.Fatal(err)
		}
	}

	// A healthy push channel relaxes pull to the reported safety-net interval.
	run(connectors.Progress{Checkpoint: json.RawMessage(`{}`), Push: &connectors.PushStatus{State: connectors.PushActive, PollInterval: 15 * time.Minute}})
	if in := read(); in.Health.Push == nil || in.Health.Push.State() != connectors.PushActive || in.Health.Push.PollInterval != 15*time.Minute {
		t.Fatalf("push %+v", in.Health.Push)
	}
	if in := nextRunIn(); in < 800 {
		t.Fatalf("healthy push did not relax pull: next run in %v s", in)
	}

	// A delivery the core cannot complete degrades push and pulls the next run in.
	if err = store.RecordDelivery(ctx, org, id, connectors.DeliveryOutcome{Failure: &connectors.RunError{Class: connectors.ClassTransient, Code: "plugin_unavailable", At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if in := read(); in.Health.Push.State() != connectors.PushDegraded || in.Health.Push.Error().Code != "plugin_unavailable" || in.Health.State != connectors.HealthActive {
		t.Fatalf("health %+v push %+v", in.Health, in.Health.Push)
	}
	if in := nextRunIn(); in > 61 {
		t.Fatalf("degraded push left pull relaxed: next run in %v s", in)
	}
	// The next accepted delivery clears it and counts its reads.
	if err = store.RecordDelivery(ctx, org, id, connectors.DeliveryOutcome{Accepted: true, Reads: 3}); err != nil {
		t.Fatal(err)
	}
	if in := read(); in.Health.Push.State() != connectors.PushActive || in.Health.Push.LastDeliveryAt == nil || in.Health.Usage == nil || in.Health.Usage.ItemsRead != 3 {
		t.Fatalf("health %+v push %+v", in.Health, in.Health.Push)
	}

	// Missed deliveries: cleared only by a delivery that carries items.
	run(connectors.Progress{Checkpoint: json.RawMessage(`{}`), Items: true, Missed: true})
	if in := read(); in.Health.Push.Error() == nil || in.Health.Push.Error().Code != connectors.CodeMissedDeliveries {
		t.Fatalf("push %+v", in.Health.Push)
	}
	if in := nextRunIn(); in > 61 {
		t.Fatalf("missed deliveries left pull relaxed: next run in %v s", in)
	}
	if err = store.RecordDelivery(ctx, org, id, connectors.DeliveryOutcome{Accepted: true}); err != nil {
		t.Fatal(err)
	}
	if in := read(); in.Health.Push.State() != connectors.PushDegraded {
		t.Fatalf("a challenge cleared missed deliveries: %+v", in.Health.Push)
	}
	if err = store.RecordDelivery(ctx, org, id, connectors.DeliveryOutcome{Accepted: true, Carried: true, Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if in := read(); in.Health.Push.State() != connectors.PushActive || in.Health.LastItemAt == nil {
		t.Fatalf("push %+v", in.Health.Push)
	}

	// A push channel refused access is access_error while pull carries on,
	// until the kind reports it active again.
	run(connectors.Progress{Checkpoint: json.RawMessage(`{}`), Push: &connectors.PushStatus{State: connectors.PushFailed, Class: connectors.ClassAccess, Code: "webhook_invalid"}})
	if in := read(); in.Health.State != connectors.HealthAccessError || in.Health.Push.Error().Code != "webhook_invalid" || in.Health.LastSuccessAt == nil {
		t.Fatalf("health %+v push %+v", in.Health, in.Health.Push)
	}
	run(connectors.Progress{Checkpoint: json.RawMessage(`{}`), Push: &connectors.PushStatus{State: connectors.PushActive}})
	if in := read(); in.Health.State != connectors.HealthActive || in.Health.Push.State() != connectors.PushActive {
		t.Fatalf("recovery: health %+v push %+v", in.Health, in.Health.Push)
	}
}
