package temporal

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

type connectorPins struct {
	t            *testing.T
	held         atomic.Bool
	releases     atomic.Int32
	failures     int32
	nonRetryable bool
}

func (p *connectorPins) Pin(ctx context.Context, kind, org, id string) (context.Context, error) {
	if kind != "connector_run" || org != "org_a" || id != "connector_1:1" {
		p.t.Errorf("pin identity=%s/%s/%s", kind, org, id)
	}
	p.held.Store(true)
	return ctx, nil
}
func (p *connectorPins) Release(ctx context.Context, kind, org, id string) error {
	if err := ctx.Err(); err != nil {
		p.t.Errorf("cleanup context canceled: %v", err)
		return err
	}
	if kind != "connector_run" || org != "org_a" || id != "connector_1:1" {
		p.t.Errorf("release identity=%s/%s/%s", kind, org, id)
	}
	attempt := p.releases.Add(1)
	if attempt <= p.failures {
		if attempt == 1 && p.nonRetryable {
			return sdktemporal.NewNonRetryableApplicationError("release rejected", "release", nil)
		}
		if attempt == 2 {
			return sdktemporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_HEARTBEAT, nil)
		}
		return errors.New("release unavailable")
	}
	p.held.Store(false)
	return nil
}

type connectorRunStore struct {
	load func(context.Context) (connectors.Target, error)
}

func (s connectorRunStore) LoadRun(ctx context.Context, _, _ string) (connectors.Target, error) {
	return s.load(ctx)
}
func (connectorRunStore) CommitCheckpoint(context.Context, string, string, int64, connectors.Progress) (bool, error) {
	panic("unexpected checkpoint")
}
func (connectorRunStore) FinishRun(context.Context, string, string, int64, *connectors.RunError) error {
	panic("unexpected finish")
}

// This workflow owner covers terminal pin cleanup. Worker loss returns typed
// SDK timeout results, skipping the dead activity's return/cleanup code. Retry
// timers use virtual time; running-activity expiry itself is an SDK contract.
func TestConnectorWorkflowReleasesTerminalPin(t *testing.T) {
	for _, tc := range []struct {
		name                                                           string
		workerLoss, storeFailure, cancelBefore, cancelCleanup, pending bool
		releaseFailures                                                int32
		nonRetryableRelease                                            bool
		wantAttempts                                                   int32
	}{
		{name: "final worker loss", workerLoss: true, wantAttempts: 3},
		{name: "non-retryable release preserves acquisition error", workerLoss: true, releaseFailures: 1, nonRetryableRelease: true, wantAttempts: 3},
		{name: "non-retryable release preserves cancellation", cancelCleanup: true, releaseFailures: 1, nonRetryableRelease: true, wantAttempts: 1},
		{name: "non-retryable release delays success", releaseFailures: 1, nonRetryableRelease: true, wantAttempts: 1},
		{name: "final store failure", storeFailure: true, wantAttempts: 3},
		{name: "success", wantAttempts: 1},
		{name: "retain pin while acquisition pending", pending: true, wantAttempts: 1},
		{name: "cleanup retries and worker loss", releaseFailures: 4, wantAttempts: 1},
		{name: "failure survives cleanup retries", workerLoss: true, releaseFailures: 4, wantAttempts: 3},
		{name: "cancel before acquisition", cancelBefore: true},
		{name: "cancel during cleanup retries", cancelCleanup: true, releaseFailures: 4, wantAttempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			pins := &connectorPins{t: t, failures: tc.releaseFailures, nonRetryable: tc.nonRetryableRelease}
			// Snapshot of a run pinned by a previous worker, before the terminal attempt.
			pins.held.Store(true)
			var attempts atomic.Int32
			store := connectorRunStore{load: func(context.Context) (connectors.Target, error) {
				attempts.Add(1)
				if !pins.held.Load() {
					t.Error("pin released under acquisition")
				}
				if tc.storeFailure {
					return connectors.Target{}, errors.New("store unavailable")
				}
				return connectors.Target{}, nil // disabled instance ends acquisition
			}}
			registerConnectors(env, &Connectors{Acquirer: connectors.Acquirer{Store: store}}, pins)
			if tc.workerLoss || tc.pending {
				// Temporal delivers the loss result; the dead worker never calls Release.
				call := env.OnActivity(acquirePinnedActivity, mock.Anything, mock.Anything).Return(func(context.Context, AcquireInput) error {
					attempts.Add(1)
					if !pins.held.Load() {
						t.Error("pin released between retries")
					}
					if tc.workerLoss {
						return sdktemporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_HEARTBEAT, nil)
					}
					return nil
				})
				if tc.pending {
					call.After(time.Minute)
					env.RegisterDelayedCallback(func() {
						if !pins.held.Load() || pins.releases.Load() != 0 {
							t.Error("released before pending acquisition completed")
						}
					}, 30*time.Second)
				}
			}
			if tc.cancelBefore {
				env.RegisterDelayedCallback(env.CancelWorkflow, 0)
			}
			if tc.cancelCleanup {
				env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
					if info.ActivityType.Name == releaseConnectorPlanActivity && pins.releases.Load() == 0 {
						env.CancelWorkflow()
					}
				})
			}
			env.ExecuteWorkflow(acquireWorkflowFn, AcquireInput{Organization: "org_a", ConnectorID: "connector_1", Run: 1})
			err := env.GetWorkflowError()
			switch {
			case tc.cancelBefore || tc.cancelCleanup:
				if !sdktemporal.IsCanceledError(err) {
					t.Fatalf("terminal error=%v, want cancellation", err)
				}
			case tc.workerLoss:
				if !sdktemporal.IsTimeoutError(err) {
					t.Fatalf("terminal error=%v, want worker-loss timeout", err)
				}
			case tc.storeFailure:
				var app *sdktemporal.ApplicationError
				if !errors.As(err, &app) || app.Message() != "store unavailable" {
					t.Fatalf("terminal error=%v, want original store failure", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if attempts.Load() != tc.wantAttempts || pins.releases.Load() != tc.releaseFailures+1 || pins.held.Load() {
				t.Fatalf("acquisitions=%d releases=%d held=%t, want %d,%d,false", attempts.Load(), pins.releases.Load(), pins.held.Load(), tc.wantAttempts, tc.releaseFailures+1)
			}
			env.AssertExpectations(t)
		})
	}
}

// Existing scheduled activities retain activity-owned cleanup on upgrade.
// Workflow replay does not execute activity code, so this uses the old path.
func TestConnectorLegacyActivityStillReleasesItsPin(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			pins := &connectorPins{t: t}
			store := connectorRunStore{load: func(context.Context) (connectors.Target, error) {
				if failure {
					return connectors.Target{}, errors.New("store unavailable")
				}
				return connectors.Target{}, nil
			}}
			registerConnectors(env, &Connectors{Acquirer: connectors.Acquirer{Store: store}}, pins)
			env.OnGetVersion("connector-terminal-cleanup", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
			env.ExecuteWorkflow(acquireWorkflowFn, AcquireInput{Organization: "org_a", ConnectorID: "connector_1", Run: 1})
			if (env.GetWorkflowError() != nil) != failure || pins.releases.Load() != 1 || pins.held.Load() {
				t.Fatalf("error=%v releases=%d held=%t", env.GetWorkflowError(), pins.releases.Load(), pins.held.Load())
			}
		})
	}
}
