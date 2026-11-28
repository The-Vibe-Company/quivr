package temporal

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"go.opentelemetry.io/otel/trace"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const ingestionBatchWorkflow = "process-ingestion-batch-v1"
const ingestionBatchActivity = "process-receipt-batch-v1"
const ingestionBatchReleaseActivity = "release-receipt-batch-v1"

// A receipt holds one of ingestionReceiptConcurrency Stage-1 slots (pin, Run,
// normalization) only until it is retrieval_ready. Its Stage-2 enrichment then
// holds one of ingestionEnrichConcurrency slots, so slow embedders never keep
// ready receipts from freeing Stage-1 slots for their siblings.
const ingestionReceiptConcurrency = 16
const ingestionEnrichConcurrency = 16

// Two waves of sixteen receipts cover a full batch in each stage. Four
// activities admit at most 64 receipts per worker per stage; provider admission
// remains independently bounded. Each receipt keeps its per-step deadlines; a
// silent worker is recovered by the heartbeat bound.
const ingestionBatchTimeout = 4 * (30*time.Second + maxNormalizationRounds*(normalizationActivityTimeout+30*time.Second) + enrichmentActivityTimeout)

// Remaining indexes belong to this activity's input. A nil result from an
// older history means all receipts completed, preserving its release command.
type ingestionBatchResult struct{ Remaining []int }

func ingestionBatch(ctx workflow.Context, in content.DispatchBatch) error {
	ctx = workflow.WithActivityOptions(ctx, activityPolicy{Timeout: ingestionBatchTimeout, Heartbeat: stepHeartbeatTimeout, RetryInterval: 10 * time.Second}.options())
	for len(in.Receipts) > 0 {
		var result ingestionBatchResult
		if err := workflow.ExecuteActivity(ctx, ingestionBatchActivity, in).Get(ctx, &result); err != nil {
			return err
		}
		pending := make(map[int]bool, len(result.Remaining))
		for _, i := range result.Remaining {
			pending[i] = true
		}
		completed, remaining := in, in
		completed.Receipts, remaining.Receipts = nil, nil
		for i, receipt := range in.Receipts {
			if pending[i] {
				remaining.Receipts = append(remaining.Receipts, receipt)
			} else {
				completed.Receipts = append(completed.Receipts, receipt)
			}
		}
		// Only release receipts whose completion is now in workflow history.
		// A poisoned sibling retains its own plan without holding finished work.
		release := workflow.WithActivityOptions(ctx, activityPolicy{Timeout: 30 * time.Second, Heartbeat: stepHeartbeatTimeout, RetryInterval: 10 * time.Second}.options())
		if err := workflow.ExecuteActivity(release, ingestionBatchReleaseActivity, completed).Get(release, nil); err != nil {
			return err
		}
		in = remaining
	}
	return nil
}

