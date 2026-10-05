package lifecycle_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
)

// The lifecycle owner separates admission from active work and shares one
// deadline. A regression to canceling the sole context loses admitted work.
func TestDrainFinishesAdmittedWorkAndCancelsItAtTheDeadline(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish", true: "deadline"}[expired], func(t *testing.T) {
			group := lifecycle.New()
			started, finish, stopped := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			group.Go(func(ctx context.Context) {
				work, admitted := lifecycle.Admit(ctx)
				if !admitted {
					t.Error("running task not admitted")
				}
				close(started)
				select {
				case <-finish:
					stopped <- work.Err()
				case <-work.Done():
					stopped <- work.Err()
				}
			})
			<-started
			group.BeginDrain()
			if _, admitted := lifecycle.Admit(lifecycle.WorkContext(group.Context())); admitted {
				t.Fatal("new operation admitted during drain")
			}
			var late atomic.Bool
			group.Go(func(context.Context) { late.Store(true) })
			if !group.Draining() || group.Context().Err() == nil {
				t.Fatal("drain must mark unready and stop admissions first")
			}
			if lifecycle.WorkContext(group.Context()).Err() != nil {
				t.Fatal("admitted work canceled before grace")
			}
			deadline := context.Background()
			if expired {
				var cancel context.CancelFunc
				deadline, cancel = context.WithDeadline(deadline, time.Unix(0, 0))
				defer cancel()
			} else {
				close(finish)
			}
			err := group.Wait(deadline)
			if expired && err != context.DeadlineExceeded {
				t.Fatalf("expired drain: %v", err)
			}
			if !expired && late.Load() {
				t.Fatal("new background loop started during drain")
			}
			if !expired && err != nil {
				t.Fatal(err)
			}
			workErr := <-stopped
			if expired && workErr != context.Canceled {
				t.Fatalf("expired work not canceled: %v", workErr)
			}
			if !expired && workErr != nil {
				t.Fatalf("completed work canceled: %v", workErr)
			}
		})
	}
}

// Durable outcome recording must survive an individual attempt's cancellation,
// while every cleanup remains bound by the process shutdown budget.
func TestCleanupSurvivesAttemptCancellationAndEndsAtProcessDeadline(t *testing.T) {
	group := lifecycle.New()
	defer group.Close()
	type valueKey struct{}
	request, cancelRequest := context.WithCancel(context.WithValue(context.Background(), valueKey{}, "request-value"))
	request = lifecycle.WithWorkContext(request, lifecycle.WorkContext(group.Context()))
	cleanup, cancelCleanup := lifecycle.CleanupContext(request, time.Hour)
	defer cancelCleanup()
	cancelRequest()
	group.BeginDrain()
	if cleanup.Err() != nil || cleanup.Value(valueKey{}) != "request-value" {
		t.Fatal("cleanup lost request values or stopped before the grace deadline")
	}
	// Nesting cleanup must retain the process marker instead of detaching again.
	nested, cancelNested := lifecycle.CleanupContext(cleanup, time.Hour)
	defer cancelNested()
	deadline, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	_ = group.Wait(deadline)
	if cleanup.Err() == nil || nested.Err() == nil {
		t.Fatal("durable cleanup escaped the process deadline")
	}
}
