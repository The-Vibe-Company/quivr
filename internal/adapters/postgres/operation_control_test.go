package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type controlFixture struct {
	t     *testing.T
	ctx   context.Context
	pool  *pgxpool.Pool
	org   string
	store fixtureContentStores
}

func newControlFixture(t *testing.T, name string) (controlFixture, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	pool := rebuildAdapterPool(t, ctx)
	return controlFixture{t: t, ctx: ctx, pool: pool, org: fmt.Sprintf("adapter-%s-%d", name, time.Now().UnixNano()), store: contentStores(pool)}, cancel
}

func (f controlFixture) corpus(key string) string {
	f.t.Helper()
	scope := corpus.Scope{Organization: f.org, Actions: []string{"corpora:write"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: f.pool}}.Create(f.ctx, scope, corpus.CreateInput{Key: key, Name: key})
	if err != nil {
		f.t.Fatal(err)
	}
	return c.ID
}

func (f controlFixture) rebuild(corpusID, key string) operations.Operation {
	f.t.Helper()
	op, err := f.store.AcceptRebuild(f.ctx, f.org, corpusID, key, []byte(`{"idempotency_key":"`+key+`"}`))
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func (f controlFixture) state(id string) string {
	f.t.Helper()
	op, err := f.store.Operation(f.ctx, f.org, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return op.State
}

func (f controlFixture) routed(corpusID string) string {
	f.t.Helper()
	g, err := f.store.Generation(f.ctx, f.org, corpusID)
	if err != nil {
		f.t.Fatal(err)
	}
	return g.ID
}

// transitions lists the journaled Operation states in commit order.
func (f controlFixture) transitions(id string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='operation.updated' AND resource_id=$2`, f.org, id).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// Cancellation stops remaining work without undoing committed effects, never
// lets a partial target activate, and leaves terminal outcomes untouched. An
// empty Corpus has no coverage gap, so activation would succeed if allowed.
func TestCancelQueuedRunningAndTerminalOperations(t *testing.T) {
	f, cancel := newControlFixture(t, "cancel")
	defer cancel()
	a := f.corpus("a")
	prior := f.routed(a)

	// Queued: no step has begun, so cancellation is immediately terminal.
	queued := f.rebuild(a, "queued")
	got, err := f.store.CancelOperation(f.ctx, f.org, queued.ID)
	if err != nil || got.State != operations.StateCanceled || got.ID != queued.ID {
		t.Fatalf("cancel queued %+v %v", got, err)
	}
	if again, err := f.store.CancelOperation(f.ctx, f.org, queued.ID); err != nil || again.State != operations.StateCanceled {
		t.Fatalf("repeated cancel %+v %v", again, err)
	}
	begun, err := f.store.BeginRebuild(f.ctx, f.org, queued.ID)
	if err != nil || begun.Operation.State != operations.StateCanceled {
		t.Fatalf("dispatched canceled Operation began: %+v %v", begun.Operation, err)
	}
	if _, err = f.store.ActivateRebuild(f.ctx, f.org, queued.ID); !errors.Is(err, operations.ErrNotRunning) {
		t.Fatalf("canceled Operation activated: %v", err)
	}
	if n := f.transitions(queued.ID); n != 2 {
		t.Fatalf("queued cancel transitions %d, want queued/canceled", n)
	}

	// Running: cancellation is requested; activation refuses; the worker settles it.
	running := f.rebuild(a, "running")
	if _, err = f.store.BeginRebuild(f.ctx, f.org, running.ID); err != nil {
		t.Fatal(err)
	}
	if got, err = f.store.CancelOperation(f.ctx, f.org, running.ID); err != nil || got.State != operations.StateCancelRequested {
		t.Fatalf("cancel running %+v %v", got, err)
	}
	if got, err = f.store.CancelOperation(f.ctx, f.org, running.ID); err != nil || got.State != operations.StateCancelRequested {
		t.Fatalf("repeated cancel running %+v %v", got, err)
	}
	if _, err = f.store.ActivateRebuild(f.ctx, f.org, running.ID); !errors.Is(err, operations.ErrNotRunning) {
		t.Fatalf("cancel-requested Operation activated: %v", err)
	}
	if err = f.store.FailRebuild(f.ctx, f.org, running.ID, operations.Error{Code: "late", Message: "late"}); err != nil {
		t.Fatal(err)
	}
	if s := f.state(running.ID); s != operations.StateCancelRequested {
		t.Fatalf("failure overrode cancellation request: %s", s)
	}
	for i := 0; i < 2; i++ {
		if err = f.store.ConfirmCancel(f.ctx, f.org, running.ID); err != nil {
			t.Fatal(err)
		}
	}
	if s := f.state(running.ID); s != operations.StateCanceled {
		t.Fatalf("confirmed state %s", s)
	}
	if n := f.transitions(running.ID); n != 4 {
		t.Fatalf("running cancel transitions %d, want queued/running/cancel_requested/canceled", n)
	}
	if g := f.routed(a); g != prior {
		t.Fatalf("canceled rebuild rerouted Corpus to %s", g)
	}

	// Terminal: cancel returns the existing outcome and records nothing.
	done := f.rebuild(a, "done")
	if _, err = f.store.BeginRebuild(f.ctx, f.org, done.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.ActivateRebuild(f.ctx, f.org, done.ID); err != nil || !ok {
		t.Fatalf("activate %v %v", ok, err)
	}
	if got, err = f.store.CancelOperation(f.ctx, f.org, done.ID); err != nil || got.State != operations.StateSucceeded || got.ResultGenerationID != done.TargetGenerationID {
		t.Fatalf("terminal cancel %+v %v", got, err)
	}
	if err = f.store.ConfirmCancel(f.ctx, f.org, done.ID); err != nil || f.state(done.ID) != operations.StateSucceeded {
		t.Fatalf("confirm on succeeded changed it: %v", err)
	}
	if n := f.transitions(done.ID); n != 3 {
		t.Fatalf("terminal cancel transitions %d, want queued/running/succeeded", n)
	}
	if g := f.routed(a); g != done.TargetGenerationID {
		t.Fatalf("completed rebuild not routed: %s", g)
	}
	for _, org := range []string{f.org, "another-org"} {
		if _, err = f.store.CancelOperation(f.ctx, org, map[bool]string{true: "operation_absent", false: done.ID}[org == f.org]); !errors.Is(err, corpus.ErrNotFound) {
			t.Fatalf("cancel absent/foreign in %s: %v", org, err)
		}
	}
}

// Completion and cancellation serialize on the Operation row: whichever
// commits first wins, and a canceled target is never activated.
func TestCompletionRacingCancellationHasOneWinner(t *testing.T) {
	f, cancel := newControlFixture(t, "race")
	defer cancel()
	outcomes := map[string]int{}
	for i := 0; i < 12; i++ {
		c := f.corpus(fmt.Sprint("race-", i))
		prior := f.routed(c)
		op := f.rebuild(c, "race")
		if _, err := f.store.BeginRebuild(f.ctx, f.org, op.ID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var activated bool
		var activateErr, cancelErr error
		var canceled operations.Operation
		wg.Add(2)
		go func() { defer wg.Done(); activated, activateErr = f.store.ActivateRebuild(f.ctx, f.org, op.ID) }()
		go func() { defer wg.Done(); canceled, cancelErr = f.store.CancelOperation(f.ctx, f.org, op.ID) }()
		wg.Wait()
		if cancelErr != nil {
			t.Fatal(cancelErr)
		}
		switch final := f.state(op.ID); {
		case final == operations.StateSucceeded:
			if activateErr != nil || !activated || canceled.State != operations.StateSucceeded || f.routed(c) != op.TargetGenerationID {
				t.Fatalf("completion won inconsistently: %v %v %+v route %s", activated, activateErr, canceled, f.routed(c))
			}
		case final == operations.StateCancelRequested:
			if !errors.Is(activateErr, operations.ErrNotRunning) || canceled.State != operations.StateCancelRequested || f.routed(c) != prior {
				t.Fatalf("cancellation won inconsistently: %v %v %+v route %s", activated, activateErr, canceled, f.routed(c))
			}
		default:
			t.Fatalf("race left state %s", final)
		}
		outcomes[f.state(op.ID)]++
	}
	t.Logf("race outcomes %v", outcomes)
}

// Rerun requires a terminal source, creates a new linked Operation with its own
// target and dispatch, replays per source + key, and never collides with the
// rebuild route's own idempotency keys.
func TestRerunTerminalOperationCreatesLinkedOperation(t *testing.T) {
	f, cancel := newControlFixture(t, "rerun")
	defer cancel()
	a := f.corpus("a")
	source := f.rebuild(a, "k")
	canonical := func(src, key string) []byte {
		return []byte(`{"idempotency_key":"` + key + `","source_operation_id":"` + src + `"}`)
	}
	rerun := func(src, key string) (operations.Operation, error) {
		return f.store.AcceptRerun(f.ctx, f.org, src, key, canonical(src, key))
	}
	if _, err := rerun(source.ID, "k"); !errors.Is(err, operations.ErrNotTerminal) {
		t.Fatalf("queued rerun: %v", err)
	}
	if _, err := f.store.BeginRebuild(f.ctx, f.org, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rerun(source.ID, "k"); !errors.Is(err, operations.ErrNotTerminal) {
		t.Fatalf("running rerun: %v", err)
	}
	if _, err := f.store.CancelOperation(f.ctx, f.org, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rerun(source.ID, "k"); !errors.Is(err, operations.ErrNotTerminal) {
		t.Fatalf("cancel_requested rerun: %v", err)
	}
	if err := f.store.ConfirmCancel(f.ctx, f.org, source.ID); err != nil {
		t.Fatal(err)
	}
	next, err := rerun(source.ID, "k")
	if err != nil || next.ID == source.ID || next.PreviousID != source.ID || next.Kind != source.Kind || next.CorpusID != a || next.State != operations.StateQueued || next.TargetGenerationID == "" || next.TargetGenerationID == source.TargetGenerationID {
		t.Fatalf("rerun %+v %v", next, err)
	}
	if replay, err := rerun(source.ID, "k"); err != nil || replay.ID != next.ID || replay.TargetGenerationID != next.TargetGenerationID {
		t.Fatalf("rerun replay %+v %v", replay, err)
	}
	if _, err = f.store.AcceptRerun(f.ctx, f.org, source.ID, "k", []byte(`{"changed":true}`)); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("changed rerun request: %v", err)
	}
	if other, err := rerun(source.ID, "k2"); err != nil || other.ID == next.ID || other.PreviousID != source.ID {
		t.Fatalf("second rerun key %+v %v", other, err)
	}
	// The rebuild route's key "k" still names the original Operation only.
	if again := f.rebuild(a, "k"); again.ID != source.ID {
		t.Fatalf("rebuild replay returned %s, want source %s", again.ID, source.ID)
	}
	if _, err = rerun("operation_absent", "k"); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("absent source: %v", err)
	}
	if _, err = f.store.AcceptRerun(f.ctx, "another-org", source.ID, "k", canonical(source.ID, "k")); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("foreign source: %v", err)
	}
	if n := f.transitions(next.ID); n != 1 {
		t.Fatalf("rerun journal %d, want queued", n)
	}
	claimed := false
	for {
		batch, err := f.store.ClaimOperations(f.ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		for _, d := range batch {
			claimed = claimed || (d.Organization == f.org && d.OperationID == next.ID)
			if err = f.store.OperationDispatched(f.ctx, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !claimed {
		t.Fatal("rerun dispatch intent missing")
	}
	// The rerun progresses under its own identity and target; reruns chain.
	if _, err = f.store.BeginRebuild(f.ctx, f.org, next.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.ActivateRebuild(f.ctx, f.org, next.ID); err != nil || !ok {
		t.Fatalf("rerun activation %v %v", ok, err)
	}
	if read, _ := f.store.Operation(f.ctx, f.org, next.ID); read.State != operations.StateSucceeded || read.ResultGenerationID != next.TargetGenerationID || f.routed(a) != next.TargetGenerationID {
		t.Fatalf("rerun outcome %+v route %s", read, f.routed(a))
	}
	chained, err := rerun(next.ID, "k")
	if err != nil || chained.PreviousID != next.ID || chained.ID == next.ID {
		t.Fatalf("chained rerun %+v %v", chained, err)
	}
}
