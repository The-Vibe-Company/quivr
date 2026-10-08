package temporal

import (
	"context"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
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

func (s *dispatchStore) ClaimIngestionBatches(ctx context.Context, _ int) ([]content.DispatchBatch, error) {
	if s.onClaim != nil {
		s.onClaim(ctx)
	}
	out := make([]content.DispatchBatch, len(s.batch))
	for i, d := range s.batch {
		out[i] = testDispatchBatch(d)
	}
	return out, nil
}
func (s *dispatchStore) IngestionBatchDispatched(_ context.Context, id string) error {
	if s.acknowledgementError != nil {
		err := s.acknowledgementError
		s.acknowledgementError = nil
		return err
	}
	for _, d := range s.batch {
		if testDispatchBatch(d).ID == id {
			s.acknowledged = append(s.acknowledged, d)
		}
	}
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
	ctx := telemetry.Extract(logging.WithRequestID(context.Background(), "caller-request-123"), http.Header{"Traceparent": []string{"00-11111111111111111111111111111111-2222222222222222-01"}})
	d := content.Dispatch{Organization: "org_a", ReceiptID: "receipt_1", TraceContext: telemetry.Encode(ctx)}
	propagated := mock.MatchedBy(func(ctx context.Context) bool {
		c := telemetry.Capture(ctx)
		return c.Traceparent == "00-11111111111111111111111111111111-2222222222222222-01" && c.RequestID == "caller-request-123"
	})
	store := &dispatchStore{batch: []content.Dispatch{d}, acknowledgementError: errors.New("connection lost after workflow start")}
	input := testDispatchBatch(d)
	tc := &mocks.Client{}
	options := mock.MatchedBy(func(o client.StartWorkflowOptions) bool {
		return o.ID == content.StableID("ingestion-batch-v1", d.Organization, d.ReceiptID) && o.WorkflowIDReusePolicy == enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE && o.TaskQueue == workqueue.TaskQueue(workqueue.Live)
	})
	tc.On("ExecuteWorkflow", propagated, options, ingestionBatchWorkflow, input).Return(nil, nil).Once()
	r := Runtime{Queues: workqueue.Config{Queues: []string{workqueue.Live}}, Client: tc, Store: store}
	r.dispatchBatch(context.Background(), r.ingestionIntents(), 1)
	if len(store.acknowledged) != 0 || len(store.feedback) != 1 {
		t.Fatalf("lost acknowledgement: ack=%v feedback=%v", store.acknowledged, store.feedback)
	}
	// Temporal rejects even a completed execution's ID. The stable batch ID
	// makes that rejection a successful durable dispatch.
	tc.On("ExecuteWorkflow", propagated, options, ingestionBatchWorkflow, input).Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{Message: "already completed"}).Once()
	restarted := Runtime{Client: tc, Store: store}
	restarted.dispatchBatch(context.Background(), restarted.ingestionIntents(), 1)
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
			tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, ingestionBatchWorkflow, testDispatchBatch(first)).Return(nil, errors.New("start unavailable")).Run(func(mock.Arguments) {
				if stop {
					cancel()
				}
			}).Once()
			if !stop {
				tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, ingestionBatchWorkflow, testDispatchBatch(second)).Return(nil, nil).Once()
			}
			r := Runtime{Queues: workqueue.Config{Queues: []string{workqueue.Live}}, Client: tc, Store: store}
			r.dispatchBatch(ctx, r.ingestionIntents(), 1)
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
			wantID, name, queue := "", rebuildWorkflowName, "quivr-bulk-v1"
			switch kind {
			case operations.KindProjectionRebuild:
				wantID = "projection-rebuild-v1_a9ee87bc3d2099318ff511e1fd4eb6e347ee25f5fea95a71b325c37374b457f9"
			case operations.KindBackfill:
				name = backfillWorkflowName
				wantID = "backfill-v1_a9ee87bc3d2099318ff511e1fd4eb6e347ee25f5fea95a71b325c37374b457f9"
			case operations.KindQuarantineReprocess:
				name = reprocessWorkflowName
				wantID = "quarantine-reprocess-v1_a9ee87bc3d2099318ff511e1fd4eb6e347ee25f5fea95a71b325c37374b457f9"
			}
			options := mock.MatchedBy(func(o client.StartWorkflowOptions) bool {
				return o.ID == wantID && o.TaskQueue == queue && o.WorkflowIDReusePolicy == enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE
			})
			in := RebuildInput{Organization: d.Organization, OperationID: d.OperationID}
			tc.On("ExecuteWorkflow", mock.Anything, options, name, in).Return(nil, nil).Once()
			r := Runtime{Queues: workqueue.Config{Queues: []string{workqueue.Live}}, Client: tc, Store: store}
			r.dispatchBatch(context.Background(), r.operationIntents(), 1)
			if len(store.operationsAcknowledged) != 0 {
				t.Fatal("lost acknowledgement was recorded")
			}
			tc.On("ExecuteWorkflow", mock.Anything, options, name, in).Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{Message: "completed"}).Once()
			restarted := Runtime{Client: tc, Store: store}
			restarted.dispatchBatch(context.Background(), restarted.operationIntents(), 1)
			if len(store.operationsAcknowledged) != 1 || store.operationsAcknowledged[0] != d {
				t.Fatalf("acknowledged %+v", store.operationsAcknowledged)
			}
			tc.AssertExpectations(t)
		})
	}
}

