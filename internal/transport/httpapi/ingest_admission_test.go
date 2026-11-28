package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type gatedAcceptancePort struct {
	acceptancePort
	onFirstAccept func()
	release       <-chan struct{}
	active        atomic.Int32
	peakActive    atomic.Int32
}

func (p *gatedAcceptancePort) Accept(ctx context.Context, scope corpus.Scope, c content.Command) (content.Receipt, error) {
	if p.onFirstAccept != nil && strings.HasSuffix(c.Key, "-0") {
		cur := p.active.Add(1)
		for {
			prev := p.peakActive.Load()
			if cur <= prev || p.peakActive.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer p.active.Add(-1)
		p.onFirstAccept()
		select {
		case <-p.release:
		case <-ctx.Done():
			return content.Receipt{}, ctx.Err()
		}
	}
	return content.Receipt{
		ID:          "receipt_" + c.Key,
		State:       "pending",
		RecordID:    "record_" + c.Source.RecordKey,
		Source:      c.Source,
		Processing:  content.Processing{State: "queued", Phase: "materialization"},
		Diagnostics: []content.Diagnostic{},
	}, nil
}

// Owns Keep/Discard Gate #2: 32 concurrent ~10 MiB batches stay within
// configured admission cap + 20% live heap, shed excess with HTTP 429 +
// Retry-After (validated against OpenAPI), and drain cleanly with 0 hangs.
func TestIngestAdmission32Concurrent10MiBBatchesBoundHeapAndShed429(t *testing.T) {
	const (
		totalBatches = 32
		capBytes     = 32 << 20 // 32 MiB generic in-flight cap -> at most 3 concurrent 10 MiB batches
		maxAllowed   = capBytes + capBytes/5
	)

	// Build one ~10 MiB valid batch payload (11 entries x ~920 KiB each, under
	// the 1 MiB per-entry bound and just under the 10 MiB batch bound).
	entries := make([]map[string]any, 11)
	chunk := strings.Repeat("x", 920<<10)
	for i := range entries {
		entries[i] = inline(fmt.Sprintf("big-%d", i), fmt.Sprintf("rec-%d", i), chunk)
	}
	payload, err := json.Marshal(map[string]any{"items": entries})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) < 9<<20 || len(payload) > 10<<20 {
		t.Fatalf("fixture payload=%d bytes; want between 9 MiB and 10 MiB", len(payload))
	}

	limiter := httpapi.NewIngestAdmission(httpapi.IngestAdmissionConfig{
		MaxConcurrent: totalBatches,
		MaxBytes:      capBytes,
		MaxQueue:      -1,
	})

	admittedReady := make(chan struct{}, totalBatches)
	releaseAdmitted := make(chan struct{})
	port := &gatedAcceptancePort{
		onFirstAccept: func() { admittedReady <- struct{}{} },
		release:       releaseAdmitted,
	}

	keys := map[string]corpus.Scope{
		adminKey: {Organization: "org_a", Actions: []string{"content:read", "content:write"}, Corpora: []string{"*"}},
	}
	contents := content.Service{Submissions: port, Receipts: port, RecordStore: port, Versions: port, Materialization: port, BlobSource: verifiedBlobs{}}
	rawHandler, err := httpapi.New(nil, contents, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithIngestAdmission(limiter))
	if err != nil {
		t.Fatal(err)
	}
	handler := checkedAPI(t, rawHandler)

	runtime.GC()
	var baseMem runtime.MemStats
	runtime.ReadMemStats(&baseMem)

	var (
		wg        sync.WaitGroup
		okCount   atomic.Int32
		shedCount atomic.Int32
		otherErr  atomic.Int32
	)
	for i := 0; i < totalBatches; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v0/records/batch", bytes.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+adminKey)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			switch rec.Code {
			case http.StatusOK:
				okCount.Add(1)
			case http.StatusTooManyRequests:
				retryAfter, convErr := strconv.Atoi(rec.Header().Get("Retry-After"))
				var body map[string]any
				if convErr != nil || retryAfter < 1 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["code"] != "rate_limited" || body["retryable"] != true {
					otherErr.Add(1)
					return
				}
				shedCount.Add(1)
			default:
				otherErr.Add(1)
			}
		}()
	}

	// Wait for the 3 admitted 10 MiB batches to reach active processing while
	// excess batches shed immediately with 429.
	wantAdmitted := int(capBytes / int64(len(payload)))
	for i := 0; i < wantAdmitted; i++ {
		select {
		case <-admittedReady:
		case <-time.After(10 * time.Second):
			close(releaseAdmitted)
			t.Fatalf("timed out waiting for admitted batch %d/%d", i+1, wantAdmitted)
		}
	}

	// Measure live heap delta at peak concurrency.
	runtime.GC()
	var peakMem runtime.MemStats
	runtime.ReadMemStats(&peakMem)
	var liveDelta int64
	if peakMem.HeapAlloc > baseMem.HeapAlloc {
		liveDelta = int64(peakMem.HeapAlloc - baseMem.HeapAlloc)
	}
	peakStats := limiter.Stats()

	close(releaseAdmitted)
	wg.Wait()

	if otherErr.Load() != 0 {
		t.Fatalf("unexpected responses: ok=%d 429=%d other=%d", okCount.Load(), shedCount.Load(), otherErr.Load())
	}
	if int(okCount.Load()) < wantAdmitted || shedCount.Load() == 0 {
		t.Fatalf("expected admitted >= %d and 429 sheds > 0; got ok=%d 429=%d", wantAdmitted, okCount.Load(), shedCount.Load())
	}
	if peakStats.InFlightBytes > capBytes {
		t.Fatalf("in-flight bytes %d exceeded cap %d", peakStats.InFlightBytes, capBytes)
	}
	if int(port.peakActive.Load()) > wantAdmitted {
		t.Fatalf("peak active batches %d exceeded byte-cap bound %d", port.peakActive.Load(), wantAdmitted)
	}
	if liveDelta > maxAllowed {
		t.Fatalf("peak live heap delta %d bytes (%.2f MiB) exceeded cap+20%% %d bytes (%.2f MiB)",
			liveDelta, float64(liveDelta)/(1<<20), maxAllowed, float64(maxAllowed)/(1<<20))
	}
	post := limiter.Stats()
	if post.InFlight != 0 || post.InFlightBytes != 0 || post.Queued != 0 {
		t.Fatalf("post-drain admission stats=%+v; want zero in-flight/bytes/queued", post)
	}
}

