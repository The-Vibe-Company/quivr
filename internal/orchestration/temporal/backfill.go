package temporal

import (
	"context"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const backfillWorkflowName = "backfill-v1"

// backfillTaskQueue is served by its own worker, one activity at a time, so
// a backfill never takes a slot from live content processing.
const backfillTaskQueue = "quivr-backfill-v0"

// backfillRounds bounds one workflow run's history; a longer backfill, or
// one paused for long, continues as new under the same workflow identity.
const backfillRounds = 400

func backfillWorkflow(ctx workflow.Context, in RebuildInput) error {
	// A killed worker is detected by the heartbeat timeout and the step
	// resumes from the checkpoint. Attempts are unbounded so an outage never
	// invents a failed Operation.
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Minute, HeartbeatTimeout: 10 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 30 * time.Second}})
	for i := 0; i < backfillRounds; i++ {
		var progress backfill.Progress
		if err := workflow.ExecuteActivity(ctx, "backfill-step", in).Get(ctx, &progress); err != nil {
			return err
		}
		if progress.Done {
			return nil
		}
		// The step says how long to wait: the pace of the rate, or the poll
		// interval while paused.
		if progress.Wait > 0 {
			if err := workflow.Sleep(ctx, progress.Wait); err != nil {
				return err
			}
		}
	}
	return workflow.NewContinueAsNewError(ctx, backfillWorkflowName, in)
}

// registerBackfill serves backfill Operations on w, pinned like rebuilds:
// every step derives through the plan the first one pinned; the last one
// releases it.
func registerBackfill(w worker.Worker, backfiller backfill.Backfiller, pins Pinner) {
	w.RegisterWorkflowWithOptions(backfillWorkflow, workflow.RegisterOptions{Name: backfillWorkflowName})
	w.RegisterActivityWithOptions(func(ctx context.Context, in RebuildInput) (backfill.Progress, error) {
		var progress backfill.Progress
		err := heartbeating(ctx, 2*time.Second, func() error {
			pinned, err := pins.Pin(ctx, workOperation, in.Organization, in.OperationID)
			if err != nil {
				return err
			}
			progress, err = backfiller.Step(pinned, in.Organization, in.OperationID)
			return err
		})
		if err != nil {
			// Bounded diagnostics: identifiers and attempt only, never payloads.
			slog.Warn("backfill retrying", "operation_id", in.OperationID, "attempt", activity.GetInfo(ctx).Attempt)
			return progress, err
		}
		if progress.Done {
			err = pins.Release(ctx, workOperation, in.Organization, in.OperationID)
		}
		return progress, err
	}, activity.RegisterOptions{Name: "backfill-step"})
}
