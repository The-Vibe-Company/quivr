package pluginhttp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

func newFakeClock(start time.Time) (func() time.Time, func(time.Duration)) {
	var mu sync.Mutex
	now := start
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	return clock, advance
}

func testRetrieverPin(endpoint string) *plugins.Pin {
	m := plugins.Manifest{
		ID:      "test.retriever",
		Version: "1.0.0",
		Compatibility: plugins.Compatibility{
			Engine:    ">=0.1.0 <0.3.0",
			PluginAPI: ">=0.7.0 <0.14.0",
		},
		Contributions: plugins.Contributions{
			Retrieval: &plugins.Retrieval{
				Profiles: map[string]plugins.RetrievalProfile{
					"default": {MaxLatencyMS: 500, MaxCostCents: 1},
				},
				Limits: plugins.RetrievalLimits{
					MaxRounds:        3,
					MaxRequests:      4,
					MaxCandidates:    100,
					MaxResponseBytes: 1024,
				},
			},
		},
	}
	return &plugins.Pin{
		Manifest:       m,
		ManifestDigest: "sha256:" + strings.Repeat("b", 64),
		Endpoint:       endpoint,
		Configuration:  json.RawMessage(`{}`),
	}
}

func writeDiscovery(w http.ResponseWriter, pin *plugins.Pin) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"plugin_api":      pin.PluginAPI(),
		"plugin":          map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version},
		"manifest_digest": pin.ManifestDigest,
		"contributions":   pin.Manifest.Contributions.Names(),
	})
}