type abortingReader struct {
	prefix []byte
	offset int
	err    error
}

func (r *abortingReader) Read(p []byte) (int, error) {
	if r.offset < len(r.prefix) {
		n := copy(p, r.prefix[r.offset:])
		r.offset += n
		return n, nil
	}
	return 0, r.err
}

func (r *abortingReader) Close() error { return nil }

// Owns leak-freedom on client disconnect mid-body: 1000 aborted requests
// (both declared Content-Length and chunked unknown length) return all
// concurrency and byte admission tokens cleanly to 0.
func TestIngestAdmission1000AbortedMidBodyRequestsLeakZeroTokens(t *testing.T) {
	limiter := httpapi.NewIngestAdmission(httpapi.IngestAdmissionConfig{
		MaxConcurrent: 32,
		MaxBytes:      16 << 20,
		MaxQueue:      -1,
	})
	handler := newAPI(t, &acceptancePort{}, httpapi.WithIngestAdmission(limiter))

	prefix := []byte(`{"items":[{"idempotency_key":"abort","source":{"corpus_id":"corpus_news","namespace":"feed","record_key":"k"},"content":{"kind":"text","text":"` + strings.Repeat("a", 8192))

	const total = 1000
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := 0; i < total; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &abortingReader{prefix: prefix, err: io.ErrUnexpectedEOF}
			req := httptest.NewRequest(http.MethodPost, "/v0/records/batch", body).WithContext(ctx)
			if idx%2 == 0 {
				// Known Content-Length larger than bytes actually sent before disconnect.
				req.ContentLength = 64 << 10
			} else {
				// Chunked / unknown Content-Length aborting mid-stream.
				req.ContentLength = -1
			}
			req.Header.Set("Authorization", "Bearer "+adminKey)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusTooManyRequests {
				t.Errorf("abort #%d: unexpected status %d: %s", idx, rec.Code, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()

	stats := limiter.Stats()
	if stats.InFlight != 0 || stats.InFlightBytes != 0 || stats.Queued != 0 {
		t.Fatalf("after %d aborted mid-body requests, stats=%+v; want zero leaked tokens", total, stats)
	}
}

type panickingReader struct{}

func (panickingReader) Read([]byte) (int, error) { panic("injected body read panic") }
func (panickingReader) Close() error             { return nil }

// Owns panic-safety during body reading: a panic inside r.Body.Read must
// release the acquired admission lease before unwinding.
func TestIngestAdmissionPanicDuringBodyReadReleasesLease(t *testing.T) {
	limiter := httpapi.NewIngestAdmission(httpapi.IngestAdmissionConfig{
		MaxConcurrent: 4,
		MaxBytes:      1 << 20,
		MaxQueue:      -1,
	})
	handler := newAPI(t, &acceptancePort{}, httpapi.WithIngestAdmission(limiter))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected injected panic from body reader")
			}
		}()
		req := httptest.NewRequest(http.MethodPost, "/v0/records/batch", panickingReader{})
		req.ContentLength = 4096
		req.Header.Set("Authorization", "Bearer "+adminKey)
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()

	stats := limiter.Stats()
	if stats.InFlight != 0 || stats.InFlightBytes != 0 || stats.Queued != 0 {
		t.Fatalf("after body-read panic, stats=%+v; want zero leaked tokens", stats)
	}
}

