package temporal

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"
	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
)

// The workflow owns retry and timeout policy; domain checkpoints own completed
// work. Inject a worker-loss timeout and observe another attempt of the same input.
// Workflow timers skip retry delays; running-activity expiry is an SDK concern.
func TestBackgroundWorkflowRetriesInterruptedStep(t *testing.T) {
	for _, kind := range []string{"rebuild", "backfill", "reprocess", "connector"} {
		t.Run(kind, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			attempts := 0
			env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
				if info.HeartbeatTimeout != 10*time.Second {
					t.Errorf("heartbeat bound %v, want 10s", info.HeartbeatTimeout)
				}
				want := 30 * time.Minute
				if kind == "connector" {
					want = 5 * time.Minute
				}
				if info.StartToCloseTimeout != want {
					t.Errorf("attempt budget %v, want %v", info.StartToCloseTimeout, want)
				}
			})
			attempt := func() error {
				attempts++
				if attempts == 1 {
					return sdktemporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_HEARTBEAT, nil)
				}
				return nil
			}
			in := RebuildInput{Organization: "org_a", OperationID: "operation_1"}
			switch kind {
			case "rebuild":
				env.RegisterActivityWithOptions(func(context.Context, RebuildInput) (bool, error) { err := attempt(); return err == nil, err }, activity.RegisterOptions{Name: "rebuild-projection-step"})
				env.ExecuteWorkflow(rebuildWorkflow, in)
			case "backfill":
				env.RegisterActivityWithOptions(func(context.Context, RebuildInput) (backfill.Progress, error) {
					err := attempt()
					return backfill.Progress{Done: err == nil}, err
				}, activity.RegisterOptions{Name: "backfill-step"})
				env.ExecuteWorkflow(backfillWorkflow, in)
			case "reprocess":
				env.RegisterActivityWithOptions(func(context.Context, RebuildInput) (quarantine.Progress, error) {
					err := attempt()
					return quarantine.Progress{Done: err == nil}, err
				}, activity.RegisterOptions{Name: "quarantine-reprocess-step"})
				env.ExecuteWorkflow(reprocessWorkflow, in)
			case "connector":
				env.RegisterActivityWithOptions(func(context.Context, AcquireInput) error { return attempt() }, activity.RegisterOptions{Name: acquireActivity})
				env.ExecuteWorkflow(acquireWorkflowFn, AcquireInput{Organization: "org_a", ConnectorID: "connector_1", Run: 1})
			}
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 {
				t.Fatalf("attempts=%d, want 2", attempts)
			}
		})
	}
}

// Replay every legacy result codec, its unfinished-step timer, and a history
// rollover. Completed activity results come from history, never fake execution.
func TestBackgroundWorkflowReplaysLegacyHistories(t *testing.T) {
	files, err := filepath.Glob("testdata/legacy/*.json.gz")
	if err != nil || len(files) != 5 {
		t.Fatalf("legacy histories %v: %v", files, err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			z, err := gzip.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			defer z.Close()
			data, err := io.ReadAll(z)
			if err != nil {
				t.Fatal(err)
			}
			var history historypb.History
			if err = protojson.Unmarshal(data, &history); err != nil {
				t.Fatal(err)
			}
			replayer := worker.NewWorkflowReplayer()
			replayer.RegisterWorkflowWithOptions(rebuildWorkflow, workflow.RegisterOptions{Name: rebuildWorkflowName})
			replayer.RegisterWorkflowWithOptions(backfillWorkflow, workflow.RegisterOptions{Name: backfillWorkflowName})
			replayer.RegisterWorkflowWithOptions(reprocessWorkflow, workflow.RegisterOptions{Name: reprocessWorkflowName})
			replayer.RegisterWorkflowWithOptions(acquireWorkflowFn, workflow.RegisterOptions{Name: acquireWorkflow})
			if err = replayer.ReplayWorkflowHistory(nil, &history); err != nil {
				t.Fatal(err)
			}
			// A worker may die after an activity result/timer is durable but before
			// its next workflow task completes. Replay that unfinished task as well.
			for i := len(history.Events) - 1; i >= 0; i-- {
				if history.Events[i].EventType == enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED {
					prefix := historypb.History{Events: history.Events[:i+1]}
					if err = replayer.ReplayWorkflowHistory(nil, &prefix); err != nil {
						t.Fatalf("resume unfinished workflow task: %v", err)
					}
					break
				}
			}
		})
	}
}

