package postgres_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
)

func (f controlFixture) begin(id string) operations.Operation {
	f.t.Helper()
	target, err := f.store.BeginRebuild(f.ctx, f.org, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return target.Operation
}

func (f controlFixture) superseded(id string) {
	f.t.Helper()
	op, err := f.store.Operation(f.ctx, f.org, id)
	if err != nil || op.State != operations.StateFailed || len(op.Errors) != 1 || op.Errors[0].Code != operations.ErrOperationSuperseded.Error() || op.Errors[0].Message == "" || op.Errors[0].Retryable {
		f.t.Fatalf("Operation %s not superseded: %+v %v", id, op, err)
	}
}

// Two rebuilds of one Corpus built with the same configuration finish in
// reverse acceptance order: the earlier one can no longer activate over the
// later one, ends failed with operation_superseded and leaves the route alone.
func TestEarlierRebuildCannotActivateOverLaterOne(t *testing.T) {
	f, cancel := newControlFixture(t, "order-reverse")
	defer cancel()
	a := f.corpus("a")
	first, second := f.rebuild(a, "first"), f.rebuild(a, "second")
	f.begin(first.ID)
	f.begin(second.ID)
	if done, err := f.store.ActivateRebuild(f.ctx, f.org, second.ID); err != nil || !done {
		t.Fatalf("later rebuild activation %v %v", done, err)
	}
	if done, err := f.store.ActivateRebuild(f.ctx, f.org, first.ID); !errors.Is(err, operations.ErrNotRunning) || done {
		t.Fatalf("earlier rebuild activated over the later one: %v %v", done, err)
	}
	f.superseded(first.ID)
	if got := f.routed(a); got != second.TargetGenerationID {
		t.Fatalf("route %s, want later rebuild %s", got, second.TargetGenerationID)
	}
	// Recovery of the later activation stays idempotent.
	if done, err := f.store.ActivateRebuild(f.ctx, f.org, second.ID); err != nil || !done {
		t.Fatalf("recovery %v %v", done, err)
	}
	// A rerun is a new accept: it is later than everything and may activate.
	rerun, err := f.store.AcceptRerun(f.ctx, f.org, first.ID, "rerun", []byte(`{"idempotency_key":"rerun"}`))
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.activate(rerun.ID); err != nil || !done || f.routed(a) != rerun.TargetGenerationID {
		t.Fatalf("rerun activation %v %v, route %s", done, err, f.routed(a))
	}
}

// Finishing in acceptance order still activates both, and a newer retrieval
// configuration outranks a later-accepted rebuild pinned to the older one.
func TestActivationOrderKeepsInOrderFinishAndConfigurationPrecedence(t *testing.T) {
	f, cancel := newControlFixture(t, "order-forward")
	defer cancel()
	a, b := f.corpus("a"), f.corpus("b")
	first, second := f.rebuild(a, "first"), f.rebuild(a, "second")
	for _, op := range []operations.Operation{first, second} {
		if done, err := f.activate(op.ID); err != nil || !done {
			t.Fatalf("in-order activation of %s: %v %v", op.ID, done, err)
		}
	}
	if f.routed(a) != second.TargetGenerationID {
		t.Fatalf("route %s, want %s", f.routed(a), second.TargetGenerationID)
	}
	// Configuration accepted first, rebuild (pinned to the prior configuration)
	// accepted later and finishing first: the configuration still activates.
	cfg, err := f.configure(b, "cfg", titleConfig("/provenance/title"))
	if err != nil {
		t.Fatal(err)
	}
	plain := f.rebuild(b, "plain")
	if done, err := f.activate(plain.ID); err != nil || !done {
		t.Fatalf("plain rebuild %v %v", done, err)
	}
	if done, err := f.activate(cfg.ID); err != nil || !done || f.routed(b) != cfg.TargetGenerationID {
		t.Fatalf("configuration activation %v %v, route %s", done, err, f.routed(b))
	}
}

// A queued Operation already outranked by an activated later one fails at
// its first step instead of building a generation that can never serve.
func TestOutrankedRebuildFailsBeforeBuilding(t *testing.T) {
	f, cancel := newControlFixture(t, "order-early")
	defer cancel()
	a := f.corpus("a")
	first, second := f.rebuild(a, "first"), f.rebuild(a, "second")
	if done, err := f.activate(second.ID); err != nil || !done {
		t.Fatalf("later rebuild %v %v", done, err)
	}
	if op := f.begin(first.ID); op.State != operations.StateFailed {
		t.Fatalf("outranked rebuild began: %+v", op)
	}
	f.superseded(first.ID)
	var events int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='operation.updated' AND resource_id=$2`, f.org, first.ID).Scan(&events); err != nil || events != 2 {
		t.Fatalf("operation.updated transitions = %d %v, want queued/failed", events, err)
	}
}

// Concurrent activations of two ready rebuilds always leave the later one routed.
func TestConcurrentActivationsConvergeOnLaterAcceptance(t *testing.T) {
	f, cancel := newControlFixture(t, "order-race")
	defer cancel()
	a := f.corpus("a")
	for i := 0; i < 20; i++ {
		first, second := f.rebuild(a, fmt.Sprint("first-", i)), f.rebuild(a, fmt.Sprint("second-", i))
		f.begin(first.ID)
		f.begin(second.ID)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j, op := range []operations.Operation{second, first} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[j] = f.store.ActivateRebuild(f.ctx, f.org, op.ID)
			}()
		}
		wg.Wait()
		if errs[0] != nil || (errs[1] != nil && !errors.Is(errs[1], operations.ErrNotRunning)) {
			t.Fatalf("iteration %d: %v", i, errs)
		}
		if got := f.routed(a); got != second.TargetGenerationID {
			t.Fatalf("iteration %d: route %s, want later %s", i, got, second.TargetGenerationID)
		}
		if st := f.state(first.ID); st != operations.StateSucceeded && st != operations.StateFailed {
			t.Fatalf("iteration %d: earlier state %s", i, st)
		}
	}
}