// Owns error precedence in readBoundedIngestBody: when the overflowing chunk
// simultaneously exceeds http.MaxBytesReader and the remaining admission byte
// budget, HTTP 413 RequestTooLarge takes precedence over HTTP 429 RateLimited
// and releases all reserved tokens cleanly.
func TestIngestAdmissionMaxBytesErrorTakesPrecedenceOver429(t *testing.T) {
	const singleLimit = 1 << 20

	// Leave 5 bytes of bucket headroom above the under-declared Content-Length
	// so the final read spanning singleLimit returns both n=9 (> headroom) and
	// *http.MaxBytesError in the same Read call.
	limiter := httpapi.NewIngestAdmission(httpapi.IngestAdmissionConfig{
		MaxConcurrent: 2,
		MaxBytes:      singleLimit - 5,
		MaxQueue:      -1,
	})
	handler := newAPI(t, &acceptancePort{}, httpapi.WithIngestAdmission(limiter))

	body := bytes.Repeat([]byte("a"), singleLimit+10)
	req := httptest.NewRequest(http.MethodPost, "/v0/records", bytes.NewReader(body))
	req.ContentLength = singleLimit - 10
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413 Request Entity Too Large (body=%s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v (raw=%s)", err, rec.Body.String())
	}
	if resp["code"] != "request_too_large" {
		t.Fatalf("code=%v, want request_too_large (body=%s)", resp["code"], rec.Body.String())
	}

	stats := limiter.Stats()
	if stats.InFlight != 0 || stats.InFlightBytes != 0 || stats.Queued != 0 {
		t.Fatalf("post-413 admission stats=%+v; want zero leaked tokens", stats)
	}
}

// Owns generic concurrency/byte bounds, bounded FIFO queue cancellation,
// chunked byte shedding, and single-record 429 + Retry-After under a virtual clock.
func TestIngestAdmissionBoundsQueueChunkedAndSingleRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := httpapi.NewIngestAdmission(httpapi.IngestAdmissionConfig{
			MaxConcurrent: 2,
			MaxBytes:      4096,
			MaxQueue:      1,
			QueueTimeout:  50 * time.Millisecond,
		})

		l1, _, ok1 := limiter.Acquire(context.Background(), 2048)
		l2, _, ok2 := limiter.Acquire(context.Background(), 1024)
		if !ok1 || !ok2 {
			t.Fatalf("expected initial 2 acquisitions to succeed: %v %v", ok1, ok2)
		}

		// 3rd request queues; 4th sheds immediately with Retry-After >= 1.
		queuedDone := make(chan bool, 1)
		qCtx, qCancel := context.WithCancel(context.Background())
		go func() {
			lease, _, ok := limiter.Acquire(qCtx, 1024)
			if ok {
				lease.Release()
			}
			queuedDone <- ok
		}()
		synctest.Wait()

		start := time.Now()
		_, retryAfter, admitted := limiter.Acquire(context.Background(), 1024)
		if admitted || retryAfter < 1 || time.Since(start) != 0 {
			t.Fatalf("excess beyond queue: admitted=%v retry=%d elapsed=%s", admitted, retryAfter, time.Since(start))
		}

		// Cancel queued waiter -> removed cleanly; release active leases -> clean 0.
		qCancel()
		if ok := <-queuedDone; ok {
			t.Fatal("canceled waiter should not be admitted")
		}

		// While saturated, single-record POST /v0/records sheds with 429 + Retry-After.
		handler := newAPI(t, &acceptancePort{}, httpapi.WithIngestAdmission(limiter))
		singlePayload, _ := json.Marshal(inline("single-429", "s", "hello"))
		req := httptest.NewRequest(http.MethodPost, "/v0/records", bytes.NewReader(singlePayload))
		req.Header.Set("Authorization", "Bearer "+adminKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("saturated POST /v0/records: status=%d retry=%q body=%s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
		}

		l1.Release()
		l2.Release()

		// Chunked body exceeding MaxBytes (4096) sheds mid-read with 429 + Retry-After and leaks 0 tokens.
		bigChunked, _ := json.Marshal(map[string]any{"items": []any{inline("chunked-big", "cb", strings.Repeat("z", 8192))}})
		cReq := httptest.NewRequest(http.MethodPost, "/v0/records/batch", io.NopCloser(bytes.NewReader(bigChunked)))
		cReq.ContentLength = -1
		cReq.Header.Set("Authorization", "Bearer "+adminKey)
		cReq.Header.Set("Content-Type", "application/json")
		cRec := httptest.NewRecorder()
		handler.ServeHTTP(cRec, cReq)
		if cRec.Code != http.StatusTooManyRequests || cRec.Header().Get("Retry-After") == "" {
			t.Fatalf("oversized chunked batch: status=%d retry=%q body=%s", cRec.Code, cRec.Header().Get("Retry-After"), cRec.Body.String())
		}

		stats := limiter.Stats()
		if stats.InFlight != 0 || stats.InFlightBytes != 0 || stats.Queued != 0 {
			t.Fatalf("post-drain stats=%+v; want zeros", stats)
		}
	})
}

