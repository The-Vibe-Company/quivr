// Package temporal keeps orchestration mechanics outside domain operations.
package temporal

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/normalization"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"
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

// stepHeartbeatTimeout bounds how long an attempt of any step of a receipt's
// processing can go unheard. A running attempt heartbeats well within it. An
// attempt that no worker runs, because its worker was killed or a stopping
// worker took the task or never reported it, is retried after this bound
// rather than its start-to-close bound (THE-745, THE-835), so a dead worker
// does not hold work pinned to a draining plugin version.
const stepHeartbeatTimeout = 10 * time.Second

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
	normalize := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: normalizationActivityTimeout, HeartbeatTimeout: stepHeartbeatTimeout, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second}})
	// The heartbeat timeout only detects an attempt nobody runs: a live
	// attempt still ends at its 30 s start-to-close bound, and a plugin call
	// inside it at its own deadline.
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, HeartbeatTimeout: stepHeartbeatTimeout, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second}})
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
	enrich := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: enrichmentActivityTimeout, HeartbeatTimeout: stepHeartbeatTimeout, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second}})
	return workflow.ExecuteActivity(enrich, "enrich-e5", input).Get(ctx, nil)
}

// enrichmentActivityTimeout bounds one enrichment attempt: the engine cap of
// a segment_and_embed call plus storage and indexing. It was the flat 30 s of
// the other steps, which cut any ingestion plugin declaring a longer
// timeout_ms before its deadline (THE-810). The attempt heartbeats, so one
// that no worker runs is retried after the heartbeat timeout.
const enrichmentActivityTimeout = processing.SegmentAndEmbedTimeoutCap + time.Minute

// Pinner pins each piece of work to the Pipeline Plan it started on (Spec 5):
// every activity of the work, retries and restarts included, resolves its
// plugins in that plan.
type Pinner interface {
	// Pin returns ctx carrying the work pinned to its plan: the active one
	// when the work is first seen.
	Pin(ctx context.Context, kind, org, id string) (context.Context, error)
	// Release forgets finished work, so the registrations of its plan can
	// stop draining.
	Release(ctx context.Context, kind, org, id string) error
}

// Work kinds, as the Pinner records them.
const (
	workIngestion    = "ingestion"
	workConnectorRun = "connector_run"
	workOperation    = "operation"
)

// unpinned runs work on the active plan at each call, as before pinning.
type unpinned struct{}

func (unpinned) Pin(ctx context.Context, _, _, _ string) (context.Context, error) { return ctx, nil }
func (unpinned) Release(context.Context, string, string, string) error            { return nil }

type Runtime struct {
	Client client.Client
	Worker worker.Worker
	// BackfillWorker serves backfills on their own task queue, one activity
	// at a time, below live content processing.
	BackfillWorker worker.Worker
	// ConnectorWorker serves acquisition on its own task queue so scheduled
	// polling is never starved by content processing, and the reverse.
	ConnectorWorker worker.Worker
	Store           DispatchStore
	Connectors      *Connectors
}

// Start runs the worker. A non-nil conns also schedules Connector Instance
// acquisition runs on their own task queue, and a non-nil backfiller or
// reprocessor serves backfills or quarantine reprocesses on the background
// queue. pins pins the processing of each receipt, each Operation and each
// connector run to the plan it started on; nil leaves them on the active
// plan.
func Start(ctx context.Context, address string, service processing.Service, rebuilder retrieval.Rebuilder, store DispatchStore, conns *Connectors, backfiller *backfill.Backfiller, reprocessor *quarantine.Reprocessor, pins Pinner) (*Runtime, error) {
	if pins == nil {
		pins = unpinned{}
	}
	// The application retries startup after transient connection failures.
	connect, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := client.DialContext(connect, client.Options{HostPort: address})
	if err != nil {
		return nil, err
	}
	w := worker.New(c, taskQueue, worker.Options{MaxConcurrentActivityExecutionSize: 4})
	w.RegisterWorkflowWithOptions(materializeWorkflow, workflow.RegisterOptions{Name: "process-e5-v3"})
	registerIngestion(w, service, pins)
	registerRebuild(w, rebuilder, pins)
	var cw worker.Worker
	if conns != nil {
		cw = worker.New(c, connectorTaskQueue, worker.Options{MaxConcurrentActivityExecutionSize: 4})
		registerConnectors(cw, conns, pins)
		if err = cw.Start(); err != nil {
			c.Close()
			return nil, err
		}
	}
	var bw worker.Worker
	if backfiller != nil || reprocessor != nil {
		bw = worker.New(c, backfillTaskQueue, worker.Options{MaxConcurrentActivityExecutionSize: 1})
		if backfiller != nil {
			registerBackfill(bw, *backfiller, pins)
		}
		if reprocessor != nil {
			registerReprocess(bw, *reprocessor, pins)
		}
		if err = bw.Start(); err != nil {
			if cw != nil {
				cw.Stop()
			}
			c.Close()
			return nil, err
		}
	}
	// Start retries are bounded per attempt; the caller can retry startup without losing accepted work.
	if err = w.Start(); err != nil {
		if cw != nil {
			cw.Stop()
		}
		if bw != nil {
			bw.Stop()
		}
		c.Close()
		return nil, err
	}
	runtime := &Runtime{Client: c, Worker: w, ConnectorWorker: cw, BackfillWorker: bw, Store: store, Connectors: conns}
	go runtime.dispatch(ctx)
	if conns != nil {
		go runtime.scheduleConnectors(ctx)
	}
	return runtime, nil
}

