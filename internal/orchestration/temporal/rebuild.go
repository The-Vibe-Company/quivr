package temporal

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/retrieval"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const rebuildWorkflowName = "projection-rebuild-v1"

// One rebuild activity per worker leaves live content activity slots available.
const rebuildTaskQueue = "quivr-rebuild-v0"

// RebuildInput carries only stable identifiers; Operation state lives in PostgreSQL.
type RebuildInput struct {
	Organization string
	OperationID  string
}

func rebuildWorkflow(ctx workflow.Context, in RebuildInput) error {
	ctx = backgroundContext(ctx, rebuildWorkflowName)
	// Existing histories retain their activity queue until continue-as-new.
	if workflow.GetVersion(ctx, "rebuild-activity-queue", workflow.DefaultVersion, 1) != workflow.DefaultVersion {
		opts := workflow.GetActivityOptions(ctx)
		opts.TaskQueue = rebuildTaskQueue
		ctx = workflow.WithActivityOptions(ctx, opts)
	}
	return operationWorkflow(ctx, workflowStep(func(ctx workflow.Context) (StepProgress, error) {
		var done bool
		err := workflow.ExecuteActivity(ctx, "rebuild-projection-step", in).Get(ctx, &done)
		return StepProgress{Done: done, Wait: 200 * time.Millisecond}, err
	}), rebuildWorkflowName, in)
}

func registerRebuild(w worker.Worker, rebuilder retrieval.Rebuilder, pins Pinner) {
	w.RegisterWorkflowWithOptions(rebuildWorkflow, workflow.RegisterOptions{Name: rebuildWorkflowName})
	registerRebuildActivity(w, rebuilder, pins)
}

func registerRebuildActivity(w worker.ActivityRegistry, rebuilder retrieval.Rebuilder, pins Pinner) {
	registerOperationStep(w, "rebuild-projection-step", pins, func(ctx context.Context, in RebuildInput) (bool, error) {
		return rebuilder.Step(ctx, in.Organization, in.OperationID)
	}, func(done bool) bool { return done })
}