type slowAuditSink struct {
	delay  time.Duration
	mu     sync.Mutex
	events []audit.Event
}

func (s *slowAuditSink) Append(ctx context.Context, event *audit.Event) error {
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event.ID = int64(len(s.events) + 1)
	s.events = append(s.events, *event)
	return nil
}

func (s *slowAuditSink) Record(ctx context.Context, event *audit.Event, work func(context.Context) error) error {
	if err := work(ctx); err != nil {
		return err
	}
	return s.Append(ctx, event)
}

func (s *slowAuditSink) List(context.Context, string, audit.Filter) ([]audit.Event, error) {
	return nil, errors.New("not used")
}

// Owns refusal-audit loop hardening: exhausting the shared 2-second refusal
// audit budget caps total audit time at 2s and NEVER turns any refusal into
// StorageUnavailable. Acceptance time of healthy peers does not burn the budget.
func TestBatchRefusalAuditBudgetExhaustionPreservesRefusal(t *testing.T) {
	t.Run("exhausted 2s audit budget keeps original refusals", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sink := &slowAuditSink{delay: 600 * time.Millisecond}
			handler := newAPI(t, &acceptancePort{}, httpapi.WithAudit(sink))

			// 6 refused entries (out-of-scope corpus -> 404 not_found).
			// 3 audits complete in 1.8s, 4th hits the 2.0s budget, 5th-6th skip immediately.
			batch := make([]any, 6)
			for i := range batch {
				batch[i] = inline(fmt.Sprintf("denied-%d", i), fmt.Sprintf("d-%d", i), "x")
			}
			start := time.Now()
			status, body := postJSON(t, handler, "/v0/records/batch", scopedKey, map[string]any{"items": batch})
			elapsed := time.Since(start)
			if status != http.StatusOK {
				t.Fatalf("got %d: %v", status, body)
			}
			conforms(t, "BatchResult", body)
			if elapsed != 2*time.Second {
				t.Fatalf("elapsed=%s; want capped at 2s shared audit budget", elapsed)
			}
			if len(sink.events) != 3 {
				t.Fatalf("recorded %d audit events; want 3 within 2s budget", len(sink.events))
			}
			for i, item := range entries(t, body) {
				e := entryError(t, item)
				if e["code"] != "not_found" || e["retryable"] != false {
					t.Fatalf("entry %d degraded to %v; must preserve original refusal not_found", i, e)
				}
			}
		})
	})

	t.Run("acceptance latency does not burn refusal audit budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sink := &slowAuditSink{delay: 10 * time.Millisecond}
			port := &acceptancePort{delay: 500 * time.Millisecond}
			handler := newAPI(t, port, httpapi.WithAudit(sink))

			deniedFirst := inline("denied-first", "df", "x")
			deniedLast := inline("denied-last", "dl", "x")
			items := []any{deniedFirst}
			// 6 allowed entries x 500ms = 3s of acceptance work (> 2s wall-clock).
			for i := 0; i < 6; i++ {
				ok := inline(fmt.Sprintf("ok-%d", i), fmt.Sprintf("ok-%d", i), "x")
				ok["source"].(map[string]any)["corpus_id"] = "corpus_other"
				items = append(items, ok)
			}
			items = append(items, deniedLast)

			status, body := postJSON(t, handler, "/v0/records/batch", scopedKey, map[string]any{"items": items})
			if status != http.StatusOK {
				t.Fatalf("got %d: %v", status, body)
			}
			conforms(t, "BatchResult", body)
			if len(sink.events) != 2 {
				t.Fatalf("recorded %d audit events; want both refused entries audited despite 3s acceptance time", len(sink.events))
			}
			out := entries(t, body)
			for _, idx := range []int{0, len(out) - 1} {
				if e := entryError(t, out[idx]); e["code"] != "not_found" || e["retryable"] != false {
					t.Fatalf("entry %d: got %v, want not_found", idx, e)
				}
			}
		})
	})
}
