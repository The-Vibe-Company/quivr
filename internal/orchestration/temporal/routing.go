package temporal

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/routing"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const routingWorkflowName = "routing-change-v1"

func routingWorkflow(ctx workflow.Context, in RebuildInput) error {
	ctx = backgroundContext(ctx, routingWorkflowName)
	return operationWorkflow(ctx, workflowStep(func(ctx workflow.Context) (StepProgress, error) {
		var progress routing.Progress
		err := workflow.ExecuteActivity(ctx, "routing-step", in).Get(ctx, &progress)
		return StepProgress{Done: progress.Done, Wait: progress.Wait}, err
	}), routingWorkflowName, in)
}

func registerRouting(w worker.Worker, service routing.Service) {
	w.RegisterWorkflowWithOptions(routingWorkflow, workflow.RegisterOptions{Name: routingWorkflowName})
	w.RegisterActivityWithOptions(func(ctx context.Context, in RebuildInput) (routing.Progress, error) {
		var progress routing.Progress
		err := heartbeating(ctx, stepHeartbeatTimeout/3, func() error {
			var err error
			progress, err = service.Step(ctx, in.Organization, in.OperationID)
			return err
		})
		return progress, err
	}, activity.RegisterOptions{Name: "routing-step"})
}
