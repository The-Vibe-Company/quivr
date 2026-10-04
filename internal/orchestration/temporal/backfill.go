package temporal

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/backfill"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const backfillWorkflowName = "backfill-v1"

// backfillTaskQueue is served by its own worker, one activity at a time, so
// a backfill never takes a slot from live content processing.
const backfillTaskQueue = "quivr-backfill-v0"

func backfillWorkflow(ctx workflow.Context, in RebuildInput) error {
	ctx = backgroundContext(ctx, backfillWorkflowName)
	return operationWorkflow(ctx, workflowStep(func(ctx workflow.Context) (StepProgress, error) {
		var progress backfill.Progress
		err := workflow.ExecuteActivity(ctx, "backfill-step", in).Get(ctx, &progress)
		return StepProgress{Done: progress.Done, Wait: progress.Wait}, err
	}), backfillWorkflowName, in)
}

func registerBackfill(w worker.Worker, backfiller backfill.Backfiller, pins Pinner) {
	w.RegisterWorkflowWithOptions(backfillWorkflow, workflow.RegisterOptions{Name: backfillWorkflowName})
	registerOperationStep(w, "backfill-step", pins, func(ctx context.Context, in RebuildInput) (backfill.Progress, error) {
		return backfiller.Step(ctx, in.Organization, in.OperationID)
	}, func(p backfill.Progress) bool { return p.Done })
}
