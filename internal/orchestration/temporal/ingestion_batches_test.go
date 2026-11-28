package temporal

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	sdklog "go.temporal.io/sdk/log"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"net/http"
)

// The activity boundary owns bounded receipt execution, retry checkpoints and
// plan lifetime. Successful siblings release after durable partial completion;
// unfinished receipts and failed pin releases retry independently. Live
// batches, the class of direct submissions, share the ready-commit scope.
func TestIngestionBatchBoundsWorkAndRetriesOnlyUnfinishedReceipts(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	steps := &batchSteps{started: make(chan struct{}, 32), release: make(chan struct{}), runs: map[string]int{}, enriched: map[string]int{}, parents: map[string]string{}}
	pins := &batchPins{steps: steps, held: map[string]bool{}, released: map[string]int{}}
	registerIngestionBatches(env, steps, pins)
	in := content.DispatchBatch{ID: "batch", WorkQueue: workqueue.Live}
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
		if !steps.scoped[fmt.Sprint(i)] {
			t.Fatalf("receipt %d lost the batch commit scope while restoring its trace and pin", i)
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
	scoped       map[string]bool
}

type batchScopeKey struct{}

func (s *batchSteps) BeginIngestionBatch(ctx context.Context) (context.Context, func()) {
	return context.WithValue(ctx, batchScopeKey{}, s), func() {}
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
	if s.scoped == nil {
		s.scoped = map[string]bool{}
	}
	s.scoped[id] = ctx.Value(batchScopeKey{}) == s
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

const stageRunLatency = 10 * time.Millisecond

// Stage-2 enrichment is a hundred times slower than Stage-1 processing.
const stageEnrichLatency = 100 * stageRunLatency

// Slow enrichment must not stall ingestion: with Enrich a hundred times slower
// than Run, every receipt is retrieval_ready within the Stage-1 waves alone,
// enrichment stays inside its own lane bound and every receipt is enriched
// before its pin is released, with no goroutine left behind.
func TestStage1NotBlockedByStage2(t *testing.T) {
	const receipts = 64
	r := simulateStages(t, stageScenario{receipts: receipts})
	if len(r.steps.enriched) != receipts || len(r.steps.released) != receipts {
		t.Fatalf("enriched %d and released %d of %d receipts", len(r.steps.enriched), len(r.steps.released), receipts)
	}
	waves := time.Duration((receipts + ingestionReceiptConcurrency - 1) / ingestionReceiptConcurrency)
	if last := slices.Max(slices.Collect(maps.Values(r.steps.ready))); last > waves*stageRunLatency {
		t.Fatalf("last receipt retrieval_ready after %v, want within %d Stage-1 waves (%v): enrichment held Stage-1 slots", last, waves, waves*stageRunLatency)
	}
	if r.steps.peakEnrich > ingestionEnrichConcurrency {
		t.Fatalf("peak concurrent enrichment %d exceeds its bound %d", r.steps.peakEnrich, ingestionEnrichConcurrency)
	}
	t.Logf("stage1 p50=%v p95=%v end-to-end=%v peak enrich=%d", r.stage1P50, r.stage1P95, r.total, r.steps.peakEnrich)
}

// A worker killed at any point of a receipt converges: the batch completes,
// every receipt is enriched before its pin is released, a released receipt
// never replays a step, and receipt 0, recorded complete before the kill,
// keeps that checkpoint instead of being processed again.
func TestIngestionBatchConvergesAfterWorkerKill(t *testing.T) {
	for _, tc := range []struct {
		name string
		kill killPoint
	}{
		{"after run before enrich", killAfterRun},
		{"after enrich before heartbeat", killAfterEnrich},
		{"during release", killDuringRelease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := simulateStages(t, stageScenario{receipts: 4, kill: tc.kill})
			s := r.steps
			if !s.killed {
				t.Fatal("the worker was never killed")
			}
			if len(s.enriched) != 4 || len(s.released) != 4 {
				t.Fatalf("enriched %v released %v, want all four", s.enriched, s.released)
			}
			if s.runCalls["0"] != 1 || s.enrichCalls["0"] != 1 {
				t.Fatalf("receipt 0 completed before the kill but ran %d and enriched %d times", s.runCalls["0"], s.enrichCalls["0"])
			}
			if tc.kill == killAfterEnrich && s.enrichCalls["1"] != 2 {
				t.Fatalf("receipt 1 died before recording its enrichment but was enriched %d times, want a retry", s.enrichCalls["1"])
			}
		})
	}
}

// Cancelling a batch stops each lane within its current step: a step that
// still completes after the cancellation is not followed by another one.
func TestIngestionBatchCancellationStopsWithinOneStep(t *testing.T) {
	r := simulateStages(t, stageScenario{receipts: 32, cancelAt: stageRunLatency / 2})
	if r.steps.runCalls["0"] == 0 {
		t.Fatal("no step was running when the batch was cancelled")
	}
	if len(r.steps.enriched) != 0 {
		t.Fatalf("receipts enriched after cancellation: %v", r.steps.enriched)
	}
}

func BenchmarkIngestionBatchStages(b *testing.B) {
	const receipts = 64
	var r stageResult
	for b.Loop() {
		r = simulateStages(b, stageScenario{receipts: receipts})
	}
	b.ReportMetric(float64(r.stage1P50)/float64(time.Millisecond), "stage1_p50_ms")
	b.ReportMetric(float64(r.stage1P95)/float64(time.Millisecond), "stage1_p95_ms")
	b.ReportMetric(receipts/r.total.Seconds(), "docs/s")
	b.ReportMetric(float64(r.peakGoroutines), "peak_goroutines")
	b.ReportMetric(float64(r.steps.peakEnrich), "peak_enrich")
	b.ReportMetric(float64(r.payload)/receipts, "payload_B/receipt")
}

type stageScenario struct {
	receipts int
	kill     killPoint
	cancelAt time.Duration // cancels the activity context at this virtual time
}

type stageResult struct {
	steps                *stageSteps
	stage1P50, stage1P95 time.Duration // virtual time to retrieval_ready
	total                time.Duration // virtual time to batch completion
	peakGoroutines       int
	payload              int // activity input and result bytes
}

// simulateStages runs one batch workflow through the Temporal test suite with
// step latencies on a virtual clock.
func simulateStages(tb testing.TB, sc stageScenario) stageResult {
	tb.Helper()
	var suite testsuite.WorkflowTestSuite
	suite.SetLogger(sdklog.NewStructuredLogger(slog.New(slog.DiscardHandler)))
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Minute) // a stall guard; the clock is virtual
	clock := &virtualClock{}
	s := &stageSteps{clock: clock, kill: sc.kill, ready: map[string]time.Duration{}, enriched: map[string]bool{}, released: map[string]bool{}, runCalls: map[string]int{}, enrichCalls: map[string]int{}}
	if sc.kill != noKill {
		s.slowRun, s.fastEnrich = "1", "0"
	}
	if sc.cancelAt > 0 {
		background, cancel := context.WithCancel(context.Background())
		defer cancel()
		env.SetWorkerOptions(worker.Options{BackgroundActivityContext: background})
		clock.At(sc.cancelAt, cancel)
	}
	registerIngestionBatches(env, s, s)
	dc := converter.GetDefaultDataConverter()
	size := func(v any) int {
		p, err := dc.ToPayload(v)
		if err != nil {
			tb.Fatal(err)
		}
		return len(p.GetData())
	}
	payload := 0
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
		s.mu.Lock()
		s.dead = false // a new attempt runs on a live worker
		s.mu.Unlock()
		if info.ActivityType.Name != ingestionBatchActivity {
			return
		}
		if sc.cancelAt > 0 && info.Attempt > 1 {
			env.CancelWorkflow()
		}
		var in content.DispatchBatch
		if err := args.Get(&in); err != nil {
			tb.Error(err)
		}
		s.mu.Lock()
		payload += size(in)
		s.mu.Unlock()
	})
	env.SetOnActivityCompletedListener(func(info *activity.Info, result converter.EncodedValue, err error) {
		if info.ActivityType.Name != ingestionBatchActivity || err != nil {
			return
		}
		var out ingestionBatchResult
		if err := result.Get(&out); err != nil {
			tb.Error(err)
		}
		s.mu.Lock()
		payload += size(out)
		s.mu.Unlock()
	})
	in := content.DispatchBatch{ID: "batch", WorkQueue: workqueue.Live}
	for i := range sc.receipts {
		in.Receipts = append(in.Receipts, content.Dispatch{Organization: "org_a", ReceiptID: fmt.Sprint(i)})
	}
	done := make(chan struct{})
	go func() { defer close(done); env.ExecuteWorkflow(ingestionBatchWorkflow, in) }()
	clock.Drive(tb, done)
	if err := env.GetWorkflowError(); err != nil && sc.cancelAt == 0 {
		tb.Fatal(err)
	}
	for _, g := range otherGoroutines() {
		if strings.Contains(g, "orchestration/temporal.processReceipt") || strings.Contains(g, "orchestration/temporal.registerIngestionBatches") {
			tb.Fatalf("goroutine leaked after the batch completed:\n%s", g)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.violations) > 0 {
		tb.Fatalf("orchestration violations: %v", s.violations)
	}
	r := stageResult{steps: s, total: clock.Now(), peakGoroutines: clock.peakGoroutines, payload: payload}
	ready := slices.Sorted(maps.Values(s.ready))
	if len(ready) > 0 {
		percentile := func(p int) time.Duration { return ready[(len(ready)*p+99)/100-1] }
		r.stage1P50, r.stage1P95 = percentile(50), percentile(95)
	}
	return r
}

