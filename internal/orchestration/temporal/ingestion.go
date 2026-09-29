// Package temporal keeps orchestration mechanics outside domain operations.
package temporal

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/normalization"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const taskQueue = "quivr-content-v0"

// normalizationTerminal is the application error type of a normalization
// failure that retrying cannot fix.
const normalizationTerminal = "normalization_terminal"

type Input struct {
	Organization string
	ReceiptID    string
}

// normalizationActivityTimeout bounds one external normalization attempt: the
// engine invocation cap plus discovery and durable recording.
const normalizationActivityTimeout = normalization.TimeoutCap + time.Minute

func materializeWorkflow(ctx workflow.Context, input Input) error {
	// Histories started before external normalization existed replay without it.
	if workflow.GetVersion(ctx, "external-normalization", workflow.DefaultVersion, 1) == 1 {
		normalize := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: normalizationActivityTimeout, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second, NonRetryableErrorTypes: []string{normalizationTerminal}}})
		if err := workflow.ExecuteActivity(normalize, "normalize-external", input).Get(ctx, nil); err != nil {
			return err
		}
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second}})
	if err := workflow.ExecuteActivity(ctx, "process-token-windows", input).Get(ctx, nil); err != nil {
		return err
	}
	return workflow.ExecuteActivity(ctx, "enrich-e5", input).Get(ctx, nil)
}

type Runtime struct {
	Client client.Client
	Worker worker.Worker
	// ConnectorWorker serves acquisition on its own task queue so scheduled
	// polling is never starved by content processing, and the reverse.
	ConnectorWorker worker.Worker
	Store           DispatchStore
	Connectors      *Connectors
}

// Start runs the worker. A non-nil conns also schedules Connector Instance
// acquisition runs on their own task queue.
func Start(ctx context.Context, address string, service processing.Service, rebuilder retrieval.Rebuilder, store DispatchStore, conns *Connectors) (*Runtime, error) {
	// The application retries startup after transient connection failures.
	connect, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := client.DialContext(connect, client.Options{HostPort: address})
	if err != nil {
		return nil, err
	}
	w := worker.New(c, taskQueue, worker.Options{MaxConcurrentActivityExecutionSize: 4})
	w.RegisterWorkflowWithOptions(materializeWorkflow, workflow.RegisterOptions{Name: "process-e5-v3"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		return service.Run(ctx, in.Organization, in.ReceiptID)
	}, activity.RegisterOptions{Name: "process-token-windows"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		err := service.Normalize(ctx, in.Organization, in.ReceiptID)
		var terminal *normalization.TerminalError
		if errors.As(err, &terminal) {
			return temporal.NewNonRetryableApplicationError(terminal.Error(), normalizationTerminal, nil)
		}
		return err
	}, activity.RegisterOptions{Name: "normalize-external"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error { return service.Enrich(ctx, in.Organization, in.ReceiptID) }, activity.RegisterOptions{Name: "enrich-e5"})
	registerRebuild(w, rebuilder)
	var cw worker.Worker
	if conns != nil {
		cw = worker.New(c, connectorTaskQueue, worker.Options{MaxConcurrentActivityExecutionSize: 4})
		registerConnectors(cw, conns)
		if err = cw.Start(); err != nil {
			c.Close()
			return nil, err
		}
	}
	// Start retries are bounded per attempt; the caller can retry startup without losing accepted work.
	if err = w.Start(); err != nil {
		if cw != nil {
			cw.Stop()
		}
		c.Close()
		return nil, err
	}
	runtime := &Runtime{Client: c, Worker: w, ConnectorWorker: cw, Store: store, Connectors: conns}
	go runtime.dispatch(ctx)
	if conns != nil {
		go runtime.scheduleConnectors(ctx)
	}
	return runtime, nil
}
func (r *Runtime) Close() {
	r.Worker.Stop()
	if r.ConnectorWorker != nil {
		r.ConnectorWorker.Stop()
	}
	r.Client.Close()
}
func (r *Runtime) dispatch(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
		r.dispatchOperation(attempt)
		d, err := r.Store.Claim(attempt)
		if err == nil {
			_, err = r.Client.ExecuteWorkflow(attempt, client.StartWorkflowOptions{ID: content.StableID("ingestion-e5-v4", d.Organization, d.ReceiptID), TaskQueue: taskQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, "process-e5-v3", Input{Organization: d.Organization, ReceiptID: d.ReceiptID})
			var already *serviceerror.WorkflowExecutionAlreadyStarted
			if err == nil || errors.As(err, &already) {
				err = r.Store.Dispatched(attempt, d)
			}
			if err != nil {
				feedback, finish := context.WithTimeout(ctx, time.Second)
				_ = r.Store.Progress(feedback, d.Organization, d.ReceiptID, "retrying", "dispatch_unavailable")
				finish()
				slog.Warn("ingestion dispatch pending", "receipt_id", d.ReceiptID)
			}
		} else if !errors.Is(err, content.ErrNoDispatch) {
			slog.Warn("outbox temporarily unavailable")
		}
		cancel()
	}
}

type DispatchStore interface {
	Claim(context.Context) (content.Dispatch, error)
	Dispatched(context.Context, content.Dispatch) error
	ClaimOperation(context.Context) (operations.Dispatch, error)
	OperationDispatched(context.Context, operations.Dispatch) error
	Progress(context.Context, string, string, string, string) error
}
