package temporal

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func servingProjectionWorkflow(ctx workflow.Context, in Input) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: enrichmentActivityTimeout, HeartbeatTimeout: stepHeartbeatTimeout, RetryPolicy: &sdktemporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second}})
	return workflow.ExecuteActivity(ctx, "publish-serving-projection", in).Get(ctx, nil)
}

// Serving publication uses ordinary served capacity. Optional evaluations use
// their separate queue and can never wait ahead of this activity.
func registerServingProjection(w worker.Worker, e processing.Evaluator, pins Pinner) {
	w.RegisterWorkflowWithOptions(servingProjectionWorkflow, workflow.RegisterOptions{Name: "publish-serving-projection-v0"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		pinned, err := pins.Pin(ctx, "serving_projection", in.Organization, in.ReceiptID)
		if err != nil {
			return err
		}
		if err = heartbeating(ctx, stepHeartbeatTimeout/3, func() error { return e.RunServing(pinned, in.Organization, in.ReceiptID) }); err != nil {
			return err
		}
		return pins.Release(ctx, "serving_projection", in.Organization, in.ReceiptID)
	}, activity.RegisterOptions{Name: "publish-serving-projection"})
}

func (r *Runtime) servingProjectionIntents(classes ...string) IntentSource {
	return intentSource(func(ctx context.Context) ([]Intent, error) {
		if len(classes) > 0 {
			ctx = workqueue.WithClass(ctx, classes[0])
		}
		jobs, err := r.Evaluation.Serving.ClaimServingProjections(ctx, 32)
		if err != nil {
			return nil, err
		}
		intents := make([]Intent, 0, len(jobs))
		for _, j := range jobs {
			queue := taskQueue
			if workqueue.Valid(j.WorkQueue) {
				queue = workqueue.TaskQueue(j.WorkQueue)
			}
			intents = append(intents, dispatchIntent{
				options: client.StartWorkflowOptions{ID: content.StableID("serving-projection-workflow", j.Organization, j.ID), TaskQueue: queue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE},
				name:    "publish-serving-projection-v0", input: Input{Organization: j.Organization, ReceiptID: j.ID},
				complete: func(ctx context.Context) error { return r.Evaluation.Serving.ServingProjectionDispatched(ctx, j) },
				retry:    func(context.Context) error { return nil },
			})
		}
		return intents, nil
	})
}