func registerIngestionBatches(w worker.Registry, steps Steps, pins Pinner) {
	w.RegisterWorkflowWithOptions(ingestionBatch, workflow.RegisterOptions{Name: ingestionBatchWorkflow})
	w.RegisterActivityWithOptions(func(ctx context.Context, in content.DispatchBatch) (ingestionBatchResult, error) {
		if batches, ok := steps.(interface {
			BeginIngestionBatch(context.Context) (context.Context, func())
		}); ok {
			var closeBatch func()
			ctx, closeBatch = batches.BeginIngestionBatch(ctx)
			defer closeBatch()
		}
		var completed []int
		if activity.HasHeartbeatDetails(ctx) {
			if err := activity.GetHeartbeatDetails(ctx, &completed); err != nil {
				return ingestionBatchResult{}, err
			}
		}
		var mu sync.Mutex
		done := make([]bool, len(in.Receipts))
		for _, i := range completed {
			if i >= 0 && i < len(done) {
				done[i] = true
			}
		}
		beat := func() { mu.Lock(); defer mu.Unlock(); activity.RecordHeartbeat(ctx, append([]int(nil), completed...)) }
		beat()
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			ticker := time.NewTicker(stepHeartbeatTimeout / 3)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					beat()
				case <-finished:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
		stage1 := make(chan struct{}, ingestionReceiptConcurrency)
		enrich := make(chan struct{}, ingestionEnrichConcurrency)
		var wg sync.WaitGroup
		var failures []error
	admit:
		for i := range done {
			if done[i] {
				continue
			}
			select {
			case stage1 <- struct{}{}:
			case <-ctx.Done():
				break admit
			}
			// select picks at random when cancellation and a free slot are
			// both ready; a cancelled batch admits no further receipt.
			if ctx.Err() != nil {
				<-stage1
				break
			}
			wg.Go(func() {
				lanes := receiptLanes{retrievalReady: sync.OnceFunc(func() { <-stage1 }), enrich: enrich}
				defer lanes.retrievalReady()
				err := processBatchedReceipt(ctx, steps, pins, in.Receipts[i], lanes)
				mu.Lock()
				if err == nil {
					done[i] = true
					completed = append(completed, i)
				} else {
					failures = append(failures, err)
				}
				mu.Unlock()
				beat()
			})
		}
		wg.Wait()
		if len(completed) > 0 && ctx.Err() == nil {
			result := ingestionBatchResult{}
			for i, complete := range done {
				if !complete {
					result.Remaining = append(result.Remaining, i)
				}
			}
			return result, nil
		}
		return ingestionBatchResult{}, errors.Join(append(failures, ctx.Err())...)
	}, activity.RegisterOptions{Name: ingestionBatchActivity})
	w.RegisterActivityWithOptions(func(ctx context.Context, in content.DispatchBatch) error {
		return heartbeating(ctx, stepHeartbeatTimeout/3, func() error {
			for _, receipt := range in.Receipts {
				// Release is idempotent: a crash or dependency failure can retry
				// already released receipts without replaying their processing.
				if err := pins.Release(telemetry.Restore(ctx, receipt.TraceContext), workIngestion, receipt.Organization, receipt.ReceiptID); err != nil {
					return err
				}
			}
			return nil
		})
	}, activity.RegisterOptions{Name: ingestionBatchReleaseActivity})
}

// receiptLanes moves one receipt from Stage 1 to Stage 2.
type receiptLanes struct {
	retrievalReady func()        // frees the receipt's Stage-1 slot; idempotent
	enrich         chan struct{} // bounds concurrent Stage-2 enrichment
}

func processBatchedReceipt(ctx context.Context, steps Steps, pins Pinner, in content.Dispatch, lanes receiptLanes) error {
	return workqueue.Track(ctx, in.Organization, "ingestion", in.ReceiptID, in.ReceiptID, func(ctx context.Context) error { return processReceipt(ctx, steps, pins, in, lanes) })
}

// processReceipt runs both stages under one trace, pin and lease. A cancelled
// batch stops after the current step instead of starting another one.
func processReceipt(ctx context.Context, steps Steps, pins Pinner, in content.Dispatch, lanes receiptLanes) error {
	parent := trace.SpanContextFromContext(ctx)
	ctx, span := telemetry.Start(telemetry.Restore(ctx, in.TraceContext), "ingestion.receipt", trace.WithLinks(trace.Link{SpanContext: parent}))
	defer span.End()
	pinned, err := pins.Pin(ctx, workIngestion, in.Organization, in.ReceiptID)
	if err != nil {
		return err
	}
	for round := 0; ; round++ {
		attempt, cancel := context.WithTimeout(pinned, 30*time.Second)
		err = steps.Run(attempt, in.Organization, in.ReceiptID)
		cancel()
		if !errors.Is(err, content.ErrNormalizationPending) {
			break
		}
		if round == maxNormalizationRounds {
			return temporal.NewNonRetryableApplicationError("normalization recorded no outcome", "NormalizationRoundsExceeded", nil)
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		attempt, cancel = context.WithTimeout(pinned, normalizationActivityTimeout)
		err = steps.Normalize(attempt, in.Organization, in.ReceiptID)
		cancel()
		if err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	// The receipt is retrieval_ready: a sibling takes its Stage-1 slot while it
	// waits for an enrichment slot.
	lanes.retrievalReady()
	select {
	case lanes.enrich <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-lanes.enrich }()
	if err = ctx.Err(); err != nil {
		return err
	}
	attempt, cancel := context.WithTimeout(pinned, enrichmentActivityTimeout)
	err = steps.Enrich(attempt, in.Organization, in.ReceiptID)
	cancel()
	if err != nil {
		return err
	}
	// The activity records completion before the workflow releases this pin.
	// An interrupted activity may replay this receipt using the same plan.
	return nil
}
