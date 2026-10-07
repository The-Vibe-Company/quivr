package autoscaling_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
)

type backlog struct {
	waiting int64
	err     error
}

func (b *backlog) Waiting(context.Context) (int64, error) { return b.waiting, b.err }

type replicas struct {
	current           int
	readErr, writeErr error
	writes            []int
}

func (r *replicas) Replicas(context.Context) (int, error) { return r.current, r.readErr }
func (r *replicas) SetReplicas(_ context.Context, count int) error {
	r.writes = append(r.writes, count)
	if r.writeErr != nil {
		return r.writeErr
	}
	r.current = count
	return nil
}

// The controller is the owner of failure holds and recovery. Fakes replace
// external APIs only; the real policy runs throughout this sequence.
func TestControllerHoldsCapacityOnErrors(t *testing.T) {
	b := &backlog{waiting: 40001}
	r := &replicas{current: 1}
	p, err := autoscaling.NewPolicy(autoscaling.Config{Min: 1, Max: 8, DocumentsPerReplica: 20000, DownscaleWindow: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	c := autoscaling.NewController(p, b, r)
	start := time.Unix(1000, 0)
	step := func(seconds int, want int, wantErr bool) {
		t.Helper()
		d, err := c.Step(context.Background(), start.Add(time.Duration(seconds)*time.Second))
		if b.err != nil && d.Waiting != -1 {
			t.Fatalf("queue error must log unknown backlog -1, got decision %+v", d)
		}
		wantApplied := want
		if r.readErr != nil {
			wantApplied = -1
		}
		if (err != nil) != wantErr || r.current != want || d.Applied != wantApplied {
			t.Fatalf("second %d: decision %+v error %v; want count %d, error %v", seconds, d, err, want, wantErr)
		}
	}
	step(0, 3, false)
	step(1, 3, false)
	if len(r.writes) != 1 {
		t.Fatalf("unchanged count caused writes: %v", r.writes)
	}
	b.waiting = 0
	step(30, 3, false)
	b.err = errors.New("queue unavailable")
	step(70, 3, true)
	b.err = nil
	step(71, 3, false)
	step(130, 3, false)
	r.writeErr = errors.New("write rejected")
	step(131, 3, true)
	if r.writes[len(r.writes)-1] != 1 {
		t.Fatalf("expected attempted drain to one: %v", r.writes)
	}
	r.writeErr = nil
	step(132, 3, false)
	step(192, 1, false)
	r.readErr = errors.New("provider unavailable")
	b.waiting = 200000
	before := len(r.writes)
	step(200, 1, true)
	if len(r.writes) != before {
		t.Fatalf("provider read error caused write: %v", r.writes)
	}
	r.readErr = nil
	step(201, 8, false)
	// An operator change starts a new observation window.
	r.current = 4
	b.waiting = 0
	step(202, 4, false)
	step(261, 4, false)
	step(262, 1, false)
}

// Scaling restarts workers, so decisions keep observing backlog while actions
// are spaced by a supplied clock. Failures must not consume an action window.
func TestControllerSpacesScalingActions(t *testing.T) {
	b := &backlog{waiting: 40001}
	r := &replicas{current: 1}
	p, err := autoscaling.NewPolicy(autoscaling.Config{Min: 1, Max: 8, DocumentsPerReplica: 20000, DownscaleWindow: 0})
	if err != nil {
		t.Fatal(err)
	}
	c := autoscaling.NewController(p, b, r)
	c.MinScaleInterval = 2 * time.Minute
	start := time.Unix(1000, 0)
	step := func(seconds, target, applied, writes int, outcome string, wantErr bool) {
		t.Helper()
		d, err := c.Step(context.Background(), start.Add(time.Duration(seconds)*time.Second))
		if (err != nil) != wantErr || d.Target != target || d.Applied != applied || d.Outcome != outcome || len(r.writes) != writes {
			t.Fatalf("second %d: decision %+v/error %v/writes %v; want target %d applied %d outcome %s writes %d error %v", seconds, d, err, r.writes, target, applied, outcome, writes, wantErr)
		}
	}
	step(0, 3, 3, 1, "scaled", false)
	b.waiting = 80001
	step(30, 5, 3, 1, "cooldown", false)
	step(119, 5, 3, 1, "cooldown", false)
	r.writeErr = errors.New("deploy rejected")
	step(120, 5, 3, 2, "update_error", true)
	r.writeErr = nil
	step(121, 5, 5, 3, "scaled", false)
	b.waiting = 0
	step(240, 1, 5, 3, "cooldown", false)
	step(241, 1, 1, 4, "scaled", false)
	// An operator can disable spacing without changing the policy.
	c.MinScaleInterval = 0
	b.waiting = 200000
	step(242, 8, 8, 5, "scaled", false)
}
