package temporal

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const rebuildWorkflowName = "projection-rebuild-v1"

// RebuildInput carries only stable identifiers; Operation state lives in PostgreSQL.
type RebuildInput struct {
	Organization string
	OperationID  string
}

func rebuildWorkflow(ctx workflow.Context, in RebuildInput) error {
	ctx = backgroundContext(ctx, rebuildWorkflowName)
	return operationWorkflow(ctx, workflowStep(func(ctx workflow.Context) (StepProgress, error) {
		var done bool
		err := workflow.ExecuteActivity(ctx, "rebuild-projection-step", in).Get(ctx, &done)
		return StepProgress{Done: done, Wait: 200 * time.Millisecond}, err
	}), rebuildWorkflowName, in)
}

func registerRebuild(w worker.Worker, rebuilder retrieval.Rebuilder, pins Pinner) {
	w.RegisterWorkflowWithOptions(rebuildWorkflow, workflow.RegisterOptions{Name: rebuildWorkflowName})
	registerOperationStep(w, "rebuild-projection-step", pins, func(ctx context.Context, in RebuildInput) (bool, error) {
		return rebuilder.Step(ctx, in.Organization, in.OperationID)
	}, func(done bool) bool { return done })
}