// Technical outages must not invent terminal administrative failures. Connector
// acquisition keeps its existing three-attempt terminal budget.
func TestBackgroundWorkflowRetryBudgets(t *testing.T) {
	for _, connector := range []bool{false, true} {
		t.Run(fmt.Sprint(connector), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			attempts := 0
			if connector {
				env.RegisterActivityWithOptions(func(context.Context, AcquireInput) error { attempts++; return errors.New("store unavailable") }, activity.RegisterOptions{Name: acquireActivity})
				env.ExecuteWorkflow(acquireWorkflowFn, AcquireInput{Organization: "org_a", ConnectorID: "connector_1", Run: 1})
				if attempts != 3 || env.GetWorkflowError() == nil {
					t.Fatalf("attempts=%d error=%v, want terminal after 3", attempts, env.GetWorkflowError())
				}
			} else {
				env.RegisterActivityWithOptions(func(context.Context, RebuildInput) (backfill.Progress, error) {
					attempts++
					if attempts <= 6 {
						return backfill.Progress{}, errors.New("store unavailable")
					}
					return backfill.Progress{Done: true}, nil
				}, activity.RegisterOptions{Name: "backfill-step"})
				env.ExecuteWorkflow(backfillWorkflow, RebuildInput{Organization: "org_a", OperationID: "operation_1"})
				if attempts != 7 || env.GetWorkflowError() != nil {
					t.Fatalf("attempts=%d error=%v, want success after outage", attempts, env.GetWorkflowError())
				}
			}
		})
	}
}

func TestBackgroundWorkflowCancellation(t *testing.T) {
	for _, duringActivity := range []bool{false, true} {
		t.Run(fmt.Sprint(duringActivity), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			in := RebuildInput{Organization: "org_a", OperationID: "operation_1"}
			env.RegisterActivityWithOptions(func(context.Context, RebuildInput) (backfill.Progress, error) { return backfill.Progress{}, nil }, activity.RegisterOptions{Name: "backfill-step"})
			call := env.OnActivity("backfill-step", mock.Anything, in).Return(backfill.Progress{Wait: time.Hour}, nil).Once()
			if duringActivity {
				call.After(time.Hour)
			}
			env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
			env.ExecuteWorkflow(backfillWorkflow, in)
			if err := env.GetWorkflowError(); !sdktemporal.IsCanceledError(err) {
				t.Fatalf("cancellation error=%v", err)
			}
			env.AssertExpectations(t)
		})
	}
}

func TestBackgroundWorkflowContinuesWithStableIdentity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	calls := 0
	env.RegisterActivityWithOptions(func(context.Context, RebuildInput) (quarantine.Progress, error) {
		calls++
		return quarantine.Progress{}, nil
	}, activity.RegisterOptions{Name: "quarantine-reprocess-step"})
	in := RebuildInput{Organization: "org_a", OperationID: "operation_1"}
	env.ExecuteWorkflow(reprocessWorkflow, in)
	var continuation *workflow.ContinueAsNewError
	if !errors.As(env.GetWorkflowError(), &continuation) {
		t.Fatalf("continuation error=%v", env.GetWorkflowError())
	}
	var resumed RebuildInput
	if err := converter.GetDefaultDataConverter().FromPayloads(continuation.Input, &resumed); err != nil {
		t.Fatal(err)
	}
	if calls != 400 || continuation.WorkflowType.Name != reprocessWorkflowName || resumed != in {
		t.Fatalf("calls=%d continuation=%s input=%+v", calls, continuation.WorkflowType.Name, resumed)
	}
}

type operationPins struct {
	pins, releases int
}

func (p *operationPins) Pin(ctx context.Context, kind, org, id string) (context.Context, error) {
	p.pins++
	return context.WithValue(ctx, p, kind+":"+org+":"+id), nil
}
func (p *operationPins) Release(context.Context, string, string, string) error {
	p.releases++
	if p.releases == 1 {
		return errors.New("release unavailable")
	}
	return nil
}

// Pause and technical retries retain the plan. Only the final domain step may
// release it, and a failed release must retry rather than report completion.
func TestBackgroundStepKeepsItsPinUntilDurableCompletion(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	pins := &operationPins{}
	attempts := 0
	var beats atomic.Int32
	env.SetOnActivityHeartbeatListener(func(*activity.Info, converter.EncodedValues) { beats.Add(1) })
	registerOperationStep(env, "backfill-step", pins, func(ctx context.Context, in RebuildInput) (backfill.Progress, error) {
		attempts++
		if ctx.Value(pins) != "operation:org_a:operation_1" {
			return backfill.Progress{}, sdktemporal.NewNonRetryableApplicationError("lost plan pin", "pin", nil)
		}
		if attempts == 1 {
			return backfill.Progress{Wait: time.Hour}, nil
		}
		if attempts == 2 && pins.releases != 0 {
			t.Error("released a paused operation's plan")
		}
		return backfill.Progress{Done: true}, nil
	}, func(p backfill.Progress) bool { return p.Done })
	env.ExecuteWorkflow(backfillWorkflow, RebuildInput{Organization: "org_a", OperationID: "operation_1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || pins.pins != 3 || pins.releases != 2 {
		t.Fatalf("attempts=%d pins=%d releases=%d, want 3,3,2", attempts, pins.pins, pins.releases)
	}
	if beats.Load() < 3 {
		t.Fatalf("%d heartbeats for three running attempts", beats.Load())
	}
}