func TestBreakerStateMachine(t *testing.T) {
	clock, advance := newFakeClock(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	b := newCircuitBreaker(3, 5*time.Second, clock)

	// Failures below threshold followed by a success reset the failure streak.
	for range 2 {
		ok, probe := b.Allow()
		if !ok || probe != 0 {
			t.Fatalf("closed Allow = (%v, %v), want (true, 0)", ok, probe)
		}
		b.RecordFailure(probe)
	}
	b.RecordSuccess(0)
	if b.State() != breakerClosed || !b.fastClosed.Load() {
		t.Fatalf("state after reset = %v (fastClosed=%v), want closed+fastClosed", b.State(), b.fastClosed.Load())
	}

	// Non-failure peer response (e.g. valid 503 envelope or oversized 200) resets
	// the closed consecutive-failure streak so intermittent failures do not accumulate.
	for range 2 {
		ok, probe := b.Allow()
		if !ok || probe != 0 {
			t.Fatalf("closed Allow = (%v, %v), want (true, 0)", ok, probe)
		}
		b.RecordFailure(probe)
	}
	b.RecordPeerResponded(0)
	if b.State() != breakerClosed || !b.fastClosed.Load() {
		t.Fatalf("state after peer response reset = %v (fastClosed=%v), want closed+fastClosed", b.State(), b.fastClosed.Load())
	}

	// Reaching consecutive failure threshold opens the breaker.
	for range 3 {
		ok, probe := b.Allow()
		if !ok || probe != 0 {
			t.Fatalf("closed Allow = (%v, %v), want (true, 0)", ok, probe)
		}
		b.RecordFailure(probe)
	}
	if b.State() != breakerOpen {
		t.Fatalf("state after threshold failures = %v, want open", b.State())
	}

	// Calls before cooldown are rejected; stale non-probe success cannot close it.
	if ok, _ := b.Allow(); ok {
		t.Fatal("Allow succeeded while open before cooldown")
	}
	b.RecordSuccess(0)
	if b.State() != breakerOpen {
		t.Fatalf("stale non-probe success changed open state to %v", b.State())
	}

	// Advance past cooldown -> half-open admits exactly one probe.
	advance(5 * time.Second)
	ok, probe := b.Allow()
	if !ok || probe == 0 {
		t.Fatalf("first half-open Allow = (%v, %v), want (true, >0)", ok, probe)
	}
	if b.State() != breakerHalfOpen {
		t.Fatalf("state = %v, want half-open", b.State())
	}
	if ok2, probe2 := b.Allow(); ok2 || probe2 != 0 {
		t.Fatalf("concurrent half-open Allow = (%v, %v), want (false, 0)", ok2, probe2)
	}

	// If a probe stalls/panics past cooldown, a new probe is admitted and the
	// expired probe token can no longer mutate breaker state.
	staleProbe := probe
	advance(5 * time.Second)
	ok, probe = b.Allow()
	if !ok || probe == 0 || probe == staleProbe {
		t.Fatalf("re-admitted half-open Allow = (%v, %v), want fresh probe token", ok, probe)
	}
	b.RecordFailure(staleProbe)
	b.RecordSuccess(staleProbe)
	if b.State() != breakerHalfOpen {
		t.Fatalf("stale probe mutated state to %v, want half-open", b.State())
	}

	// Neutral release frees the probe slot without closing or re-opening cooldown.
	b.ReleaseProbe(probe)
	ok, probe = b.Allow()
	if !ok || probe == 0 {
		t.Fatalf("re-acquired half-open Allow = (%v, %v), want (true, >0)", ok, probe)
	}

	// Failed probe re-opens and restarts cooldown.
	b.RecordFailure(probe)
	if b.State() != breakerOpen {
		t.Fatalf("state after failed probe = %v, want open", b.State())
	}
	advance(2 * time.Second)
	if ok, _ := b.Allow(); ok {
		t.Fatal("Allow succeeded before restarted cooldown elapsed")
	}

	// After second cooldown, one healthy probe closes the breaker.
	advance(3 * time.Second)
	ok, probe = b.Allow()
	if !ok || probe == 0 {
		t.Fatalf("probe after second cooldown = (%v, %v), want (true, >0)", ok, probe)
	}
	b.RecordSuccess(probe)
	if b.State() != breakerClosed || !b.fastClosed.Load() {
		t.Fatalf("state=%v fastClosed=%v, want closed+fastClosed", b.State(), b.fastClosed.Load())
	}

	// Registry evicts retired idle closed endpoints when capacity is reached.
	reg := newBreakerRegistry(3, 5*time.Second, clock)
	reg.maxEntries = 4
	for i := range 6 {
		_ = reg.forEndpoint("http://plugin-" + string(rune('a'+i)) + ".test")
	}
	if got := reg.count.Load(); got > int64(reg.maxEntries) {
		t.Fatalf("registry count = %d, want <= %d after idle eviction", got, reg.maxEntries)
	}
}

func TestBreakerFastFailAndHalfOpenRecovery(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	endpoint := "http://" + addr
	pin := testRetrieverPin(endpoint)

	srv1 := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			writeDiscovery(w, pin)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requests":[{"primitive":"bm25","query_text":"harbour","k":10}]}`))
	}))
	srv1.Listener = lis
	srv1.Start()
	t.Cleanup(srv1.Close)

	clock, advance := newFakeClock(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	const cooldown = 5 * time.Second
	breaker := newCircuitBreaker(5, cooldown, clock)
	retriever := Retriever{Pin: pin, breaker: breaker}
	req := plugins.NewRetrievalSession(&pin.Manifest, benchFirstRequest()).Request()

	// Healthy round succeeds.
	if body, err := retriever.Round(context.Background(), req); err != nil || len(body) == 0 {
		t.Fatalf("initial healthy round = (%q, %v)", body, err)
	}

	// Stop server so endpoint refuses connections; threshold consecutive failures open breaker.
	srv1.Close()
	for i := range 5 {
		_, err := retriever.Round(context.Background(), req)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("failure %d err = %v, want ErrUnavailable", i+1, err)
		}
	}
	if breaker.State() != breakerOpen {
		t.Fatalf("breaker state = %v, want open", breaker.State())
	}

	// Already-canceled and expired contexts preserve context error identity.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := retriever.Round(canceledCtx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx err = %v, want context.Canceled", err)
	}
	expiredCtx, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	if _, err := retriever.Round(expiredCtx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired ctx err = %v, want context.DeadlineExceeded", err)
	}

	// 200 calls while open must fast-fail with ErrCircuitOpen / ErrUnavailable in <= 5 ms.
	const fastFailCount = 200
	latencies := make([]time.Duration, fastFailCount)
	for i := range fastFailCount {
		t0 := time.Now()
		_, err := retriever.Round(context.Background(), req)
		latencies[i] = time.Since(t0)
		if !errors.Is(err, ErrCircuitOpen) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("fast-fail %d err = %v, want ErrCircuitOpen/ErrUnavailable", i, err)
		}
	}
	slices.Sort(latencies)
	p50 := latencies[len(latencies)/2]
	p99 := latencies[int(float64(len(latencies)-1)*0.99)]
	t.Logf("breaker open fast-fail (n=%d): p50=%s p99=%s max=%s", fastFailCount, p50, p99, latencies[len(latencies)-1])
	if p50 > 5*time.Millisecond {
		t.Fatalf("breaker fast-fail p50 = %s, want <= 5ms", p50)
	}

	// Restart a healthy server on the same address with a probe gate to prove
	// half-open admits exactly one probe while rejecting concurrent callers.
	lis2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("re-listen %s: %v", addr, err)
	}
	entered := make(chan struct{}, 1)
	releaseProbe := make(chan struct{})
	var postHits atomic.Int32
	srv2 := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			writeDiscovery(w, pin)
			return
		}
		postHits.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-releaseProbe:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requests":[{"primitive":"bm25","query_text":"harbour","k":10}]}`))
	}))
	srv2.Listener = lis2
	srv2.Start()
	t.Cleanup(srv2.Close)

	// Advance fake clock past cooldown without wall-clock sleeps.
	advance(cooldown + time.Millisecond)

	probeDone := make(chan error, 1)
	go func() {
		_, err := retriever.Round(context.Background(), req)
		probeDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("half-open probe did not reach recovered server")
	}

	const concurrentDuringProbe = 16
	var rejected atomic.Int32
	var wg sync.WaitGroup
	wg.Add(concurrentDuringProbe)
	for range concurrentDuringProbe {
		go func() {
			defer wg.Done()
			_, err := retriever.Round(context.Background(), req)
			if errors.Is(err, ErrCircuitOpen) {
				rejected.Add(1)
			}
		}()
	}
	wg.Wait()
	if rejected.Load() != concurrentDuringProbe {
		t.Fatalf("half-open rejected %d/%d concurrent callers, want all rejected", rejected.Load(), concurrentDuringProbe)
	}
	if got := postHits.Load(); got != 1 {
		t.Fatalf("half-open admitted %d POSTs to peer, want exactly 1", got)
	}

	close(releaseProbe)
	if err := <-probeDone; err != nil {
		t.Fatalf("half-open probe failed: %v", err)
	}
	if breaker.State() != breakerClosed {
		t.Fatalf("breaker state after healthy probe = %v, want closed", breaker.State())
	}

	for i := range 5 {
		if body, err := retriever.Round(context.Background(), req); err != nil || len(body) == 0 {
			t.Fatalf("post-recovery round %d = (%q, %v)", i, body, err)
		}
	}
}

