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

type Input struct {
	Organization string
	ReceiptID    string
}

// ProcessResult is the result of process-token-windows-v2.
type ProcessResult struct {
	// NormalizationRequired reports a routed Blob Version without a recorded
	// normalization outcome: nothing was published, normalize-external must
	// run first.
	NormalizationRequired bool
}

// normalizationActivityTimeout bounds one external normalization attempt: the
// engine invocation cap plus discovery and durable recording.
const normalizationActivityTimeout = normalization.TimeoutCap + time.Minute

// maxNormalizationRounds bounds normalize-then-process rounds of one receipt.
// normalize-external records an outcome (or leaves one to publication), so a
// second round only happens when a route or the pin changed in between.
const maxNormalizationRounds = 3

func materializeWorkflow(ctx workflow.Context, input Input) error {
	// Histories started before external normalization existed replay without
	// it (DefaultVersion). Version 1 ran normalize-external first for every
	// receipt; version 2 runs it only when publication reports a routed Blob
	// without a normalization outcome, so other content runs no extra Activity.
	version := workflow.GetVersion(ctx, "external-normalization", workflow.DefaultVersion, 2)
	normalize := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: normalizationActivityTimeout, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second}})
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second}})
	switch version {
	case 1:
		if err := workflow.ExecuteActivity(normalize, "normalize-external", input).Get(ctx, nil); err != nil {
			return err
		}
		fallthrough
	case workflow.DefaultVersion:
		if err := workflow.ExecuteActivity(ctx, "process-token-windows", input).Get(ctx, nil); err != nil {
			return err
		}
	default:
		for round := 0; ; round++ {
			var result ProcessResult
			if err := workflow.ExecuteActivity(ctx, "process-token-windows-v2", input).Get(ctx, &result); err != nil {
				return err
			}
			if !result.NormalizationRequired {
				break
			}
			if round == maxNormalizationRounds {
				return errors.New("normalization recorded no outcome")
			}
			if err := workflow.ExecuteActivity(normalize, "normalize-external", input).Get(ctx, nil); err != nil {
				return err
			}
		}
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
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) (ProcessResult, error) {
		err := service.Run(ctx, in.Organization, in.ReceiptID)
		if errors.Is(err, content.ErrNormalizationPending) {
			return ProcessResult{NormalizationRequired: true}, nil
		}
		return ProcessResult{}, err
	}, activity.RegisterOptions{Name: "process-token-windows-v2"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		return service.Normalize(ctx, in.Organization, in.ReceiptID)
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
