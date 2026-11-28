package temporal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"go.temporal.io/sdk/client"
)

// settleStore behaves like PostgreSQL on a finished context: a write on an
// expired context fails with the context's error instead of persisting.
// Parallel dispatch lanes write concurrently, so counters are atomic. With
// cutAcks, each receipt's first acknowledgement in a trial runs until the
// attempt's deadline cuts it short, as a slow commit would.
type settleStore struct {
	dispatchStore
	acks, retries, refused, cut atomic.Int64
	cutAcks                     bool
	mu                          sync.Mutex
	cutIDs                      map[string]bool
}

func (s *settleStore) IngestionBatchDispatched(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		s.refused.Add(1)
		return err
	}
	if s.cutAcks && s.firstWrite(id) {
		<-ctx.Done()
		s.cut.Add(1)
		return ctx.Err()
	}
	s.acks.Add(1)
	return nil
}

// firstWrite alternates per receipt: the first acknowledgement is cut short,
// the next one persists and arms the following trial.
func (s *settleStore) firstWrite(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cutIDs[id] {
		delete(s.cutIDs, id)
		return false
	}
	if s.cutIDs == nil {
		s.cutIDs = map[string]bool{}
	}
	s.cutIDs[id] = true
	return true
}

func (s *settleStore) Progress(ctx context.Context, _, _, _, _ string) error {
	if err := ctx.Err(); err != nil {
		s.refused.Add(1)
		return err
	}
	s.retries.Add(1)
	return nil
}

type settleScheduler struct {
	schedulerFake
	released, refused atomic.Int64
}

func (s *settleScheduler) ReleaseConnectorRun(ctx context.Context, _ connectors.ConnectorRun) error {
	if err := ctx.Err(); err != nil {
		s.refused.Add(1)
		return err
	}
	s.released.Add(1)
	return nil
}

// Temporal answers once the dispatch attempt has expired, or at once when
// prompt: accepted returns the start as accepted; otherwise it fails with the
// attempt's deadline error, or an unavailable error on a live attempt.
type lateStartClient struct {
	client.Client
	accepted, prompt bool
	starts           *atomic.Int64
}

func (c lateStartClient) ExecuteWorkflow(ctx context.Context, _ client.StartWorkflowOptions, _ interface{}, _ ...interface{}) (client.WorkflowRun, error) {
	c.starts.Add(1)
	if !c.prompt {
		<-ctx.Done()
	}
	if c.accepted {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("start unavailable")
}

// Owns dispatch settlement when the 3 s attempt expires. Every start in flight
// answers exactly at the deadline, or its acknowledgement is cut short by it
// (synctest advances the fake clock), and each one's write must still reach
// the store: an expired start is retried, an accepted start is acknowledged
// and never reported as retrying. Parallel lanes fill all their start slots;
// unstarted intents stay leased.
func TestDispatchSettlesOnALiveContextAfterTheAttemptExpires(t *testing.T) {
	const trials = 1000
	receipts := make([]content.Dispatch, 2*ingestionStartConcurrency)
	for i := range receipts {
		receipts[i] = content.Dispatch{Organization: "org_a", ReceiptID: fmt.Sprint("receipt_", i)}
	}
	run := connectors.ConnectorRun{Organization: "org_a", ConnectorID: "connector_1", Run: 1, WorkQueue: workqueue.Bulk}
	for _, tc := range []struct {
		name                     string
		accepted, prompt, cutAck bool
		starts                   int
		connector                bool
	}{
		{name: "ingestion_expired_serial", starts: 1},
		{name: "ingestion_expired_parallel", starts: ingestionStartConcurrency},
		{name: "ingestion_accepted_late_parallel", accepted: true, starts: ingestionStartConcurrency},
		{name: "ingestion_acknowledgement_cut_serial", accepted: true, prompt: true, cutAck: true, starts: 1},
		{name: "ingestion_acknowledgement_cut_parallel", accepted: true, prompt: true, cutAck: true, starts: ingestionStartConcurrency},
		{name: "connector_expired", starts: 1, connector: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &settleStore{dispatchStore: dispatchStore{batch: receipts}, cutAcks: tc.cutAck}
				scheduler := &settleScheduler{schedulerFake: schedulerFake{runs: []connectors.ConnectorRun{run}}}
				var starts atomic.Int64
				r := Runtime{Client: lateStartClient{accepted: tc.accepted, prompt: tc.prompt, starts: &starts}, Store: store, Connectors: &Connectors{Scheduler: scheduler}}
				source := r.ingestionIntents(workqueue.Live)
				if tc.connector {
					source = r.connectorIntents(workqueue.Bulk)
				}
				for range trials {
					attempt, cancel := context.WithTimeout(context.Background(), dispatchAttempt)
					r.dispatchBatch(attempt, source, tc.starts)
					cancel()
				}
				settled := store.retries.Load() + scheduler.released.Load()
				if tc.accepted {
					settled = store.acks.Load()
				}
				refused := store.refused.Load() + scheduler.refused.Load()
				want := int64(trials * tc.starts)
				if got := starts.Load(); got != want || settled != want || refused != 0 {
					t.Fatalf("%d trials: %d starts (want %d), %d settled on a live context, %d writes ran on the expired attempt",
						trials, got, want, settled, refused)
				}
				if tc.accepted && store.retries.Load() != 0 {
					t.Fatalf("%d accepted starts reported as retrying/dispatch_unavailable", store.retries.Load())
				}
				if cut := store.cut.Load(); tc.cutAck && cut != want {
					t.Fatalf("%d acknowledgements cut by the deadline, want %d: the case did not exercise a cut write", cut, want)
				}
			})
		})
	}
}

type acceptingClient struct{ client.Client }

func (acceptingClient) ExecuteWorkflow(context.Context, client.StartWorkflowOptions, interface{}, ...interface{}) (client.WorkflowRun, error) {
	return nil, nil
}

// BenchmarkDispatchIntent measures the dispatcher's own cost for one accepted
// start: trace restoration, the start call and the acknowledgement.
func BenchmarkDispatchIntent(b *testing.B) {
	ctx := telemetry.Extract(context.Background(), http.Header{"Traceparent": []string{"00-11111111111111111111111111111111-2222222222222222-01"}})
	receipt := content.Dispatch{Organization: "org_a", ReceiptID: "receipt_1", TraceContext: telemetry.Encode(ctx)}
	store := &settleStore{dispatchStore: dispatchStore{batch: []content.Dispatch{receipt}}}
	r := Runtime{Client: acceptingClient{}, Store: store}
	intents, err := r.ingestionIntents(workqueue.Live).Claim(context.Background())
	if err != nil || len(intents) != 1 {
		b.Fatalf("claim: %v %d", err, len(intents))
	}
	attempt, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	b.ReportAllocs()
	for b.Loop() {
		r.dispatchIntent(attempt, intents[0])
	}
	if store.acks.Load() == 0 || store.refused.Load() != 0 {
		b.Fatalf("acks=%d refused=%d", store.acks.Load(), store.refused.Load())
	}
}
