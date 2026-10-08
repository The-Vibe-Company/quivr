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

func ingestionEvaluationWorkflow(ctx workflow.Context, in Input) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: enrichmentActivityTimeout, HeartbeatTimeout: stepHeartbeatTimeout, RetryPolicy: &sdktemporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second}})
	return workflow.ExecuteActivity(ctx, "evaluate-ingestion-plugin", in).Get(ctx, nil)
}

func registerIngestionEvaluation(w worker.Worker, e processing.Evaluator, pins Pinner) {
	w.RegisterWorkflowWithOptions(ingestionEvaluationWorkflow, workflow.RegisterOptions{Name: "evaluate-ingestion-plugin-v0"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		pinned, err := pins.Pin(ctx, "evaluation", in.Organization, in.ReceiptID)
		if err != nil {
			return err
		}
		if err = heartbeating(ctx, stepHeartbeatTimeout/3, func() error { return e.Run(pinned, in.Organization, in.ReceiptID) }); err != nil {
			return err
		}
		return pins.Release(ctx, "evaluation", in.Organization, in.ReceiptID)
	}, activity.RegisterOptions{Name: "evaluate-ingestion-plugin"})
}

// The shared dispatcher polls this source in an independent lane. Optional
// start failures cannot occupy served receipt dispatch or activity capacity.
func (r *Runtime) ingestionEvaluationIntents(classes ...string) IntentSource {
	return intentSource(func(ctx context.Context) ([]Intent, error) {
		if len(classes) > 0 {
			ctx = workqueue.WithClass(ctx, classes[0])
		}
		jobs, err := r.Evaluation.Store.ClaimIngestionEvaluations(ctx, 32)
		if err != nil {
			return nil, err
		}
		intents := make([]Intent, 0, len(jobs))
		for _, job := range jobs {
			intents = append(intents, dispatchIntent{
				// Optional vector work has its own queue and activity slots. A plugin's
				// deadline or outage can never occupy a served ingestion activity slot.
				options: client.StartWorkflowOptions{ID: content.StableID("ingestion-evaluation-workflow", job.Organization, job.ID), TaskQueue: workqueue.TaskQueue(job.WorkQueue) + "-evaluation", WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE},
				name:    "evaluate-ingestion-plugin-v0", input: Input{Organization: job.Organization, ReceiptID: job.ID},
				complete: func(ctx context.Context) error { return r.Evaluation.Store.IngestionEvaluationDispatched(ctx, job) },
				retry:    func(context.Context) error { return nil }, // Lease expiry recovers it.
			})
		}
		return intents, nil
	})
}