type killPoint int

const (
	noKill            killPoint = iota
	killAfterRun                // receipt 1 is retrieval_ready; its worker dies before Enrich
	killAfterEnrich             // receipt 1 is enriched; its worker dies before recording it
	killDuringRelease           // the worker dies while releasing completed receipts
)

var errWorkerKilled = errors.New("worker killed")

// stageSteps fakes Run and Enrich latencies on the virtual clock, and the
// pinner, so it observes what the orchestration does: when each receipt
// became retrieval_ready, concurrent enrichment, and any step started after
// cancellation or release. Enrichment is idempotent, as processing.Service's.
// A killed worker fails every step until the next attempt starts.
type stageSteps struct {
	clock                 *virtualClock
	kill                  killPoint
	slowRun, fastEnrich   string // receipts whose latencies order the kill matrix
	mu                    sync.Mutex
	ready                 map[string]time.Duration
	enriched, released    map[string]bool
	runCalls, enrichCalls map[string]int
	enriching, peakEnrich int
	dead, killed          bool
	violations            []string
}

// started records a step's start and reports whether its worker is alive.
func (s *stageSteps) started(ctx context.Context, step, id string) bool {
	if ctx.Err() != nil {
		s.violations = append(s.violations, step+" "+id+" started after cancellation")
	}
	if s.released[id] {
		s.violations = append(s.violations, step+" "+id+" started after its pin was released")
	}
	return !s.dead
}

