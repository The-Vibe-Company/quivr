package temporal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
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
)

// ConnectorScheduler claims due Connector Instance runs.
type ConnectorScheduler interface {
	ClaimConnectorRuns(context.Context, time.Duration, int) ([]postgres.ConnectorRun, error)
	ReleaseConnectorRun(context.Context, postgres.ConnectorRun) error
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
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 3}})
	return workflow.ExecuteActivity(ctx, acquireActivity, in).Get(ctx, nil)
}

func registerConnectors(w worker.Worker, c *Connectors) {
	w.RegisterWorkflowWithOptions(acquireWorkflowFn, workflow.RegisterOptions{Name: acquireWorkflow})
	w.RegisterActivityWithOptions(func(ctx context.Context, in AcquireInput) error {
		return c.Acquirer.Run(ctx, in.Organization, in.ConnectorID, in.Run)
	}, activity.RegisterOptions{Name: acquireActivity})
}

// acquisitionWorkflowID is stable per run, so at most one acquisition of an
// instance is in flight: a duplicate dispatch of a running or completed run is
// rejected; only a failed run may be started again under the same identity.
func acquisitionWorkflowID(r postgres.ConnectorRun) string {
	return fmt.Sprintf("connector:%s:%s:%d", r.Organization, r.ConnectorID, r.Run)
}

func (r *Runtime) scheduleConnectors(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		claim, cancel := context.WithTimeout(ctx, 3*time.Second)
		runs, err := r.Connectors.Scheduler.ClaimConnectorRuns(claim, connectorLease, 20)
		cancel()
		if err != nil {
			slog.Warn("connector schedule temporarily unavailable")
		}
		for _, run := range runs {
			r.dispatchConnectorRun(ctx, run)
		}
	}
}

func (r *Runtime) dispatchConnectorRun(ctx context.Context, run postgres.ConnectorRun) {
	start, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := r.Client.ExecuteWorkflow(start, client.StartWorkflowOptions{ID: acquisitionWorkflowID(run), TaskQueue: connectorTaskQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY}, acquireWorkflow, AcquireInput{Organization: run.Organization, ConnectorID: run.ConnectorID, Run: run.Run})
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if err == nil || errors.As(err, &already) {
		return
	}
	// Release with a fresh deadline so the next tick retries instead of
	// waiting for lease expiry.
	release, done := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer done()
	_ = r.Connectors.Scheduler.ReleaseConnectorRun(release, run)
	slog.Warn("connector acquisition dispatch pending", "connector_id", run.ConnectorID)
}