func TestConnectorDispatchRetriesFailedStartsUnderTheSameRun(t *testing.T) {
	run := connectors.ConnectorRun{Organization: "org_a", ConnectorID: "connector_1", Run: 7, WorkQueue: workqueue.Bulk}
	s := &schedulerFake{runs: []connectors.ConnectorRun{run}}
	tc := &mocks.Client{}
	options := mock.MatchedBy(func(o client.StartWorkflowOptions) bool {
		return o.ID == "connector:org_a:connector_1:7" && o.TaskQueue == workqueue.TaskQueue(workqueue.Bulk) && o.WorkflowIDReusePolicy == enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY
	})
	in := AcquireInput{Organization: run.Organization, ConnectorID: run.ConnectorID, Run: run.Run, WorkQueue: run.WorkQueue}
	tc.On("ExecuteWorkflow", mock.Anything, options, acquireWorkflow, in).Return(nil, errors.New("start unavailable")).Once()
	r := Runtime{Client: tc, Connectors: &Connectors{Scheduler: s}}
	r.dispatchBatch(context.Background(), r.connectorIntents(), 1)
	if len(s.released) != 1 || s.released[0] != run {
		t.Fatalf("released %+v", s.released)
	}
	tc.On("ExecuteWorkflow", mock.Anything, options, acquireWorkflow, in).Return(nil, nil).Once()
	r.dispatchBatch(context.Background(), r.connectorIntents(), 1)
	tc.On("ExecuteWorkflow", mock.Anything, options, acquireWorkflow, in).Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{Message: "active or completed"}).Once()
	r.dispatchBatch(context.Background(), r.connectorIntents(), 1)
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
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, ingestionBatchWorkflow, testDispatchBatch(receipt)).Return(nil, nil).Run(func(mock.Arguments) { cancel() }).Once()
	r := Runtime{Queues: workqueue.Config{Queues: []string{workqueue.Live}}, Client: tc, Store: store, Connectors: &Connectors{Scheduler: s}}
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

