package temporal

import (
	"context"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const reprocessWorkflowName = "quarantine-reprocess-v1"

// reprocessRounds bounds one workflow run's history, like backfillRounds.
const reprocessRounds = 400

// reprocessWorkflow runs a quarantine reprocess on the background task queue
// it shares with backfills, below live content processing.
func reprocessWorkflow(ctx workflow.Context, in RebuildInput) error {
	// A step reprocesses a batch of Versions, each through its plugin calls
	// (up to the engine caps of normalization and enrichment); a killed
	// worker is detected by the heartbeat timeout and the started Version
	// resumes in its phase. Attempts are unbounded so an outage never
	// invents a failed Operation.
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Minute, HeartbeatTimeout: 10 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 30 * time.Second}})
	for i := 0; i < reprocessRounds; i++ {
		var progress quarantine.Progress
		if err := workflow.ExecuteActivity(ctx, "quarantine-reprocess-step", in).Get(ctx, &progress); err != nil {
			return err
		}
		if progress.Done {
			return nil
		}
		if progress.Wait > 0 {
			if err := workflow.Sleep(ctx, progress.Wait); err != nil {
				return err
			}
		}
	}
	return workflow.NewContinueAsNewError(ctx, reprocessWorkflowName, in)
}

// registerReprocess serves quarantine reprocess Operations on w: every step
// runs in the plan the Operation was pinned to at acceptance; the last one
// releases it.
func registerReprocess(w worker.Worker, reprocessor quarantine.Reprocessor, pins Pinner) {
	w.RegisterWorkflowWithOptions(reprocessWorkflow, workflow.RegisterOptions{Name: reprocessWorkflowName})
	w.RegisterActivityWithOptions(func(ctx context.Context, in RebuildInput) (quarantine.Progress, error) {
		var progress quarantine.Progress
		err := heartbeating(ctx, 2*time.Second, func() error {
			pinned, err := pins.Pin(ctx, workOperation, in.Organization, in.OperationID)
			if err != nil {
				return err
			}
			progress, err = reprocessor.Step(pinned, in.Organization, in.OperationID)
			return err
		})
		if err != nil {
			// Bounded diagnostics: identifiers and attempt only, never payloads.
			slog.Warn("quarantine reprocess retrying", "operation_id", in.OperationID, "attempt", activity.GetInfo(ctx).Attempt)
			return progress, err
		}
		if progress.Done {
			err = pins.Release(ctx, workOperation, in.Organization, in.OperationID)
		}
		return progress, err
	}, activity.RegisterOptions{Name: "quarantine-reprocess-step"})
}
