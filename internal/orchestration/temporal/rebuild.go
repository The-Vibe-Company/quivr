package temporal

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const rebuildWorkflowName = "projection-rebuild-v1"

// RebuildInput carries only stable identifiers; Operation state lives in PostgreSQL.
type RebuildInput struct {
	Organization string
	OperationID  string
}

// rebuildRounds bounds one workflow run's history; longer rebuilds continue as new
// under the same workflow identity.
const rebuildRounds = 400

func rebuildWorkflow(ctx workflow.Context, in RebuildInput) error {
	// A killed worker is detected by the heartbeat timeout and the step resumes
	// from durable coverage. Backoff is capped so recovery stays prompt, and
	// attempts are unbounded so an outage never invents a failed Operation.
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 2 * time.Minute, HeartbeatTimeout: 10 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 30 * time.Second}})
	for i := 0; i < rebuildRounds; i++ {
		var done bool
		if err := workflow.ExecuteActivity(ctx, "rebuild-projection-step", in).Get(ctx, &done); err != nil {
			return err
		}
		if done {
			return nil
		}
		// Coverage gaps can come from in-flight ingestion; pause briefly between rounds.
		if err := workflow.Sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	return workflow.NewContinueAsNewError(ctx, rebuildWorkflowName, in)
}

func registerRebuild(w worker.Worker, rebuilder retrieval.Rebuilder, pins Pinner) {
	w.RegisterWorkflowWithOptions(rebuildWorkflow, workflow.RegisterOptions{Name: rebuildWorkflowName})
	w.RegisterActivityWithOptions(func(ctx context.Context, in RebuildInput) (bool, error) {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					activity.RecordHeartbeat(ctx)
				}
			}
		}()
		// Every step derives through the plan the Operation's first step
		// pinned; the last one releases it.
		pinned, err := pins.Pin(ctx, workOperation, in.Organization, in.OperationID)
		if err != nil {
			return false, err
		}
		done, err := rebuilder.Step(pinned, in.Organization, in.OperationID)
		if err != nil {
			// Bounded diagnostics: identifiers and attempt only, never payloads.
			slog.Warn("projection rebuild retrying", "operation_id", in.OperationID, "attempt", activity.GetInfo(ctx).Attempt)
		}
		if err == nil && done {
			err = pins.Release(ctx, workOperation, in.Organization, in.OperationID)
		}
		return done, err
	}, activity.RegisterOptions{Name: "rebuild-projection-step"})
}

// dispatchOperation starts one committed Operation. Duplicate starts are the
// same durable execution, so dispatch intent is then marked complete.
func (r *Runtime) dispatchOperation(ctx context.Context) {
	d, err := r.Store.ClaimOperation(ctx)
	if errors.Is(err, operations.ErrNoDispatch) {
		return
	}
	if err != nil {
		slog.Warn("operation outbox temporarily unavailable")
		return
	}
	name, queue := rebuildWorkflowName, taskQueue
	if d.Kind == operations.KindBackfill {
		name, queue = backfillWorkflowName, backfillTaskQueue
	}
	_, err = r.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: content.StableID(name, d.Organization, d.OperationID), TaskQueue: queue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, name, RebuildInput{Organization: d.Organization, OperationID: d.OperationID})
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if err == nil || errors.As(err, &already) {
		err = r.Store.OperationDispatched(ctx, d)
	}
	if err != nil {
		// The Operation stays queued and the lease expires for another attempt.
		slog.Warn("operation dispatch pending", "operation_id", d.OperationID)
	}
}