func (s *stageSteps) Run(ctx context.Context, _, id string) error {
	s.mu.Lock()
	s.runCalls[id]++
	alive := s.started(ctx, "run", id)
	s.mu.Unlock()
	if !alive {
		return errWorkerKilled
	}
	latency := stageRunLatency
	if id == s.slowRun {
		latency *= 3
	}
	// A baseline write completes even when its caller stops waiting.
	_ = s.clock.Sleep(context.WithoutCancel(ctx), latency)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return errWorkerKilled
	}
	if _, ok := s.ready[id]; !ok {
		s.ready[id] = s.clock.Now()
	}
	return nil
}

func (s *stageSteps) Normalize(context.Context, string, string) error { return nil }

func (s *stageSteps) Enrich(ctx context.Context, _, id string) error {
	s.mu.Lock()
	s.enrichCalls[id]++
	alive := s.started(ctx, "enrich", id)
	if _, ok := s.ready[id]; !ok {
		s.violations = append(s.violations, "enrich "+id+" before retrieval_ready")
	}
	if alive && s.kill == killAfterRun && id == "1" && !s.killed {
		s.killed, s.dead, alive = true, true, false
	}
	if !alive {
		s.mu.Unlock()
		return errWorkerKilled
	}
	s.enriching++
	s.peakEnrich = max(s.peakEnrich, s.enriching)
	s.mu.Unlock()
	latency := stageEnrichLatency
	if id == s.fastEnrich {
		latency = stageRunLatency
	}
	err := s.clock.Sleep(ctx, latency)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enriching--
	if err != nil {
		return err
	}
	if s.dead {
		return errWorkerKilled
	}
	s.enriched[id] = true
	if s.kill == killAfterEnrich && id == "1" && !s.killed {
		s.killed, s.dead = true, true
		return errWorkerKilled
	}
	return nil
}

