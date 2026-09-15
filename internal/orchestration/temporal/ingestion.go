// Package temporal keeps orchestration mechanics outside domain operations.
package temporal

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"

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

func materializeWorkflow(ctx workflow.Context, input Input) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second}})
	if err := workflow.ExecuteActivity(ctx, "process-token-windows", input).Get(ctx, nil); err != nil {
		return err
	}
	return workflow.ExecuteActivity(ctx, "enrich-e5", input).Get(ctx, nil)
}

type Runtime struct {
	Client client.Client
	Worker worker.Worker
	Store  DispatchStore
}

func Start(ctx context.Context, address string, service processing.Service, store DispatchStore) (*Runtime, error) {
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
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error { return service.Enrich(ctx, in.Organization, in.ReceiptID) }, activity.RegisterOptions{Name: "enrich-e5"})
	// Start retries are bounded per attempt; the caller can retry startup without losing accepted work.
	if err = w.Start(); err != nil {
		c.Close()
		return nil, err
	}
	runtime := &Runtime{Client: c, Worker: w, Store: store}
	go runtime.dispatch(ctx)
	return runtime, nil
}
func (r *Runtime) Close() { r.Worker.Stop(); r.Client.Close() }
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
		d, err := r.Store.Claim(attempt)
		if err == nil {
			_, err = r.Client.ExecuteWorkflow(attempt, client.StartWorkflowOptions{ID: content.StableID("ingestion-e5-v3", d.Organization, d.ReceiptID), TaskQueue: taskQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, "process-e5-v3", Input{Organization: d.Organization, ReceiptID: d.ReceiptID})
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
	Progress(context.Context, string, string, string, string) error
}
