package temporal

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"
)

// dispatchStore records acknowledgements and can interrupt one after the
// durable workflow start. Queue ordering and leases are owned by PostgreSQL tests.
type dispatchStore struct {
	operations             []operations.Dispatch
	operationsAcknowledged []operations.Dispatch
	onClaim                func(context.Context)
	batch                  []content.Dispatch
	acknowledgementError   error
	acknowledged           []content.Dispatch
	feedback               []content.Dispatch
}

func (s *dispatchStore) Claim(ctx context.Context, _ int) ([]content.Dispatch, error) {
	if s.onClaim != nil {
		s.onClaim(ctx)
	}
	return s.batch, nil
}
func (s *dispatchStore) Dispatched(_ context.Context, d content.Dispatch) error {
	if s.acknowledgementError != nil {
		err := s.acknowledgementError
		s.acknowledgementError = nil
		return err
	}
	s.acknowledged = append(s.acknowledged, d)
	return nil
}
func (s *dispatchStore) Progress(_ context.Context, org, id, state, code string) error {
	s.feedback = append(s.feedback, content.Dispatch{Organization: org, ReceiptID: id})
	return nil
}
func (s *dispatchStore) ClaimOperations(context.Context, int) ([]operations.Dispatch, error) {
	return s.operations, nil
}
func (s *dispatchStore) OperationDispatched(_ context.Context, d operations.Dispatch) error {
	if s.acknowledgementError != nil {
		err := s.acknowledgementError
		s.acknowledgementError = nil
		return err
	}
	s.operationsAcknowledged = append(s.operationsAcknowledged, d)
	return nil
}

// A restart after Temporal accepted a workflow but before PostgreSQL saw the
// acknowledgement must reuse that workflow, including when it already completed.
func TestIngestionDispatchRestartAcknowledgesExistingWorkflow(t *testing.T) {
	d := content.Dispatch{Organization: "org_a", ReceiptID: "receipt_1"}
	store := &dispatchStore{batch: []content.Dispatch{d}, acknowledgementError: errors.New("connection lost after workflow start")}
	tc := &mocks.Client{}
	options := mock.MatchedBy(func(o client.StartWorkflowOptions) bool {
		return o.ID == "ingestion-e5-v4_f1c908eb4bb786fcba81e0904e1af852ab851dce29ef594561e9e37ca080d782" && o.WorkflowIDReusePolicy == enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE && o.TaskQueue == taskQueue
	})
	tc.On("ExecuteWorkflow", mock.Anything, options, "process-e5-v3", Input{Organization: d.Organization, ReceiptID: d.ReceiptID}).Return(nil, nil).Once()
	r := Runtime{Client: tc, Store: store}
	r.dispatchBatch(context.Background(), r.ingestionIntents())
	if len(store.acknowledged) != 0 || len(store.feedback) != 1 {
		t.Fatalf("lost acknowledgement: ack=%v feedback=%v", store.acknowledged, store.feedback)
	}
	// Replace the dispatcher, as a worker restart does. Temporal rejects reuse of
	// the completed workflow ID; that is a successful durable dispatch.
	tc.On("ExecuteWorkflow", mock.Anything, options, "process-e5-v3", Input{Organization: d.Organization, ReceiptID: d.ReceiptID}).Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{Message: "already completed"}).Once()
	restarted := Runtime{Client: tc, Store: store}
	restarted.dispatchBatch(context.Background(), restarted.ingestionIntents())
	if len(store.acknowledged) != 1 || store.acknowledged[0] != d {
		t.Fatalf("restart acknowledgement: got %v, want %v", store.acknowledged, d)
	}
	tc.AssertExpectations(t)
}

// A failed workflow start must keep its intent, without holding up other
// receipts in the batch. An expired attempt leaves the rest to lease recovery.
func TestIngestionDispatchFailureKeepsUnstartedWorkRecoverable(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "deadline"}[stop], func(t *testing.T) {
			first := content.Dispatch{Organization: "org_a", ReceiptID: "first"}
			second := content.Dispatch{Organization: "org_a", ReceiptID: "second"}
			store := &dispatchStore{batch: []content.Dispatch{first, second}}
			tc := &mocks.Client{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, "process-e5-v3", Input{Organization: first.Organization, ReceiptID: first.ReceiptID}).Return(nil, errors.New("start unavailable")).Run(func(mock.Arguments) {
				if stop {
					cancel()
				}
			}).Once()
			if !stop {
				tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, "process-e5-v3", Input{Organization: second.Organization, ReceiptID: second.ReceiptID}).Return(nil, nil).Once()
			}
			r := Runtime{Client: tc, Store: store}
			r.dispatchBatch(ctx, r.ingestionIntents())
			want := 0
			if !stop {
				want = 1
			}
			if len(store.acknowledged) != want || (want == 1 && store.acknowledged[0] != second) {
				t.Fatalf("acknowledged %v, want only successfully started work", store.acknowledged)
			}
			tc.AssertExpectations(t)
		})
	}
}

type schedulerFake struct {
	runs     []connectors.ConnectorRun
	released []connectors.ConnectorRun
	claim    func(context.Context)
}

func (s *schedulerFake) ClaimConnectorRuns(ctx context.Context, _ time.Duration, _ int) ([]connectors.ConnectorRun, error) {
	if s.claim != nil {
		s.claim(ctx)
	}
	return s.runs, ctx.Err()
}
func (s *schedulerFake) ReleaseConnectorRun(_ context.Context, r connectors.ConnectorRun) error {
	s.released = append(s.released, r)
	return nil
}

