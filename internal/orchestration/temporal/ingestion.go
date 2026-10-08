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
)

type Input struct {
	Organization string
	ReceiptID    string
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
// Normalization records an outcome (or leaves one to publication), so a
// second round only happens when a route or the pin changed in between.
const maxNormalizationRounds = 3

// enrichmentActivityTimeout bounds one enrichment attempt: the engine cap of
// a segment_and_embed call plus storage and indexing, so an ingestion plugin
// declaring a longer timeout_ms is not cut before its deadline (THE-810).
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
	Queues       workqueue.Config
	QueueWorkers []worker.Worker
	dispatchDone <-chan struct{}
	Evaluation   *processing.Evaluator
	Client       client.Client
	Store        DispatchStore
	Connectors   *Connectors
}

// Start runs one worker per selected queue class. A non-nil conns also
// schedules Connector Instance acquisition runs, and a non-nil backfiller or
// reprocessor serves backfills or quarantine reprocesses on the bulk queue.
// pins pins the processing of each receipt, each Operation and each connector
// run to the plan it started on; nil leaves them on the active plan.
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
	startWorker := func(queue, class string, slots int, register func(worker.Worker)) error {
		w := worker.New(c, queue, worker.Options{MaxConcurrentActivityExecutionSize: slots, WorkerStopTimeout: grace, BackgroundActivityContext: background, Interceptors: []interceptor.WorkerInterceptor{&queueClass{class: class}}})
		register(w)
		if err := w.Start(); err != nil {
			return err
		}
		runtime.QueueWorkers = append(runtime.QueueWorkers, w)
		return nil
	}
	registerCore := func(w worker.Worker) {
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
	for _, q := range settings.Queues {
		if err = startWorker(workqueue.TaskQueue(q), q, settings.Capacity(q), registerCore); err != nil {
			break
		}
		if service.Evaluation != nil {
			register := func(w worker.Worker) { registerIngestionEvaluation(w, *service.Evaluation, pins) }
			if err = startWorker(workqueue.TaskQueue(q)+"-evaluation", q, evaluationConcurrency, register); err != nil {
				break
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
	for _, w := range r.QueueWorkers {
		wg.Go(w.Stop)
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
