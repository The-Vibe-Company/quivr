package temporal

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/quarantine"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const reprocessWorkflowName = "quarantine-reprocess-v1"

func reprocessWorkflow(ctx workflow.Context, in RebuildInput) error {
	ctx = backgroundContext(ctx, reprocessWorkflowName)
	return operationWorkflow(ctx, workflowStep(func(ctx workflow.Context) (StepProgress, error) {
		var progress quarantine.Progress
		err := workflow.ExecuteActivity(ctx, "quarantine-reprocess-step", in).Get(ctx, &progress)
		return StepProgress{Done: progress.Done, Wait: progress.Wait}, err
	}), reprocessWorkflowName, in)
}

func registerReprocess(w worker.Worker, reprocessor quarantine.Reprocessor, pins Pinner) {
	w.RegisterWorkflowWithOptions(reprocessWorkflow, workflow.RegisterOptions{Name: reprocessWorkflowName})
	registerOperationStep(w, "quarantine-reprocess-step", pins, func(ctx context.Context, in RebuildInput) (quarantine.Progress, error) {
		return reprocessor.Step(ctx, in.Organization, in.OperationID)
	}, func(p quarantine.Progress) bool { return p.Done })
}