func TestBackgroundDispatchKeepsLegacyOperationIdentities(t *testing.T) {
	for _, kind := range []string{operations.KindProjectionRebuild, operations.KindBackfill, operations.KindQuarantineReprocess} {
		t.Run(kind, func(t *testing.T) {
			d := operations.Dispatch{Organization: "org_a", OperationID: "operation_1", Kind: kind}
			store := &dispatchStore{operations: []operations.Dispatch{d}, acknowledgementError: errors.New("acknowledgement lost")}
			tc := &mocks.Client{}
			wantID, name, queue := "", rebuildWorkflowName, taskQueue
			switch kind {
			case operations.KindProjectionRebuild:
				wantID = "projection-rebuild-v1_a9ee87bc3d2099318ff511e1fd4eb6e347ee25f5fea95a71b325c37374b457f9"
			case operations.KindBackfill:
				name, queue = backfillWorkflowName, backfillTaskQueue
				wantID = "backfill-v1_a9ee87bc3d2099318ff511e1fd4eb6e347ee25f5fea95a71b325c37374b457f9"
			case operations.KindQuarantineReprocess:
				name, queue = reprocessWorkflowName, backfillTaskQueue
				wantID = "quarantine-reprocess-v1_a9ee87bc3d2099318ff511e1fd4eb6e347ee25f5fea95a71b325c37374b457f9"
			}
			options := mock.MatchedBy(func(o client.StartWorkflowOptions) bool {
				return o.ID == wantID && o.TaskQueue == queue && o.WorkflowIDReusePolicy == enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE
			})
			in := RebuildInput{Organization: d.Organization, OperationID: d.OperationID}
			tc.On("ExecuteWorkflow", mock.Anything, options, name, in).Return(nil, nil).Once()
			r := Runtime{Client: tc, Store: store}
			r.dispatchBatch(context.Background(), r.operationIntents())
			if len(store.operationsAcknowledged) != 0 {
				t.Fatal("lost acknowledgement was recorded")
			}
			tc.On("ExecuteWorkflow", mock.Anything, options, name, in).Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{Message: "completed"}).Once()
			restarted := Runtime{Client: tc, Store: store}
			restarted.dispatchBatch(context.Background(), restarted.operationIntents())
			if len(store.operationsAcknowledged) != 1 || store.operationsAcknowledged[0] != d {
				t.Fatalf("acknowledged %+v", store.operationsAcknowledged)
			}
			tc.AssertExpectations(t)
		})
	}
}

func TestConnectorDispatchRetriesFailedStartsUnderTheSameRun(t *testing.T) {
	run := connectors.ConnectorRun{Organization: "org_a", ConnectorID: "connector_1", Run: 7}
	s := &schedulerFake{runs: []connectors.ConnectorRun{run}}
	tc := &mocks.Client{}
	options := mock.MatchedBy(func(o client.StartWorkflowOptions) bool {
		return o.ID == "connector:org_a:connector_1:7" && o.TaskQueue == connectorTaskQueue && o.WorkflowIDReusePolicy == enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY
	})
	in := AcquireInput{Organization: run.Organization, ConnectorID: run.ConnectorID, Run: run.Run}
	tc.On("ExecuteWorkflow", mock.Anything, options, acquireWorkflow, in).Return(nil, errors.New("start unavailable")).Once()
	r := Runtime{Client: tc, Connectors: &Connectors{Scheduler: s}}
	r.dispatchBatch(context.Background(), r.connectorIntents())
	if len(s.released) != 1 || s.released[0] != run {
		t.Fatalf("released %+v", s.released)
	}
	tc.On("ExecuteWorkflow", mock.Anything, options, acquireWorkflow, in).Return(nil, nil).Once()
	r.dispatchBatch(context.Background(), r.connectorIntents())
	tc.On("ExecuteWorkflow", mock.Anything, options, acquireWorkflow, in).Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{Message: "active or completed"}).Once()
	r.dispatchBatch(context.Background(), r.connectorIntents())
	if len(s.released) != 1 {
		t.Fatal("released an accepted/duplicate workflow run")
	}
	tc.AssertExpectations(t)
}

func TestBackgroundDispatcherDoesNotBlockReceiptsBehindConnectors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := make(chan struct{})
	s := &schedulerFake{claim: func(ctx context.Context) { close(blocked); <-ctx.Done() }}
	receipt := content.Dispatch{Organization: "org_a", ReceiptID: "receipt_1"}
	store := &dispatchStore{batch: []content.Dispatch{receipt}, onClaim: func(ctx context.Context) {
		select {
		case <-blocked:
		case <-ctx.Done():
		}
	}}
	tc := &mocks.Client{}
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, "process-e5-v3", Input{Organization: receipt.Organization, ReceiptID: receipt.ReceiptID}).Return(nil, nil).Run(func(mock.Arguments) { cancel() }).Once()
	r := Runtime{Client: tc, Store: store, Connectors: &Connectors{Scheduler: s}}
	done := make(chan struct{})
	go func() { r.dispatch(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stalled source blocked receipt dispatch or shutdown")
	}
	if len(store.acknowledged) != 1 {
		t.Fatalf("acknowledged %+v, want receipt despite stalled connector source", store.acknowledged)
	}
	tc.AssertExpectations(t)
}
