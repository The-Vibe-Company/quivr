package temporal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"net/http"
)

// The activity boundary owns bounded receipt execution, retry checkpoints and
// plan lifetime. Successful siblings release after durable partial completion;
// unfinished receipts and failed pin releases retry independently.
func TestIngestionBatchBoundsWorkAndRetriesOnlyUnfinishedReceipts(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	steps := &batchSteps{started: make(chan struct{}, 32), release: make(chan struct{}), runs: map[string]int{}, enriched: map[string]int{}, parents: map[string]string{}}
	pins := &batchPins{steps: steps, held: map[string]bool{}, released: map[string]int{}}
	registerIngestionBatches(env, steps, pins)
	in := content.DispatchBatch{ID: "batch"}
	for i := 0; i < 32; i++ {
		parent := telemetry.Extract(logging.WithRequestID(context.Background(), fmt.Sprint(i)), http.Header{"Traceparent": {fmt.Sprintf("00-%032x-2222222222222222-01", i+1)}})
		in.Receipts = append(in.Receipts, content.Dispatch{Organization: "org_a", ReceiptID: fmt.Sprint(i), TraceContext: telemetry.Encode(parent)})
	}
	finished := make(chan struct{})
	go func() { defer close(finished); env.ExecuteWorkflow(ingestionBatchWorkflow, in) }()
	for i := 0; i < 16; i++ {
		select {
		case <-steps.started:
		case <-time.After(time.Second):
			close(steps.release)
			<-finished
			t.Fatalf("only %d of sixteen receipt slots ran", i)
		}
	}
	select {
	case <-steps.started:
		t.Error("more than sixteen receipts ran while slots were occupied")
	default:
	}
	close(steps.release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("workflow did not finish after dependency release")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if peak := steps.peak.Load(); peak != 16 {
		t.Fatalf("peak receipt concurrency %d, want 16", peak)
	}
	for i := 0; i < 32; i++ {
		if got := steps.parents[fmt.Sprint(i)]; got != fmt.Sprint(i) {
			t.Fatalf("receipt %d lost its caller context: %q", i, got)
		}
		want := 1
		if i == 0 || i == 1 {
			want = 2
		}
		if got := steps.runs[fmt.Sprint(i)]; got != want {
			t.Fatalf("receipt %d processed %d times, want %d (normalization or unfinished retry)", i, got, want)
		}
		if got := steps.enriched[fmt.Sprint(i)]; got != 1 {
			t.Fatalf("receipt %d enriched successfully %d times, want 1", i, got)
		}
		wantReleases := 1
		if i == 1 {
			wantReleases = 2 // its completed-subset release failed once
		}
		if got := pins.released[fmt.Sprint(i)]; got != wantReleases {
			t.Fatalf("receipt %d release attempts %d, want %d", i, got, wantReleases)
		}
	}
	if len(pins.early) != 0 {
		t.Fatalf("pins released before every receipt enriched: %v", pins.early)
	}
	if len(pins.held) != 0 {
		t.Fatalf("pins still held after workflow completion: %v", pins.held)
	}
	if !pins.failed {
		t.Fatal("release retry was not exercised")
	}
	t.Run("poisoned receipt retains only its own pin", func(t *testing.T) {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		unblocked := make(chan struct{})
		close(unblocked)
		steps := &batchSteps{started: make(chan struct{}, 32), release: unblocked, runs: map[string]int{}, enriched: map[string]int{}, parents: map[string]string{}, poison: true, failed: true}
		pins := &batchPins{steps: steps, held: map[string]bool{}, released: map[string]int{}, failed: true}
		registerIngestionBatches(env, steps, pins)
		observed := false
		env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
			if info.ActivityType.Name != ingestionBatchActivity {
				return
			}
			var input content.DispatchBatch
			if err := args.Get(&input); err != nil {
				t.Fatal(err)
			}
			if info.Attempt == 1 && len(input.Receipts) == 32 {
				return
			}
			observed = true
			pins.mu.Lock()
			if len(pins.held) != 1 || !pins.held["0"] || len(pins.released) != 31 || len(pins.early) != 0 {
				t.Errorf("poison retry retained completed siblings: held=%v released=%v early=%v", pins.held, pins.released, pins.early)
			}
			pins.mu.Unlock()
			env.CancelWorkflow()
		})
		env.ExecuteWorkflow(ingestionBatchWorkflow, in)
		if !observed || !sdktemporal.IsCanceledError(env.GetWorkflowError()) {
			t.Fatalf("pending poison retry observation=%v workflow=%v", observed, env.GetWorkflowError())
		}
	})
}

type batchSteps struct {
	started      chan struct{}
	release      chan struct{}
	active, peak atomic.Int32
	mu           sync.Mutex
	runs         map[string]int
	parents      map[string]string
	enriched     map[string]int
	normalized   bool
	failed       bool
	poison       bool
}

func (s *batchSteps) Run(ctx context.Context, _, id string) error {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for old := s.peak.Load(); n > old && !s.peak.CompareAndSwap(old, n); old = s.peak.Load() {
	}
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[id]++
	s.parents[id] = logging.RequestID(ctx)
	if id == "0" && (s.poison || !s.normalized) {
		return content.ErrNormalizationPending
	}
	return nil
}
func (s *batchSteps) Normalize(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.normalized = true
	return nil
}
func (s *batchSteps) Enrich(_ context.Context, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "1" && !s.failed {
		s.failed = true
		return errors.New("transient vector write failure")
	}
	s.enriched[id]++
	return nil
}

// batchPins records the orchestration boundary, including a failed release
// after the first pin was released, so its retry must be idempotent.
type batchPins struct {
	steps    *batchSteps
	mu       sync.Mutex
	held     map[string]bool
	released map[string]int
	early    []string
	failed   bool
}

func (p *batchPins) Pin(ctx context.Context, _, _, id string) (context.Context, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held[id] = true
	return ctx, nil
}

func (p *batchPins) Release(_ context.Context, _, _, id string) error {
	p.steps.mu.Lock()
	completed := p.steps.enriched[id]
	p.steps.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released[id]++
	if completed == 0 {
		p.early = append(p.early, id)
	}
	if id == "1" && !p.failed {
		p.failed = true
		return errors.New("transient plan release failure")
	}
	delete(p.held, id)
	return nil
}
