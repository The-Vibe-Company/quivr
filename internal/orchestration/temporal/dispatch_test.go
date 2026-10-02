package temporal

import (
	"context"
	"errors"
	"testing"

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
	batch                []content.Dispatch
	acknowledgementError error
	acknowledged         []content.Dispatch
	feedback             []content.Dispatch
}

func (s *dispatchStore) Claim(context.Context, int) ([]content.Dispatch, error) { return s.batch, nil }
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
func (*dispatchStore) ClaimOperation(context.Context) (operations.Dispatch, error) {
	return operations.Dispatch{}, operations.ErrNoDispatch
}
func (*dispatchStore) OperationDispatched(context.Context, operations.Dispatch) error { return nil }

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
	r.dispatchIngestion(context.Background())
	if len(store.acknowledged) != 0 || len(store.feedback) != 1 {
		t.Fatalf("lost acknowledgement: ack=%v feedback=%v", store.acknowledged, store.feedback)
	}
	// Replace the dispatcher, as a worker restart does. Temporal rejects reuse of
	// the completed workflow ID; that is a successful durable dispatch.
	tc.On("ExecuteWorkflow", mock.Anything, options, "process-e5-v3", Input{Organization: d.Organization, ReceiptID: d.ReceiptID}).Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{Message: "already completed"}).Once()
	restarted := Runtime{Client: tc, Store: store}
	restarted.dispatchIngestion(context.Background())
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
			r.dispatchIngestion(ctx)
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
