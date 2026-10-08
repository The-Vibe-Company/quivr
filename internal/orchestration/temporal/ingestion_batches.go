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

const ingestionBatchQueue = "quivr-ingestion-batches-v1"
const ingestionBatchWorkflow = "process-ingestion-batch-v1"
const ingestionBatchActivity = "process-receipt-batch-v1"
const ingestionBatchReleaseActivity = "release-receipt-batch-v1"
const ingestionReceiptConcurrency = 16

// Two waves of sixteen receipts cover a full batch. Four activities admit at
// most 64 receipts per worker; provider admission remains independently bounded.
// Each receipt retains the
// legacy stage deadlines; a silent worker is recovered by the heartbeat bound.
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
		pending := make(chan int, len(done))
		for i := range done {
			if !done[i] {
				pending <- i
			}
		}
		close(pending)
		var wg sync.WaitGroup
		var failures []error
		for range min(ingestionReceiptConcurrency, len(done)) {
			wg.Go(func() {
				for i := range pending {
					if ctx.Err() != nil {
						return
					}
					err := processBatchedReceipt(ctx, steps, pins, in.Receipts[i])
					mu.Lock()
					if err == nil {
						done[i] = true
						completed = append(completed, i)
					} else {
						failures = append(failures, err)
					}
					mu.Unlock()
					beat()
				}
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

func processBatchedReceipt(ctx context.Context, steps Steps, pins Pinner, in content.Dispatch) error {
	return workqueue.Track(ctx, in.Organization, "ingestion", in.ReceiptID, in.ReceiptID, func(ctx context.Context) error { return processReceipt(ctx, steps, pins, in) })
}

func processReceipt(ctx context.Context, steps Steps, pins Pinner, in content.Dispatch) error {
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