func TestBreakerErrorClassificationAndEndpointIsolation(t *testing.T) {
	t.Run("declared 4xx and 5xx envelopes and oversized responses never trip breaker", func(t *testing.T) {
		var mode atomic.Int32 // 0=422 envelope, 1=503 envelope, 2=oversized 200
		var posts atomic.Int32
		var pin *plugins.Pin
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/discovery" {
				writeDiscovery(w, pin)
				return
			}
			posts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			switch mode.Load() {
			case 0:
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"code":"unsupported","message":"query refused","retryable":false}`))
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"code":"busy","message":"try later","retryable":true}`))
			case 2:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("x", 2048) + `"}`))
			}
		}))
		defer srv.Close()

		pin = testRetrieverPin(srv.URL)
		clock, _ := newFakeClock(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
		breaker := newCircuitBreaker(3, 5*time.Second, clock)
		retriever := Retriever{Pin: pin, breaker: breaker}
		req := plugins.NewRetrievalSession(&pin.Manifest, benchFirstRequest()).Request()

		// 10 consecutive 422 declared envelopes -> never trips.
		mode.Store(0)
		for i := range 10 {
			_, err := retriever.Round(context.Background(), req)
			if !errors.Is(err, content.ErrInvalid) {
				t.Fatalf("422 call %d err = %v, want content.ErrInvalid", i, err)
			}
		}
		if breaker.State() != breakerClosed {
			t.Fatalf("breaker state after 422 envelopes = %v, want closed", breaker.State())
		}

		// 10 consecutive 503 declared envelopes -> never trips.
		mode.Store(1)
		for i := range 10 {
			_, err := retriever.Round(context.Background(), req)
			var pe *PluginError
			if !errors.As(err, &pe) || !pe.Retryable {
				t.Fatalf("503 envelope call %d err = %v, want retryable PluginError", i, err)
			}
		}
		if breaker.State() != breakerClosed {
			t.Fatalf("breaker state after 503 declared envelopes = %v, want closed", breaker.State())
		}

		// 10 consecutive oversized 200 responses -> never trips.
		mode.Store(2)
		for i := range 10 {
			_, err := retriever.Round(context.Background(), req)
			if !errors.Is(err, retrieval.ErrPluginInvalid) {
				t.Fatalf("oversized call %d err = %v, want retrieval.ErrPluginInvalid", i, err)
			}
		}
		if breaker.State() != breakerClosed {
			t.Fatalf("breaker state after oversized responses = %v, want closed", breaker.State())
		}
		if got := posts.Load(); got != 30 {
			t.Fatalf("peer received %d POSTs, want all 30 (breaker must not open)", got)
		}
	})

	t.Run("5xx without valid envelope trips breaker", func(t *testing.T) {
		var posts atomic.Int32
		var pin *plugins.Pin
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/discovery" {
				writeDiscovery(w, pin)
				return
			}
			posts.Add(1)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
		}))
		defer srv.Close()

		pin = testRetrieverPin(srv.URL)
		clock, _ := newFakeClock(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
		breaker := newCircuitBreaker(3, 5*time.Second, clock)
		retriever := Retriever{Pin: pin, breaker: breaker}
		req := plugins.NewRetrievalSession(&pin.Manifest, benchFirstRequest()).Request()

		for i := range 3 {
			_, err := retriever.Round(context.Background(), req)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("502 call %d err = %v, want ErrUnavailable", i, err)
			}
		}
		if breaker.State() != breakerOpen {
			t.Fatalf("breaker state after 502s without envelope = %v, want open", breaker.State())
		}
		_, err := retriever.Round(context.Background(), req)
		if !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("call after open = %v, want ErrCircuitOpen", err)
		}
		if got := posts.Load(); got != 3 {
			t.Fatalf("peer received %d POSTs, want 3 before breaker fast-fail", got)
		}
	})

	t.Run("transport timeouts trip breaker without sleeping", func(t *testing.T) {
		pin := testRetrieverPin("http://timeout-breaker.test")
		discoveryBody, err := json.Marshal(map[string]any{
			"plugin_api":      pin.PluginAPI(),
			"plugin":          map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version},
			"manifest_digest": pin.ManifestDigest,
			"contributions":   pin.Manifest.Contributions.Names(),
		})
		if err != nil {
			t.Fatal(err)
		}
		var posts atomic.Int32
		devhost.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/v0/discovery" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(discoveryBody)),
				}, nil
			}
			posts.Add(1)
			return nil, context.DeadlineExceeded
		}))
		t.Cleanup(func() { devhost.SetTransport(nil) })

		clock, _ := newFakeClock(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
		breaker := newCircuitBreaker(3, 5*time.Second, clock)
		retriever := Retriever{Pin: pin, breaker: breaker}
		req := plugins.NewRetrievalSession(&pin.Manifest, benchFirstRequest()).Request()

		for i := range 3 {
			_, err := retriever.Round(context.Background(), req)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("timeout call %d err = %v, want ErrUnavailable", i, err)
			}
		}
		if breaker.State() != breakerOpen {
			t.Fatalf("breaker state after timeouts = %v, want open", breaker.State())
		}
		if _, err := retriever.Round(context.Background(), req); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("post-open call err = %v, want ErrCircuitOpen", err)
		}
		if got := posts.Load(); got != 3 {
			t.Fatalf("peer received %d POSTs, want 3 before breaker fast-fail", got)
		}
	})

	t.Run("per-endpoint registry isolates endpoints", func(t *testing.T) {
		var pinB *plugins.Pin
		srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/discovery" {
				writeDiscovery(w, pinB)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"requests":[{"primitive":"bm25","query_text":"ok","k":5}]}`))
		}))
		defer srvB.Close()

		// Endpoint A is closed/refusing connections.
		deadSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		deadURL := deadSrv.URL
		deadSrv.Close()

		pinA := testRetrieverPin(deadURL)
		pinB = testRetrieverPin(srvB.URL)
		t.Cleanup(func() {
			defaultBreakers.delete(pinA.Endpoint)
			defaultBreakers.delete(pinB.Endpoint)
		})

		retA := Retriever{Pin: pinA}
		retB := Retriever{Pin: pinB}
		req := plugins.NewRetrievalSession(&pinA.Manifest, benchFirstRequest()).Request()

		// Drive endpoint A past default threshold (5).
		for range defaultBreakerThreshold {
			_, err := retA.Round(context.Background(), req)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("dead endpoint err = %v, want ErrUnavailable", err)
			}
		}
		if _, err := retA.Round(context.Background(), req); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("endpoint A err = %v, want ErrCircuitOpen", err)
		}
		// Endpoint B must remain completely unaffected.
		if body, err := retB.Round(context.Background(), req); err != nil || len(body) == 0 {
			t.Fatalf("endpoint B affected by endpoint A breaker: (%q, %v)", body, err)
		}
	})
}

func TestMarshalSearchRequestByteIdenticalAndHMAC(t *testing.T) {
	m := benchSearchManifest()

	// Golden multi-round check with HTML-escapable runes, rich metadata, and optional fields.
	session := plugins.NewRetrievalSession(m, benchFirstRequest())
	ord := 2
	alpha := 0.35
	cands1 := []plugins.Candidate{
		{
			SegmentID:       "seg-html-1",
			RecordID:        "rec-1",
			VersionID:       "ver-1",
			PartKey:         "body",
			Text:            "a < b & c > d \u2028 line \u2029 \x00\x08\x0c\t\r\n \"quoted\" \\slash\\ \xff café 日本語",
			Start:           0,
			End:             30,
			PassageText:     "passage <tag>",
			SourceRanges:    []plugins.SourceRange{{PartKey: "body", Start: 0, End: 10}, {PartKey: "body", Start: 12, End: 30}},
			SourceSeparator: "\n---\n",
			Region:          &plugins.ImageRegion{CoordinateSystem: "normalized", X: 0.1, Y: 0.2, Width: 0.7, Height: 0.7, Label: "chart <1>"},
			Score:           1e-7,
			Explanation:     "bm25 & dense",
			Title:           "Doc <1>",
			CommonMetadata:  json.RawMessage(`{ "k" : "a<b>c&d" , "n" : 1 , "sep" : "\u2028" }`),
			PassageOrdinal:  &ord,
			TotalPassages:   5,
			LeadPassage:     "lead & intro",
		},
		{
			SegmentID: "seg-float-edges",
			RecordID:  "rec-2",
			VersionID: "ver-2",
			PartKey:   "body",
			Text:      "edge floats",
			Score:     1e21,
		},
		{
			SegmentID: "seg-neg-zero",
			RecordID:  "rec-3",
			VersionID: "ver-3",
			PartKey:   "body",
			Text:      allBytesString(),
			Score:     0.0,
		},
	}
	req1 := []plugins.CandidateRequest{{
		Primitive:   plugins.PrimitiveHybrid,
		QueryText:   "a < b & c",
		Vector:      []float64{0.1, -0.25, 0.5},
		Space:       "engine.text@1",
		Field:       plugins.FieldSource,
		Alpha:       &alpha,
		Fusion:      plugins.FusionRelativeScore,
		K:           10,
		Filter:      &plugins.CandidateFilter{SourceNamespaces: []string{"ns/a", "ns/b"}},
		GroupBy:     plugins.GroupByRecord,
		IncludeLead: true,
	}}

	assertIdentical := func(t *testing.T, label string, req plugins.SearchRequest) []byte {
		t.Helper()
		want, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("%s json.Marshal: %v", label, err)
		}
		got, release, err := marshalSearchRequest(req)
		if err != nil {
			t.Fatalf("%s marshalSearchRequest: %v", label, err)
		}
		gotCopy := append([]byte(nil), got...)
		release()
		if !bytes.Equal(gotCopy, want) {
			t.Fatalf("%s wire bytes differ:\n got: %s\nwant: %s", label, gotCopy, want)
		}
		return gotCopy
	}

	// Round 1 (empty served), with and without Budget, and nil Served.
	r1 := session.Request()
	assertIdentical(t, "round1-with-budget", r1)
	r1NoBudget := r1
	r1NoBudget.Budget = nil
	assertIdentical(t, "round1-no-budget", r1NoBudget)
	r1NilServed := r1
	r1NilServed.Served = nil
	assertIdentical(t, "round1-nil-served", r1NilServed)

	// Round 2 (1 served entry -> populates cache) and Round 3 (2 served entries -> reuses cache).
	session.Serve("inv-r2", req1, [][]plugins.Candidate{cands1})
	r2 := session.Request()
	assertIdentical(t, "round2-with-budget", r2)
	r2NoBudget := r2
	r2NoBudget.Budget = nil
	assertIdentical(t, "round2-no-budget", r2NoBudget)

	session.Serve("inv-r3", req1, [][]plugins.Candidate{benchCandidates(2, 5)})
	r3 := session.Request()
	wireR3 := assertIdentical(t, "round3-cached-plus-new", r3)
	// Second marshal of round 3 hits cache for every entry.
	assertIdentical(t, "round3-all-cached", r3)

	// Deterministic randomized fuzz check over 50 multi-round shapes.
	rng := rand.New(rand.NewPCG(2585, 42))
	for i := range 50 {
		s := plugins.NewRetrievalSession(m, benchFirstRequest())
		if rng.IntN(2) == 0 {
			r := s.Request()
			r.Budget = nil
			assertIdentical(t, "fuzz-r1", r)
		}
		numRounds := 1 + rng.IntN(3)
		for round := range numRounds {
			nReqs := 1 + rng.IntN(2)
			reqs := make([]plugins.CandidateRequest, nReqs)
			cands := make([][]plugins.Candidate, nReqs)
			for j := range nReqs {
				k := rng.IntN(6)
				reqs[j] = plugins.CandidateRequest{Primitive: plugins.PrimitiveBM25, QueryText: "q & <test>", K: max(1, k)}
				if k > 0 {
					cands[j] = benchCandidates(round+1, k)
				}
			}
			s.Serve("inv-fuzz", reqs, cands)
			req := s.Request()
			if i%2 == 0 {
				req.Budget = nil
			} else {
				req.Budget = &plugins.SearchBudget{RemainingTimeMS: rng.IntN(5000), RemainingCostCents: rng.Float64() * 10}
			}
			assertIdentical(t, "fuzz-multiround", req)
		}
	}

	// Verify HMAC signature produced by plugins.SignHTTPRequest over wireR3 verifies cleanly.
	ring, err := plugins.NewSigningKeys()
	if err != nil {
		t.Fatal(err)
	}
	reqCtx := plugins.WithRequestSigning(context.Background(), m.ID, ring)
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://plugin.test"+plugins.SearchRoute, bytes.NewReader(wireR3))
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.SignHTTPRequest(httpReq, wireR3); err != nil {
		t.Fatalf("SignHTTPRequest: %v", err)
	}
	auth := httpReq.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("missing Bearer token: %q", auth)
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid JWT parts: %d", len(parts))
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(ring.Keys[0].Secret)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(wantSig)) {
		t.Fatalf("HMAC signature mismatch")
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		BodySHA256 string `json:"body_sha256"`
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	bodySum := sha256.Sum256(wireR3)
	if claims.BodySHA256 != hex.EncodeToString(bodySum[:]) {
		t.Fatalf("body_sha256 = %s, want %s", claims.BodySHA256, hex.EncodeToString(bodySum[:]))
	}

	// End-to-end check: Retriever.Round sends exact json.Marshal bytes to the peer.
	var received []byte
	var pin *plugins.Pin
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			writeDiscovery(w, pin)
			return
		}
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ranking":{"hits":[]}}`))
	}))
	defer srv.Close()
	pin = testRetrieverPin(srv.URL)
	t.Cleanup(func() { defaultBreakers.delete(pin.Endpoint) })

	if _, err := (Retriever{Pin: pin}).Round(context.Background(), r3); err != nil {
		t.Fatalf("Retriever.Round: %v", err)
	}
	if !bytes.Equal(received, wireR3) {
		t.Fatalf("wire body received by peer does not match json.Marshal(SearchRequest)")
	}
}

func allBytesString() string {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return string(b)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
