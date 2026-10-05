package temporal

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"log/slog"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

// Intent is a committed request to start work. Its source owns acknowledgement
// and failure recovery; the dispatcher owns Temporal's start/duplicate protocol.
type Intent interface {
	Start() (client.StartWorkflowOptions, string, interface{})
	Complete(context.Context) error
	Retry(context.Context) error
}

type IntentSource interface {
	Claim(context.Context) ([]Intent, error)
}

type intentSource func(context.Context) ([]Intent, error)

func (s intentSource) Claim(ctx context.Context) ([]Intent, error) { return s(ctx) }

type dispatchIntent struct {
	options         client.StartWorkflowOptions
	name            string
	input           interface{}
	complete, retry func(context.Context) error
}

func (i dispatchIntent) Start() (client.StartWorkflowOptions, string, interface{}) {
	return i.options, i.name, i.input
}
func (i dispatchIntent) Complete(ctx context.Context) error { return i.complete(ctx) }
func (i dispatchIntent) Retry(ctx context.Context) error    { return i.retry(ctx) }

const dispatchAttempt = 3 * time.Second
const ingestionDispatchBatch = 8
const ingestionStartConcurrency = 8
const operationDispatchBatch = 32

// One dispatcher runs independent source lanes. A stalled connector start
// cannot hold receipts or operations behind its deadline; all lanes share the
// same bounded batch/start protocol.
func (r *Runtime) dispatch(ctx context.Context) {
	var lanes sync.WaitGroup
	poll := func(period time.Duration, source IntentSource, starts int) {
		lanes.Add(1)
		go func() { defer lanes.Done(); r.pollIntents(ctx, period, source, starts) }()
	}
	poll(200*time.Millisecond, r.ingestionIntents(), ingestionStartConcurrency)
	poll(200*time.Millisecond, r.operationIntents(), 1)
	if r.Connectors != nil {
		poll(500*time.Millisecond, r.connectorIntents(), 1)
	}
	if r.Evaluation != nil {
		poll(time.Second, r.ingestionEvaluationIntents(), 1)
		if r.Evaluation.Serving != nil {
			poll(200*time.Millisecond, r.servingProjectionIntents(), 1)
		}
	}
	lanes.Wait()
}

func (r *Runtime) pollIntents(ctx context.Context, period time.Duration, source IntentSource, starts int) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		attempt, cancel := context.WithTimeout(lifecycle.WorkContext(ctx), dispatchAttempt)
		r.dispatchBatch(attempt, source, starts)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}

}

// dispatchBatch starts only the claimed arrival-ordered batch, with bounded
// parallelism. It joins all attempts before another claim; an expired attempt
// leaves unstarted receipts leased for recovery under the same workflow IDs.
func (r *Runtime) dispatchBatch(ctx context.Context, source IntentSource, starts int) {
	batch, err := source.Claim(ctx)
	if err != nil {
		slog.Warn("background intents temporarily unavailable")
		return
	}
	if starts == 1 {
		for _, intent := range batch {
			if ctx.Err() != nil {
				return
			}
			r.dispatchIntent(ctx, intent)
		}
		return
	}
	pending := make(chan Intent, len(batch))
	for _, intent := range batch {
		pending <- intent
	}
	close(pending)
	var running sync.WaitGroup
	for i := 0; i < min(starts, len(batch)); i++ {
		running.Go(func() {
			for intent := range pending {
				if ctx.Err() != nil {
					return
				}
				r.dispatchIntent(ctx, intent)
			}
		})
	}
	running.Wait()
}

func (r *Runtime) dispatchIntent(ctx context.Context, intent Intent) {
	options, name, input := intent.Start()
	_, err := r.Client.ExecuteWorkflow(ctx, options, name, input)
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if err == nil || errors.As(err, &already) {
		err = intent.Complete(ctx)
	}
	if err != nil {
		_ = intent.Retry(ctx)
		slog.Warn("background dispatch pending", "workflow_id", options.ID)
	}
}

func (r *Runtime) ingestionIntents() IntentSource {
	return intentSource(func(ctx context.Context) ([]Intent, error) {
		batch, err := r.Store.ClaimIngestionBatches(ctx, ingestionDispatchBatch)
		if err != nil {
			return nil, err
		}
		intents := make([]Intent, 0, len(batch))
		for _, b := range batch {
			intents = append(intents, dispatchIntent{
				options: client.StartWorkflowOptions{ID: b.ID, TaskQueue: ingestionBatchQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE},
				name:    ingestionBatchWorkflow, input: b,
				complete: func(ctx context.Context) error { return r.Store.IngestionBatchDispatched(ctx, b.ID) },
				retry: func(ctx context.Context) error {
					for _, d := range b.Receipts {
						if err := r.Store.Progress(ctx, d.Organization, d.ReceiptID, "retrying", "dispatch_unavailable"); err != nil {
							return err
						}
					}
					return nil
				},
			})
		}
		return intents, nil
	})
}

func (r *Runtime) operationIntents() IntentSource {
	return intentSource(func(ctx context.Context) ([]Intent, error) {
		batch, err := r.Store.ClaimOperations(ctx, operationDispatchBatch)
		if err != nil {
			return nil, err
		}
		intents := make([]Intent, 0, len(batch))
		for _, d := range batch {
			name, queue := rebuildWorkflowName, taskQueue
			switch d.Kind {
			case operations.KindBackfill:
				name, queue = backfillWorkflowName, backfillTaskQueue
			case operations.KindQuarantineReprocess:
				name, queue = reprocessWorkflowName, backfillTaskQueue
			}
			intents = append(intents, dispatchIntent{
				// IDs deliberately retain the legacy per-kind name, independent of the
				// shared workflow implementation, including completed executions.
				options: client.StartWorkflowOptions{ID: content.StableID(name, d.Organization, d.OperationID), TaskQueue: queue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE},
				name:    name, input: RebuildInput{Organization: d.Organization, OperationID: d.OperationID},
				complete: func(ctx context.Context) error { return r.Store.OperationDispatched(ctx, d) },
				retry:    func(context.Context) error { return nil }, // Lease expiry recovers it.
			})
		}
		return intents, nil
	})
}

func (r *Runtime) connectorIntents() IntentSource {
	return intentSource(func(ctx context.Context) ([]Intent, error) {
		runs, err := r.Connectors.Scheduler.ClaimConnectorRuns(ctx, connectorLease, 20)
		if err != nil {
			return nil, err
		}
		intents := make([]Intent, 0, len(runs))
		for _, run := range runs {
			intents = append(intents, dispatchIntent{
				options: client.StartWorkflowOptions{ID: acquisitionWorkflowID(run), TaskQueue: connectorTaskQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY},
				name:    acquireWorkflow, input: AcquireInput{Organization: run.Organization, ConnectorID: run.ConnectorID, Run: run.Run},
				complete: func(context.Context) error { return nil }, // Run completion advances its schedule.
				retry: func(ctx context.Context) error {
					release, cancel := lifecycle.CleanupContext(ctx, time.Second)
					defer cancel()
					return r.Connectors.Scheduler.ReleaseConnectorRun(release, run)
				},
			})
		}
		return intents, nil
	})
}