// Steps runs the steps of a receipt's processing (processing.Service).
type Steps interface {
	Run(ctx context.Context, org, receiptID string) error
	Normalize(ctx context.Context, org, receiptID string) error
	Enrich(ctx context.Context, org, receiptID string) error
}

// registerIngestion registers the activities of a receipt's processing. Each
// resolves plugins in the plan its first activity pinned; enrichment, the last
// one, releases it. Each heartbeats, so an attempt no worker runs is retried
// after stepHeartbeatTimeout.
func registerIngestion(w worker.ActivityRegistry, steps Steps, pins Pinner) {
	pinned := func(ctx context.Context, in Input) (context.Context, error) {
		return pins.Pin(ctx, workIngestion, in.Organization, in.ReceiptID)
	}
	step := func(ctx context.Context, run func() error) error {
		return heartbeating(ctx, stepHeartbeatTimeout/3, run)
	}
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		ctx, err := pinned(ctx, in)
		if err != nil {
			return err
		}
		return step(ctx, func() error { return steps.Run(ctx, in.Organization, in.ReceiptID) })
	}, activity.RegisterOptions{Name: "process-token-windows"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) (ProcessResult, error) {
		ctx, err := pinned(ctx, in)
		if err != nil {
			return ProcessResult{}, err
		}
		err = step(ctx, func() error { return steps.Run(ctx, in.Organization, in.ReceiptID) })
		if errors.Is(err, content.ErrNormalizationPending) {
			return ProcessResult{NormalizationRequired: true}, nil
		}
		return ProcessResult{}, err
	}, activity.RegisterOptions{Name: "process-token-windows-v2"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		ctx, err := pinned(ctx, in)
		if err != nil {
			return err
		}
		return step(ctx, func() error { return steps.Normalize(ctx, in.Organization, in.ReceiptID) })
	}, activity.RegisterOptions{Name: "normalize-external"})
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		pinnedCtx, err := pinned(ctx, in)
		if err != nil {
			return err
		}
		if err = step(ctx, func() error { return steps.Enrich(pinnedCtx, in.Organization, in.ReceiptID) }); err != nil {
			return err
		}
		return pins.Release(ctx, workIngestion, in.Organization, in.ReceiptID)
	}, activity.RegisterOptions{Name: "enrich-e5"})
}

// heartbeating runs run while it records an activity heartbeat at once and
// then every interval, so the server can tell a live attempt from one no
// worker is running. A heartbeat never moves the attempt's deadline, and the
// heartbeats stop when its context ends.
func heartbeating(ctx context.Context, interval time.Duration, run func() error) error {
	done := make(chan struct{})
	defer close(done)
	activity.RecordHeartbeat(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	return run()
}

func (r *Runtime) Close() {
	r.Worker.Stop()
	if r.ConnectorWorker != nil {
		r.ConnectorWorker.Stop()
	}
	if r.BackfillWorker != nil {
		r.BackfillWorker.Stop()
	}
	r.Client.Close()
}

// ingestionDispatchBatch bounds outstanding leases and work per polling tick.
const ingestionDispatchBatch = 32

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
		cancel()
		// Give ingestion its own bounded attempt, so a slow operation start cannot
		// consume the batch's time. This stays below the store's five-second lease.
		attempt, cancel = context.WithTimeout(ctx, 3*time.Second)
		r.dispatchIngestion(attempt)
		cancel()
	}
}

func (r *Runtime) dispatchIngestion(ctx context.Context) {
	batch, err := r.Store.Claim(ctx, ingestionDispatchBatch)
	if err != nil {
		slog.Warn("outbox temporarily unavailable")
		return
	}
	for _, d := range batch {
		if ctx.Err() != nil {
			return
		}
		_, err = r.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: content.StableID("ingestion-e5-v4", d.Organization, d.ReceiptID), TaskQueue: taskQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, "process-e5-v3", Input{Organization: d.Organization, ReceiptID: d.ReceiptID})
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if err == nil || errors.As(err, &already) {
			err = r.Store.Dispatched(ctx, d)
		}
		if err != nil {
			// Feedback shares the batch deadline; unavailable dependencies must not
			// extend dispatch beyond its lease or hold the next polling tick forever.
			_ = r.Store.Progress(ctx, d.Organization, d.ReceiptID, "retrying", "dispatch_unavailable")
			slog.Warn("ingestion dispatch pending", "receipt_id", d.ReceiptID)
		}
	}
}

type DispatchStore interface {
	Claim(context.Context, int) ([]content.Dispatch, error)
	Dispatched(context.Context, content.Dispatch) error
	ClaimOperation(context.Context) (operations.Dispatch, error)
	OperationDispatched(context.Context, operations.Dispatch) error
	Progress(context.Context, string, string, string, string) error
}
