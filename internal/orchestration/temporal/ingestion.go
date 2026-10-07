// Package temporal keeps orchestration mechanics outside domain operations.
package temporal

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/normalization"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
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
	normalize := workflow.WithActivityOptions(ctx, activityPolicy{Timeout: normalizationActivityTimeout, Heartbeat: stepHeartbeatTimeout, RetryInterval: 30 * time.Second}.options())
	// The heartbeat timeout only detects an attempt nobody runs: a live
	// attempt still ends at its 30 s start-to-close bound, and a plugin call
	// inside it at its own deadline.
	ctx = workflow.WithActivityOptions(ctx, activityPolicy{Timeout: 30 * time.Second, Heartbeat: stepHeartbeatTimeout, RetryInterval: 10 * time.Second}.options())
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
	enrich := workflow.WithActivityOptions(ctx, activityPolicy{Timeout: enrichmentActivityTimeout, Heartbeat: stepHeartbeatTimeout, RetryInterval: 10 * time.Second}.options())
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
	Queues               workqueue.Config
	QueueWorkers         []worker.Worker
	dispatchDone         <-chan struct{}
	IngestionBatchWorker worker.Worker
	EvaluationWorker     worker.Worker
	Evaluation           *processing.Evaluator
	Client               client.Client
	Worker               worker.Worker
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
func Start(ctx context.Context, address string, service processing.Service, rebuilder retrieval.Rebuilder, store DispatchStore, conns *Connectors, backfiller *backfill.Backfiller, reprocessor *quarantine.Reprocessor, pins Pinner, evaluationConcurrency int, tlsConfig *tls.Config, options ...RuntimeOptions) (*Runtime, error) {
	grace := time.Minute
	if len(options) > 0 && options[0].ShutdownGrace > 0 {
		grace = options[0].ShutdownGrace
	}
	if evaluationConcurrency == 0 {
		evaluationConcurrency = 4
	}
	if evaluationConcurrency < 1 || evaluationConcurrency > 32 {
		return nil, errors.New("evaluation concurrency must be between 1 and 32")
	}
	if pins == nil {
		pins = unpinned{}
	}
	// The application retries startup after transient connection failures.
	connect, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := Dial(connect, address, tlsConfig)
	if err != nil {
		return nil, err
	}
	settings := workqueue.Config{}
	if len(options) > 0 {
		settings = options[0].Queues
	}
	settings, err = settings.Resolve()
	if err != nil {
		c.Close()
		return nil, err
	}
	background := lifecycle.WorkContext(ctx)
	if len(options) > 0 && options[0].Tracker != nil {
		background = workqueue.WithTracker(background, options[0].Tracker)
	}
	runtime := &Runtime{Evaluation: service.Evaluation, Client: c, Store: store, Connectors: conns, Queues: settings}
	startWorker := func(queue string, slots int, gate *capacityGate, register func(worker.Worker)) error {
		w := worker.New(c, queue, worker.Options{MaxConcurrentActivityExecutionSize: slots, WorkerStopTimeout: grace, BackgroundActivityContext: background, Interceptors: []interceptor.WorkerInterceptor{gate}})
		register(w)
		if err := w.Start(); err != nil {
			return err
		}
		runtime.QueueWorkers = append(runtime.QueueWorkers, w)
		return nil
	}
	registerCore := func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(materializeWorkflow, workflow.RegisterOptions{Name: "process-e5-v3"})
		registerIngestion(w, service, pins)
		registerIngestionBatches(w, service, pins)
		registerRebuild(w, rebuilder, pins)
		if conns != nil {
			registerConnectors(w, conns, pins)
		}
		if backfiller != nil {
			registerBackfill(w, *backfiller, pins)
		}
		if reprocessor != nil {
			registerReprocess(w, *reprocessor, pins)
		}
		if service.Evaluation != nil && service.Evaluation.Serving != nil {
			registerServingProjection(w, *service.Evaluation, pins)
		}
	}
	gates := map[string]*capacityGate{}
	for _, q := range settings.Queues {
		gates[q] = newCapacityGate(q, settings.Capacity(q))
	}
	for _, q := range settings.Queues {
		gate := gates[q]
		if err = startWorker(workqueue.TaskQueue(q), settings.Capacity(q), gate, registerCore); err != nil {
			break
		}
		// Legacy queues retain their recorded names and inherited activity routing.
		// Their activity bodies share the class gate, so upgrade drain does not
		// multiply the configured execution capacity.
		legacy := []string{}
		if q == workqueue.Live {
			legacy = []string{connectorTaskQueue, ingestionBatchQueue}
			// The oldest queue mixed live ingestion and bulk rebuild histories.
			// Only a process serving both classes drains it.
			if settings.Serves(workqueue.Bulk) {
				legacy = append(legacy, taskQueue)
			}
		}
		if q == workqueue.Bulk {
			legacy = []string{backfillTaskQueue}
		}
		for _, queue := range legacy {
			legacyGate := gate
			if queue == taskQueue {
				legacyGate = &capacityGate{class: workqueue.Live, slots: gate.slots, bulk: gates[workqueue.Bulk]}
			}
			if err = startWorker(queue, settings.Capacity(q), legacyGate, registerCore); err != nil {
				break
			}
		}
		if err != nil {
			break
		}
		if service.Evaluation != nil {
			evaluationGate := newCapacityGate(q, evaluationConcurrency)
			register := func(w worker.Worker) { registerIngestionEvaluation(w, *service.Evaluation, pins) }
			if err = startWorker(workqueue.TaskQueue(q)+"-evaluation", evaluationConcurrency, evaluationGate, register); err != nil {
				break
			}
			if q == workqueue.Live {
				if err = startWorker(ingestionEvaluationQueue, evaluationConcurrency, evaluationGate, register); err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		for _, w := range runtime.QueueWorkers {
			w.Stop()
		}
		c.Close()
		return nil, err
	}
	done := make(chan struct{})
	runtime.dispatchDone = done
	go func() { defer close(done); runtime.dispatch(ctx) }()
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

// RuntimeOptions carries the process grace budget to all activity workers.
type RuntimeOptions struct {
	ShutdownGrace time.Duration
	Queues        workqueue.Config
	Tracker       workqueue.Tracker
}

// Close joins workers and dispatcher while the process budget allows it. At
// expiry, closing the client aborts remaining RPCs and durable leases recover.
func (r *Runtime) Close(ctx context.Context) {
	defer r.Client.Close()
	var wg sync.WaitGroup
	for _, w := range append([]worker.Worker{r.IngestionBatchWorker, r.EvaluationWorker, r.Worker, r.ConnectorWorker, r.BackfillWorker}, r.QueueWorkers...) {
		if w != nil {
			wg.Add(1)
			go func() { defer wg.Done(); w.Stop() }()
		}
	}
	stopped := make(chan struct{})
	go func() { wg.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		return
	}
	if r.dispatchDone != nil {
		select {
		case <-r.dispatchDone:
		case <-ctx.Done():
		}
	}
}

// ReceiptDispatchStore owns receipt dispatch and failed-start progress.
type ReceiptDispatchStore interface {
	ClaimIngestionBatches(context.Context, int) ([]content.DispatchBatch, error)
	IngestionBatchDispatched(context.Context, string) error
	Progress(context.Context, string, string, string, string) error
}

// OperationDispatchStore owns operation dispatch acknowledgement.
type OperationDispatchStore interface {
	ClaimOperations(context.Context, int) ([]operations.Dispatch, error)
	OperationDispatched(context.Context, operations.Dispatch) error
}

type DispatchStore interface {
	ReceiptDispatchStore
	OperationDispatchStore
}
