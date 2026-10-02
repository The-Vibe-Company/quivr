package temporal

import (
	"context"
	"log/slog"
	"time"

	"go.temporal.io/sdk/activity"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Step describes one durable unit of background work. Checkpoints and terminal
// domain state belong to the activity; orchestration owns retries and pacing.
type Step interface {
	Run(workflow.Context) (StepProgress, error)
}

type StepProgress struct {
	Done bool
	Wait time.Duration
}

type workflowStep func(workflow.Context) (StepProgress, error)

func (s workflowStep) Run(ctx workflow.Context) (StepProgress, error) { return s(ctx) }

const operationRounds = 400

// operationWorkflow is shared by every background operation. The continuation
// retains the entry point and payload codec recorded by existing histories.
func operationWorkflow(ctx workflow.Context, step Step, name string, input interface{}) error {
	for i := 0; i < operationRounds; i++ {
		progress, err := step.Run(ctx)
		if err != nil {
			return err
		}
		if progress.Done {
			return nil
		}
		if progress.Wait > 0 {
			if err = workflow.Sleep(ctx, progress.Wait); err != nil {
				return err
			}
		}
	}
	return workflow.NewContinueAsNewError(ctx, name, input)
}

// activityPolicy keeps the same retry algorithm for every work budget.
// Attempts=0 retries outages indefinitely; connectors retain their terminal
// three-attempt budget. Receipt stages retain their invocation-aware deadlines.
type activityPolicy struct {
	Timeout, Heartbeat, RetryInterval time.Duration
	Attempts                          int32
}

func (p activityPolicy) options() workflow.ActivityOptions {
	return workflow.ActivityOptions{StartToCloseTimeout: p.Timeout, HeartbeatTimeout: p.Heartbeat,
		RetryPolicy: &sdktemporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: p.RetryInterval, MaximumAttempts: p.Attempts}}
}

func backgroundContext(ctx workflow.Context, kind string) workflow.Context {
	p := activityPolicy{Timeout: 30 * time.Minute, Heartbeat: stepHeartbeatTimeout, RetryInterval: 30 * time.Second}
	if kind == acquireWorkflow {
		p.Timeout = 5 * time.Minute
		p.Attempts = acquireAttempts
	}
	// The shared loop preserves activity codecs and timer ordering. This guard
	// preserves timeout/retry commands when replaying histories from old workers.
	if workflow.GetVersion(ctx, "shared-operation-policy", workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		switch kind {
		case rebuildWorkflowName:
			p.Timeout = 2 * time.Minute
		case acquireWorkflow:
			p.Heartbeat = 30 * time.Second
			p.RetryInterval = 10 * time.Second
		}
	}
	return workflow.WithActivityOptions(ctx, p.options())
}

// registerOperationStep shares pin and heartbeat ownership across the domain
// step adapters. A failed release retries the idempotent terminal step; pauses,
// outages and history rollover keep the same plan pinned.
func registerOperationStep[T any](w worker.ActivityRegistry, name string, pins Pinner, run func(context.Context, RebuildInput) (T, error), done func(T) bool) {
	w.RegisterActivityWithOptions(func(ctx context.Context, in RebuildInput) (T, error) {
		var progress T
		err := heartbeating(ctx, stepHeartbeatTimeout/3, func() error {
			pinned, err := pins.Pin(ctx, workOperation, in.Organization, in.OperationID)
			if err != nil {
				return err
			}
			progress, err = run(pinned, in)
			if err == nil && done(progress) {
				err = pins.Release(ctx, workOperation, in.Organization, in.OperationID)
			}
			return err
		})
		if err != nil {
			slog.Warn("background operation retrying", "operation_id", in.OperationID, "step", name, "attempt", activity.GetInfo(ctx).Attempt)
		}
		return progress, err
	}, activity.RegisterOptions{Name: name})
}
