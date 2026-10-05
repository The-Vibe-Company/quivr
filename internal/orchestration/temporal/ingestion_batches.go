package temporal

import (
	"context"
	"errors"
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
const ingestionReceiptConcurrency = 8

// Four waves of eight receipts cover a full batch. Each receipt retains the
// legacy stage deadlines; a silent worker is recovered by the heartbeat bound.
const ingestionBatchTimeout = 4 * (30*time.Second + maxNormalizationRounds*(normalizationActivityTimeout+30*time.Second) + enrichmentActivityTimeout)

func ingestionBatch(ctx workflow.Context, in content.DispatchBatch) error {
	ctx = workflow.WithActivityOptions(ctx, activityPolicy{Timeout: ingestionBatchTimeout, Heartbeat: stepHeartbeatTimeout, RetryInterval: 10 * time.Second}.options())
	if err := workflow.ExecuteActivity(ctx, ingestionBatchActivity, in).Get(ctx, nil); err != nil {
		return err
	}
	// Processing completion is durable before pins are released. A lost
	// heartbeat may replay a completed receipt while processing retries, so
	// even successful siblings keep their original plans until this boundary.
	release := workflow.WithActivityOptions(ctx, activityPolicy{Timeout: 30 * time.Second, Heartbeat: stepHeartbeatTimeout, RetryInterval: 10 * time.Second}.options())
	return workflow.ExecuteActivity(release, ingestionBatchReleaseActivity, in).Get(release, nil)
}

func registerIngestionBatches(w worker.Registry, steps Steps, pins Pinner) {
	w.RegisterWorkflowWithOptions(ingestionBatch, workflow.RegisterOptions{Name: ingestionBatchWorkflow})
	w.RegisterActivityWithOptions(func(ctx context.Context, in content.DispatchBatch) error {
		var completed []int
		if activity.HasHeartbeatDetails(ctx) {
			if err := activity.GetHeartbeatDetails(ctx, &completed); err != nil {
				return err
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
		return errors.Join(append(failures, ctx.Err())...)
	}, activity.RegisterOptions{Name: ingestionBatchActivity})
	w.RegisterActivityWithOptions(func(ctx context.Context, in content.DispatchBatch) error {
		return heartbeating(ctx, stepHeartbeatTimeout/3, func() error {
			for _, receipt := range in.Receipts {
				// Release is idempotent: a crash or dependency failure can retry
				// already released receipts without replaying their processing.
				if err := pins.Release(ctx, workIngestion, receipt.Organization, receipt.ReceiptID); err != nil {
					return err
				}
			}
			return nil
		})
	}, activity.RegisterOptions{Name: ingestionBatchReleaseActivity})
}

func processBatchedReceipt(ctx context.Context, steps Steps, pins Pinner, in content.Dispatch) error {
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
	// Pins remain held while any sibling retries, delaying plan draining until
	// the workflow records processing completion and releases the whole batch.
	return nil
}