func (s *stageSteps) Pin(ctx context.Context, _, _, _ string) (context.Context, error) {
	return ctx, nil
}

func (s *stageSteps) Release(_ context.Context, _, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kill == killDuringRelease && !s.killed && len(s.released) > 0 {
		s.killed = true
		return errWorkerKilled
	}
	if !s.enriched[id] {
		s.violations = append(s.violations, "release "+id+" before enrichment")
	}
	s.released[id] = true
	return nil
}

// virtualClock is the fake clock of the receipt steps. It advances to the
// earliest pending wake-up only when every other goroutine is blocked, so
// step latencies are exact virtual durations and no test waits on the wall
// clock. testing/synctest offers the same within a *testing.T only; the
// benchmark needs it too.
type virtualClock struct {
	mu             sync.Mutex
	now            time.Duration
	seq            int
	events         []virtualEvent
	peakGoroutines int
}

type virtualEvent struct {
	at   time.Duration
	seq  int
	fire func()
}

func (c *virtualClock) Now() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// At runs fire once the clock reaches at.
func (c *virtualClock) At(at time.Duration, fire func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	c.events = append(c.events, virtualEvent{at: at, seq: c.seq, fire: fire})
}

// Sleep blocks for d of virtual time, or until ctx ends.
func (c *virtualClock) Sleep(ctx context.Context, d time.Duration) error {
	woke := make(chan struct{})
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.events = append(c.events, virtualEvent{at: c.now + d, seq: seq, fire: func() { close(woke) }})
	c.mu.Unlock()
	select {
	case <-woke:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		c.events = slices.DeleteFunc(c.events, func(e virtualEvent) bool { return e.seq == seq })
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Drive advances the clock until done is closed and every other goroutine is
// blocked again. It fails instead of hanging when nothing can advance.
func (c *virtualClock) Drive(tb testing.TB, done <-chan struct{}) {
	tb.Helper()
	stall := time.Now().Add(time.Minute)
	for {
		goroutines := otherGoroutines()
		if !allBlocked(goroutines) {
			runtime.Gosched()
			continue
		}
		c.mu.Lock()
		c.peakGoroutines = max(c.peakGoroutines, len(goroutines))
		select {
		case <-done:
			c.mu.Unlock()
			return
		default:
		}
		if len(c.events) == 0 {
			c.mu.Unlock()
			if time.Now().After(stall) {
				tb.Fatalf("virtual clock stalled at %v with no pending step:\n%s", c.Now(), strings.Join(goroutines, "\n\n"))
			}
			runtime.Gosched()
			continue
		}
		c.now = max(c.now, slices.MinFunc(c.events, func(a, b virtualEvent) int { return cmp.Compare(a.at, b.at) }).at)
		var due []virtualEvent
		c.events = slices.DeleteFunc(c.events, func(e virtualEvent) bool {
			if e.at <= c.now {
				due = append(due, e)
				return true
			}
			return false
		})
		c.mu.Unlock()
		slices.SortFunc(due, func(a, b virtualEvent) int { return cmp.Compare(a.seq, b.seq) })
		for _, e := range due {
			e.fire()
		}
	}
}

// otherGoroutines returns the stacks of every goroutine but the caller, from
// one stop-the-world snapshot.
func otherGoroutines() []string {
	buf := make([]byte, 256<<10)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			// The caller's own stack comes first.
			return strings.Split(string(buf[:n]), "\n\n")[1:]
		}
		buf = make([]byte, 2*len(buf))
	}
}

// allBlocked reports whether every goroutine waits on another one, so only
// the clock can make progress.
func allBlocked(goroutines []string) bool {
	for _, g := range goroutines {
		state, _, _ := strings.Cut(g[strings.IndexByte(g, '[')+1:], "]")
		state, _, _ = strings.Cut(state, ",")
		switch state {
		case "running", "runnable", "syscall", "preempted", "copystack", "GC assist wait", "GC assist marking":
			return false
		}
	}
	return true
}