// A burst fills the receipt start slots while each accepted start is blocked.
// No receipt beyond the bound may start, and all work must be acknowledged
// before shutdown. Dependency channels control progress without clock waits.
func TestReceiptDispatchStartsABoundedBurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	starts := make(chan struct{}, 32)
	release := make(chan struct{})
	acks := make(chan struct{}, 32)
	var active, peak atomic.Int32
	store := &burstDispatchStore{acks: acks}
	tc := &burstStartClient{start: func(ctx context.Context) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		starts <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	r := Runtime{Queues: workqueue.Config{Queues: []string{workqueue.Live}}, Client: tc, Store: store}
	done := make(chan struct{})
	go func() { r.dispatch(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 8; i++ {
		select {
		case <-starts:
		case <-time.After(time.Second):
			t.Fatalf("only %d starts ran while peers were blocked; want 8 receipt start slots", i)
		}
	}
	select {
	case <-starts:
		t.Fatal("receipt dispatch exceeded 8 in-flight starts")
	default:
	}
	close(release)
	for i := 0; i < 32; i++ {
		select {
		case <-acks:
		case <-time.After(time.Second):
			t.Fatalf("acknowledged %d/32 burst receipts", i)
		}
	}
	if n := peak.Load(); n != 8 {
		t.Fatalf("peak starts %d, want bounded capacity 8", n)
	}
}

type burstStartClient struct {
	client.Client
	start func(context.Context) error
}

func (c *burstStartClient) ExecuteWorkflow(ctx context.Context, _ client.StartWorkflowOptions, _ interface{}, _ ...interface{}) (client.WorkflowRun, error) {
	return nil, c.start(ctx)
}

type burstDispatchStore struct {
	dispatchStore
	claimed atomic.Bool
	acks    chan struct{}
}

func (s *burstDispatchStore) ClaimIngestionBatches(context.Context, int) ([]content.DispatchBatch, error) {
	if s.claimed.Swap(true) {
		return nil, nil
	}
	out := make([]content.DispatchBatch, 32)
	for i := range out {
		out[i] = testDispatchBatch(content.Dispatch{Organization: "org_a", ReceiptID: fmt.Sprint(i)})
	}
	return out, nil
}

func (s *burstDispatchStore) IngestionBatchDispatched(context.Context, string) error {
	s.acks <- struct{}{}
	return nil
}

func testDispatchBatch(d content.Dispatch) content.DispatchBatch {
	return content.DispatchBatch{ID: content.StableID("ingestion-batch-v1", d.Organization, d.ReceiptID), WorkQueue: workqueue.Live, Receipts: []content.Dispatch{d}}
}

// The dispatcher owns start routing and workflow identity: each job starts on
// its class's queue, and a lost acknowledgement retains the same workflow ID
// and payload.
type servingDispatchStore struct {
	processing.ServingProjectionStore
	job          content.IngestionEvaluation
	acknowledged int
}

func (s *servingDispatchStore) ClaimServingProjections(context.Context, int) ([]content.IngestionEvaluation, error) {
	return []content.IngestionEvaluation{s.job}, nil
}
func (s *servingDispatchStore) ServingProjectionDispatched(context.Context, content.IngestionEvaluation) error {
	s.acknowledged++
	return nil
}
func TestServingDispatchRoutesJobsToTheirQueueAndPreservesIdentity(t *testing.T) {
	for _, class := range []string{"live", "bulk"} {
		t.Run("class_"+class, func(t *testing.T) {
			store := &servingDispatchStore{job: content.IngestionEvaluation{Organization: "org", ID: "job", WorkQueue: class}}
			queue := "quivr-live-v1"
			if class == "bulk" {
				queue = "quivr-bulk-v1"
			}
			c := &mocks.Client{}
			options := mock.MatchedBy(func(o client.StartWorkflowOptions) bool {
				return o.ID == content.StableID("serving-projection-workflow", "org", "job") && o.TaskQueue == queue && o.WorkflowIDReusePolicy == enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE
			})
			c.On("ExecuteWorkflow", mock.Anything, options, "publish-serving-projection-v0", Input{Organization: "org", ReceiptID: "job"}).Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{}).Once()
			runtime := Runtime{Client: c, Evaluation: &processing.Evaluator{Serving: store}}
			runtime.dispatchBatch(context.Background(), runtime.servingProjectionIntents(), 1)
			if store.acknowledged != 1 {
				t.Fatalf("existing serving workflow acknowledgement=%d, want1", store.acknowledged)
			}
			c.AssertExpectations(t)
		})
	}
}
