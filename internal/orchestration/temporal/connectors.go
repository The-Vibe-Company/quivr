package temporal

import (
	"context"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"

	"go.temporal.io/sdk/activity"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	connectorTaskQueue           = "quivr-connectors-v0"
	acquireWorkflow              = "connector-acquire-v1"
	acquireActivity              = "connector-acquire"
	acquirePinnedActivity        = "connector-acquire-v2"
	releaseConnectorPlanActivity = "connector-release-plan"
	// connectorLease only hides a claimed instance from other dispatchers
	// until its workflow exists. A re-dispatch after expiry targets the same
	// run and is rejected while that run is still in flight, so a short lease
	// is safe and bounds the delay after a crash between claim and start.
	connectorLease = 30 * time.Second
	// acquireAttempts bounds acquisition only; terminal cleanup retries separately.
	acquireAttempts = 3
)

// ConnectorScheduler claims due Connector Instance runs.
type ConnectorScheduler interface {
	ClaimConnectorRuns(context.Context, time.Duration, int) ([]connectors.ConnectorRun, error)
	ReleaseConnectorRun(context.Context, connectors.ConnectorRun) error
}

// Connectors enables scheduled acquisition in the worker.
type Connectors struct {
	Scheduler ConnectorScheduler
	Acquirer  connectors.Acquirer
}

// AcquireInput identifies one acquisition run; it never carries a secret.
type AcquireInput struct {
	Organization string
	ConnectorID  string
	Run          int64
}

func acquireWorkflowFn(ctx workflow.Context, in AcquireInput) error {
	ctx = backgroundContext(ctx, acquireWorkflow)
	name := acquireActivity
	durableCleanup := workflow.GetVersion(ctx, "connector-terminal-cleanup", workflow.DefaultVersion, 1) != workflow.DefaultVersion
	settled := ctx
	if durableCleanup {
		name = acquirePinnedActivity
		opts := workflow.GetActivityOptions(ctx)
		opts.WaitForCancellation = true
		ctx = workflow.WithActivityOptions(ctx, opts)
		// Request cancellation through ctx, but wait for acknowledgement (or
		// worker-loss timeout) before releasing the plan beneath acquisition.
		settled, _ = workflow.NewDisconnectedContext(ctx)
	}
	err := operationWorkflow(ctx, workflowStep(func(ctx workflow.Context) (StepProgress, error) {
		err := workflow.ExecuteActivity(ctx, name, in).Get(settled, nil)
		return StepProgress{Done: true}, err
	}), acquireWorkflow, in)
	if !durableCleanup {
		return err
	}
	cleanup, _ := workflow.NewDisconnectedContext(ctx)
	cleanup = workflow.WithActivityOptions(cleanup, activityPolicy{
		Timeout: time.Minute, Heartbeat: stepHeartbeatTimeout, RetryInterval: 30 * time.Second,
	}.options())
	if releaseErr := workflow.ExecuteActivity(cleanup, releaseConnectorPlanActivity, in).Get(cleanup, nil); releaseErr != nil {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return releaseErr
	}
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func registerConnectors(w worker.Registry, c *Connectors, pins Pinner) {
	w.RegisterWorkflowWithOptions(acquireWorkflowFn, workflow.RegisterOptions{Name: acquireWorkflow})
	acquire := func(ctx context.Context, in AcquireInput, legacy bool) error {
		return heartbeating(ctx, stepHeartbeatTimeout/3, func() error {
			// The run's attempts resolve its connector kind in the plan its first
			// attempt pinned; the run's end releases it.
			id := fmt.Sprintf("%s:%d", in.ConnectorID, in.Run)
			pinned, err := pins.Pin(ctx, workConnectorRun, in.Organization, id)
			if err != nil {
				return err
			}
			err = c.Acquirer.Run(pinned, in.Organization, in.ConnectorID, in.Run)
			if legacy && (err == nil || activity.GetInfo(ctx).Attempt >= acquireAttempts) {
				if releaseErr := pins.Release(context.WithoutCancel(ctx), workConnectorRun, in.Organization, id); releaseErr != nil && err == nil {
					err = releaseErr
				}
			}
			return err
		})
	}
	// Keep the original activity's cleanup for histories that already scheduled it.
	w.RegisterActivityWithOptions(func(ctx context.Context, in AcquireInput) error {
		return acquire(ctx, in, true)
	}, activity.RegisterOptions{Name: acquireActivity})
	w.RegisterActivityWithOptions(func(ctx context.Context, in AcquireInput) error {
		return acquire(ctx, in, false)
	}, activity.RegisterOptions{Name: acquirePinnedActivity})
	w.RegisterActivityWithOptions(func(ctx context.Context, in AcquireInput) error {
		return heartbeating(ctx, stepHeartbeatTimeout/3, func() error {
			if err := pins.Release(ctx, workConnectorRun, in.Organization, fmt.Sprintf("%s:%d", in.ConnectorID, in.Run)); err != nil {
				// Release is idempotent and must finish before the run closes,
				// even if its dependency classifies a failure as non-retryable.
				return sdktemporal.NewApplicationError("release connector plan", "ConnectorPlanRelease", err)
			}
			return nil
		})
	}, activity.RegisterOptions{Name: releaseConnectorPlanActivity})
}

// acquisitionWorkflowID is stable per run, so at most one acquisition of an
// instance is in flight: a duplicate dispatch of a running or completed run is
// rejected; only a failed run may be started again under the same identity.
func acquisitionWorkflowID(r connectors.ConnectorRun) string {
	return fmt.Sprintf("connector:%s:%s:%d", r.Organization, r.ConnectorID, r.Run)
}
