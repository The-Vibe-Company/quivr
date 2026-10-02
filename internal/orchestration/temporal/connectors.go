package temporal

import (
	"context"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	connectorTaskQueue = "quivr-connectors-v0"
	acquireWorkflow    = "connector-acquire-v1"
	acquireActivity    = "connector-acquire"
	// connectorLease only hides a claimed instance from other dispatchers
	// until its workflow exists. A re-dispatch after expiry targets the same
	// run and is rejected while that run is still in flight, so a short lease
	// is safe and bounds the delay after a crash between claim and start.
	connectorLease = 30 * time.Second
	// acquireAttempts bounds the attempts of one run; its last attempt
	// releases the run's plan whatever its outcome.
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
	return operationWorkflow(ctx, workflowStep(func(ctx workflow.Context) (StepProgress, error) {
		err := workflow.ExecuteActivity(ctx, acquireActivity, in).Get(ctx, nil)
		return StepProgress{Done: true}, err
	}), acquireWorkflow, in)
}

func registerConnectors(w worker.Worker, c *Connectors, pins Pinner) {
	w.RegisterWorkflowWithOptions(acquireWorkflowFn, workflow.RegisterOptions{Name: acquireWorkflow})
	w.RegisterActivityWithOptions(func(ctx context.Context, in AcquireInput) error {
		return heartbeating(ctx, stepHeartbeatTimeout/3, func() error {
			// The run's attempts resolve its connector kind in the plan its first
			// attempt pinned; the run's end releases it.
			id := fmt.Sprintf("%s:%d", in.ConnectorID, in.Run)
			pinned, err := pins.Pin(ctx, workConnectorRun, in.Organization, id)
			if err != nil {
				return err
			}
			err = c.Acquirer.Run(pinned, in.Organization, in.ConnectorID, in.Run)
			if err == nil || activity.GetInfo(ctx).Attempt >= acquireAttempts {
				if releaseErr := pins.Release(context.WithoutCancel(ctx), workConnectorRun, in.Organization, id); releaseErr != nil && err == nil {
					err = releaseErr
				}
			}
			return err
		})
	}, activity.RegisterOptions{Name: acquireActivity})
}

// acquisitionWorkflowID is stable per run, so at most one acquisition of an
// instance is in flight: a duplicate dispatch of a running or completed run is
// rejected; only a failed run may be started again under the same identity.
func acquisitionWorkflowID(r connectors.ConnectorRun) string {
	return fmt.Sprintf("connector:%s:%s:%d", r.Organization, r.ConnectorID, r.Run)
}
