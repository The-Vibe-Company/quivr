package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// fakeSteps answers the successive Runs from process (true: normalization
// pending) and counts the steps. Each step first waits, within a deadline, for
// its attempt's first heartbeat and records the steps that never sent one.
type fakeSteps struct {
	process                    []bool
	runs, normalized, enriched int
	heard                      func(ctx context.Context) bool
	unheard                    []string
	normalizeErr               error
}

func (f *fakeSteps) step(ctx context.Context) {
	if !f.heard(ctx) {
		f.unheard = append(f.unheard, activity.GetInfo(ctx).ActivityType.Name)
	}
}

func (f *fakeSteps) Run(ctx context.Context, _, _ string) error {
	f.step(ctx)
	pending := f.process[min(f.runs, len(f.process)-1)]
	f.runs++
	if pending {
		return content.ErrNormalizationPending
	}
	return nil
}

func (f *fakeSteps) Normalize(ctx context.Context, _, _ string) error {
	f.step(ctx)
	f.normalized++
	return f.normalizeErr
}

func (f *fakeSteps) Enrich(ctx context.Context, _, _ string) error {
	f.step(ctx)
	f.enriched++
	return nil
}

// ingestionEnvironment registers the real workflow and activities over fake
// steps, and records the heartbeat timeout each step was scheduled with.
func ingestionEnvironment(t *testing.T, process ...bool) (*testsuite.TestWorkflowEnvironment, *fakeSteps, map[string]time.Duration) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var mu sync.Mutex
	beats := map[string]chan struct{}{}
	beat := func(id string) chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		if beats[id] == nil {
			beats[id] = make(chan struct{})
		}
		return beats[id]
	}
	env.SetOnActivityHeartbeatListener(func(info *activity.Info, _ converter.EncodedValues) {
		select {
		case <-beat(info.ActivityID):
		default:
			close(beat(info.ActivityID))
		}
	})
	timeouts := map[string]time.Duration{}
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		timeouts[info.ActivityType.Name] = info.HeartbeatTimeout
	})
	steps := &fakeSteps{process: process, heard: func(ctx context.Context) bool {
		select {
		case <-beat(activity.GetInfo(ctx).ActivityID):
			return true
		case <-time.After(time.Second):
			return false
		}
	}}
	env.RegisterWorkflowWithOptions(materializeWorkflow, workflow.RegisterOptions{Name: "process-e5-v3"})
	registerIngestion(env, steps, unpinned{})
	return env, steps, timeouts
}

func ingestionRun(t *testing.T, process ...bool) (*fakeSteps, map[string]time.Duration, error) {
	t.Helper()
	env, steps, timeouts := ingestionEnvironment(t, process...)
	env.ExecuteWorkflow("process-e5-v3", Input{Organization: "org_a", ReceiptID: "receipt_1"})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	return steps, timeouts, env.GetWorkflowError()
}

// Normalization owns the no-failed-outcome contract in
// TestUnavailablePluginNeverQuarantines. Here the real workflow must keep
// retrying that error without advancing to publication or enrichment.
func TestUnavailableNormalizerKeepsWorkflowPendingBeyondRetryInterval(t *testing.T) {
	env, steps, _ := ingestionEnvironment(t, true, false)
	steps.normalizeErr = errors.New("plugin_unavailable: connection refused")
	observed := false
	env.RegisterDelayedCallback(func() {
		observed = true
		if env.IsWorkflowCompleted() || steps.normalized <= 5 || steps.runs != 1 || steps.enriched != 0 {
			t.Errorf("after two simulated minutes: completed %t, normalization attempts %d, publication runs %d, enrichments %d", env.IsWorkflowCompleted(), steps.normalized, steps.runs, steps.enriched)
		}
		env.CancelWorkflow()
	}, 2*time.Minute)
	env.ExecuteWorkflow("process-e5-v3", Input{Organization: "org_a", ReceiptID: "receipt_1"})
	if !observed {
		t.Fatal("workflow stopped before the simulated outage exceeded the 30s retry interval")
	}
	if err := env.GetWorkflowError(); !sdktemporal.IsCanceledError(err) {
		t.Fatalf("workflow ended with %v, want cancellation after observing the pending outage", err)
	}
}

func TestContentWithoutNormalizationRunsNoNormalizationActivity(t *testing.T) {
	s, _, err := ingestionRun(t, false)
	if err != nil || s.normalized != 0 || s.runs != 1 || s.enriched != 1 {
		t.Fatalf("normalized %d processed %d enriched %d err %v", s.normalized, s.runs, s.enriched, err)
	}
}

func TestRoutedBlobsNormalizeOnceBeforePublication(t *testing.T) {
	s, _, err := ingestionRun(t, true, false)
	if err != nil || s.normalized != 1 || s.runs != 2 || s.enriched != 1 {
		t.Fatalf("normalized %d processed %d enriched %d err %v", s.normalized, s.runs, s.enriched, err)
	}
}

func TestNormalizationWithoutAnOutcomeStopsAfterBoundedRounds(t *testing.T) {
	s, _, err := ingestionRun(t, true)
	if err == nil || s.normalized != maxNormalizationRounds || s.enriched != 0 {
		t.Fatalf("normalized %d enriched %d err %v", s.normalized, s.enriched, err)
	}
}

// A step whose worker died is retried once its heartbeat timeout passes, so
// work pinned to a draining plugin version is freed within seconds (THE-835):
// every step must heartbeat while it runs, under a timeout of about 10 s.
func TestEveryStepIsRetriedWithinSecondsWhenItsWorkerDies(t *testing.T) {
	s, timeouts, err := ingestionRun(t, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.unheard) > 0 {
		t.Errorf("steps that never heartbeat while running: %v", s.unheard)
	}
	for _, name := range []string{"process-token-windows-v2", "normalize-external", "enrich-e5"} {
		if d, ok := timeouts[name]; !ok || d <= 0 || d > 10*time.Second {
			t.Errorf("%s scheduled with heartbeat timeout %v (ran %t), want at most 10s", name, d, ok)
		}
	}
}
