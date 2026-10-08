package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// fakeSteps reports normalization pending on every Run and counts the
// steps of one receipt.
type fakeSteps struct {
	normalized, enriched int
	normalizeErr         error
}

func (f *fakeSteps) Run(context.Context, string, string) error {
	return content.ErrNormalizationPending
}

func (f *fakeSteps) Normalize(context.Context, string, string) error {
	f.normalized++
	return f.normalizeErr
}

func (f *fakeSteps) Enrich(context.Context, string, string) error {
	f.enriched++
	return nil
}

// ingestionEnvironment registers the real batch workflow over fake steps, and
// records the heartbeat timeout each activity was scheduled with.
func ingestionEnvironment(t *testing.T) (*testsuite.TestWorkflowEnvironment, *fakeSteps, map[string]time.Duration) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	timeouts := map[string]time.Duration{}
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		timeouts[info.ActivityType.Name] = info.HeartbeatTimeout
	})
	steps := &fakeSteps{}
	registerIngestionBatches(env, steps, unpinned{})
	return env, steps, timeouts
}

var oneReceipt = content.DispatchBatch{ID: "batch", WorkQueue: workqueue.Live, Receipts: []content.Dispatch{{Organization: "org_a", ReceiptID: "receipt_1"}}}

// Normalization owns the no-failed-outcome contract in
// TestUnavailablePluginNeverQuarantines. Here the real workflow must keep
// retrying that error without advancing to enrichment. Its activity heartbeats
// under a timeout of about 10 s, so an attempt whose worker died is retried
// within seconds and work pinned to a draining plugin version is freed (THE-835).
func TestUnavailableNormalizerKeepsWorkflowPendingBeyondRetryInterval(t *testing.T) {
	env, steps, timeouts := ingestionEnvironment(t)
	var heartbeats atomic.Int64
	env.SetOnActivityHeartbeatListener(func(info *activity.Info, _ converter.EncodedValues) {
		if info.ActivityType.Name == ingestionBatchActivity {
			heartbeats.Add(1)
		}
	})
	steps.normalizeErr = errors.New("plugin_unavailable: connection refused")
	observed := false
	env.RegisterDelayedCallback(func() {
		observed = true
		if env.IsWorkflowCompleted() || steps.normalized <= 5 || steps.enriched != 0 {
			t.Errorf("after 45 simulated seconds: completed %t, normalization attempts %d, enrichments %d", env.IsWorkflowCompleted(), steps.normalized, steps.enriched)
		}
		env.CancelWorkflow()
	}, 45*time.Second)
	env.ExecuteWorkflow(ingestionBatchWorkflow, oneReceipt)
	if !observed {
		t.Fatalf("workflow stopped before the simulated outage exceeded the retry interval: %v", env.GetWorkflowError())
	}
	if err := env.GetWorkflowError(); !sdktemporal.IsCanceledError(err) {
		t.Fatalf("workflow ended with %v, want cancellation after observing the pending outage", err)
	}
	if heartbeats.Load() == 0 {
		t.Error("retrying batch activity never emitted a heartbeat")
	}
	if d, ok := timeouts[ingestionBatchActivity]; !ok || d <= 0 || d > 10*time.Second {
		t.Errorf("%s scheduled with heartbeat timeout %v (ran %t), want at most 10s", ingestionBatchActivity, d, ok)
	}
}
