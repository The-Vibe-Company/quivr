package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

const (
	defaultIngestMaxConcurrent = 64
	defaultIngestMaxBytes      = 64 << 20
	defaultIngestQueueTimeout  = 50 * time.Millisecond
	ingestChunkReadSize        = 32 << 10
)

// IngestAdmissionConfig configures generic, process-local in-flight HTTP
// ingestion bounds (concurrency and payload bytes). Zero values select safe
// engine defaults; negative MaxQueue or QueueTimeout disable waiting.
type IngestAdmissionConfig struct {
	MaxConcurrent int
	MaxBytes      int64
	MaxQueue      int
	QueueTimeout  time.Duration
	Now           func() time.Time
}

// IngestAdmissionStats is a point-in-time snapshot of process-local ingest
// admission state.
type IngestAdmissionStats struct {
	MaxConcurrent int
	InFlight      int
	MaxBytes      int64
	InFlightBytes int64
	Queued        int
}

type ingestWaiter struct {
	ch      chan struct{}
	bytes   int64
	granted bool
}

// IngestAdmission bounds simultaneous HTTP ingestion requests and total
// in-flight payload bytes per API process. Excess requests are shed cleanly
// with HTTP 429 and a positive integer Retry-After delay.
type IngestAdmission struct {
	maxConcurrent int
	maxBytes      int64
	maxQueue      int
	queueTimeout  time.Duration
	now           func() time.Time

	mu            sync.Mutex
	inFlight      int
	inFlightBytes int64
	waiters       []*ingestWaiter
}

// NewIngestAdmission builds a generic in-flight concurrency and byte limiter
// with normalized process-local defaults.
func NewIngestAdmission(cfg IngestAdmissionConfig) *IngestAdmission {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultIngestMaxConcurrent
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultIngestMaxBytes
	}
	maxQueue := cfg.MaxQueue
	if maxQueue < 0 {
		maxQueue = 0
	}
	queueTimeout := cfg.QueueTimeout
	switch {
	case queueTimeout == 0:
		queueTimeout = defaultIngestQueueTimeout
	case queueTimeout < 0:
		queueTimeout = 0
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &IngestAdmission{
		maxConcurrent: maxConcurrent,
		maxBytes:      maxBytes,
		maxQueue:      maxQueue,
		queueTimeout:  queueTimeout,
		now:           now,
	}
}

// WithIngestAdmission overrides the default process-local ingest admission limiter.
func WithIngestAdmission(limiter *IngestAdmission) Option {
	return func(a *API) {
		if limiter != nil {
			a.ingestAdmission = limiter
		}
	}
}

// IngestLease represents one admitted in-flight HTTP ingestion request and its
// reserved payload byte budget. Release is idempotent.
type IngestLease struct {
	limiter *IngestAdmission
	mu      sync.Mutex
	bytes   int64
	done    atomic.Bool
}

// Stats returns a consistent snapshot of the limiter's current state.
func (l *IngestAdmission) Stats() IngestAdmissionStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return IngestAdmissionStats{
		MaxConcurrent: l.maxConcurrent,
		InFlight:      l.inFlight,
		MaxBytes:      l.maxBytes,
		InFlightBytes: l.inFlightBytes,
		Queued:        len(l.waiters),
	}
}

// Acquire claims one concurrency slot and estimatedBytes of in-flight payload
// capacity. For unknown-length bodies pass estimatedBytes == 0 and reserve
// incrementally via IngestLease.Reserve as chunks arrive. When saturated or
// canceled, Acquire returns admitted=false with a positive integer Retry-After.
func (l *IngestAdmission) Acquire(ctx context.Context, estimatedBytes int64) (*IngestLease, int, bool) {
	if estimatedBytes < 0 {
		estimatedBytes = 0
	}
	if ctx.Err() != nil {
		return nil, l.retryAfter(), false
	}
	l.mu.Lock()
	if estimatedBytes > l.maxBytes {
		retry := l.retryAfterLocked()
		l.mu.Unlock()
		return nil, retry, false
	}
	if l.canAdmitLocked(estimatedBytes) && len(l.waiters) == 0 {
		l.inFlight++
		l.inFlightBytes += estimatedBytes
		l.mu.Unlock()
		return &IngestLease{limiter: l, bytes: estimatedBytes}, 0, true
	}
	if l.maxQueue <= 0 || len(l.waiters) >= l.maxQueue || l.queueTimeout <= 0 {
		retry := l.retryAfterLocked()
		l.mu.Unlock()
		return nil, retry, false
	}
	wait := l.queueTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := deadline.Sub(l.now())
		if remaining <= 0 {
			retry := l.retryAfterLocked()
			l.mu.Unlock()
			return nil, retry, false
		}
		if capWait := remaining / 4; capWait < wait {
			wait = capWait
		}
	}
	if wait <= 0 {
		retry := l.retryAfterLocked()
		l.mu.Unlock()
		return nil, retry, false
	}
	w := &ingestWaiter{ch: make(chan struct{}), bytes: estimatedBytes}
	l.waiters = append(l.waiters, w)
	l.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-w.ch:
		if ctx.Err() != nil {
			l.release(estimatedBytes)
			return nil, l.retryAfter(), false
		}
		return &IngestLease{limiter: l, bytes: estimatedBytes}, 0, true
	case <-timer.C:
		return l.abortWaiter(ctx, w, false)
	case <-ctx.Done():
		return l.abortWaiter(ctx, w, true)
	}
}

func (l *IngestAdmission) canAdmitLocked(requestedBytes int64) bool {
	if l.inFlight >= l.maxConcurrent {
		return false
	}
	if requestedBytes == 0 {
		return l.inFlightBytes < l.maxBytes
	}
	return l.inFlightBytes+requestedBytes <= l.maxBytes
}

func (l *IngestAdmission) abortWaiter(ctx context.Context, w *ingestWaiter, ctxDone bool) (*IngestLease, int, bool) {
	l.mu.Lock()
	if w.granted {
		if !ctxDone && ctx.Err() == nil {
			bytes := w.bytes
			l.mu.Unlock()
			return &IngestLease{limiter: l, bytes: bytes}, 0, true
		}
		l.inFlight--
		l.inFlightBytes -= w.bytes
		l.dispatchLocked()
		retry := l.retryAfterLocked()
		l.mu.Unlock()
		return nil, retry, false
	}
	for i, candidate := range l.waiters {
		if candidate == w {
			copy(l.waiters[i:], l.waiters[i+1:])
			l.waiters[len(l.waiters)-1] = nil
			l.waiters = l.waiters[:len(l.waiters)-1]
			if i == 0 {
				l.dispatchLocked()
			}
			break
		}
	}
	retry := l.retryAfterLocked()
	l.mu.Unlock()
	return nil, retry, false
}

// Reserve claims additional in-flight bytes for an already-admitted request
// (used for chunked or under-declared request bodies).
func (lease *IngestLease) Reserve(delta int64) (int, bool) {
	if lease == nil || delta <= 0 {
		return 0, true
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.done.Load() {
		return 1, false
	}
	l := lease.limiter
	l.mu.Lock()
	if l.inFlightBytes+delta > l.maxBytes {
		retry := l.retryAfterLocked()
		l.mu.Unlock()
		return retry, false
	}
	l.inFlightBytes += delta
	l.mu.Unlock()
	lease.bytes += delta
	return 0, true
}

// ReleaseBytes returns unused byte capacity (e.g. when the actual body was
// shorter than Content-Length) while keeping the concurrency slot active.
func (lease *IngestLease) ReleaseBytes(delta int64) {
	if lease == nil || delta <= 0 {
		return
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.done.Load() {
		return
	}
	if delta > lease.bytes {
		delta = lease.bytes
	}
	if delta == 0 {
		return
	}
	lease.bytes -= delta
	l := lease.limiter
	l.mu.Lock()
	l.inFlightBytes -= delta
	l.dispatchLocked()
	l.mu.Unlock()
}

// Release returns the concurrency slot and all reserved bytes. Idempotent.
func (lease *IngestLease) Release() {
	if lease == nil || !lease.done.CompareAndSwap(false, true) {
		return
	}
	lease.mu.Lock()
	reserved := lease.bytes
	lease.bytes = 0
	lease.mu.Unlock()
	lease.limiter.release(reserved)
}

func (l *IngestAdmission) release(bytes int64) {
	l.mu.Lock()
	l.inFlight--
	l.inFlightBytes -= bytes
	l.dispatchLocked()
	l.mu.Unlock()
}

func (l *IngestAdmission) dispatchLocked() {
	for len(l.waiters) > 0 {
		w := l.waiters[0]
		if !l.canAdmitLocked(w.bytes) {
			break
		}
		copy(l.waiters, l.waiters[1:])
		l.waiters[len(l.waiters)-1] = nil
		l.waiters = l.waiters[:len(l.waiters)-1]
		w.granted = true
		l.inFlight++
		l.inFlightBytes += w.bytes
		close(w.ch)
	}
}

func (l *IngestAdmission) retryAfter() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retryAfterLocked()
}

func (l *IngestAdmission) retryAfterLocked() int {
	sec := 1
	if l.maxBytes > 0 && l.inFlightBytes >= l.maxBytes && len(l.waiters) > 0 {
		sec = 2
	}
	return min(max(sec, 1), 5)
}

// acquireIngestPayload validates Content-Type, enforces Content-Length bounds,
// acquires generic ingest admission BEFORE reading the body, and reads one
// bounded UTF-8 payload. On success the caller MUST defer lease.Release().
func (a *API) acquireIngestPayload(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, *IngestLease, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, publicerr.UnsupportedMediaType, nil)
		return nil, nil, false
	}
	if r.ContentLength > limit {
		writeError(w, publicerr.RequestTooLarge, nil)
		return nil, nil, false
	}
	var estimated int64
	if r.ContentLength > 0 {
		estimated = r.ContentLength
	}
	var lease *IngestLease
	if a.ingestAdmission != nil {
		var retryAfter int
		var admitted bool
		lease, retryAfter, admitted = a.ingestAdmission.Acquire(r.Context(), estimated)
		if !admitted {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeError(w, publicerr.RateLimited, nil)
			return nil, nil, false
		}
	}
	transferred := false
	defer func() {
		if !transferred {
			lease.Release()
		}
	}()
	payload, ok := readBoundedIngestBody(w, r, limit, estimated, lease)
	if !ok {
		return nil, nil, false
	}
	transferred = true
	return payload, lease, true
}

func readBoundedIngestBody(w http.ResponseWriter, r *http.Request, limit, reserved int64, lease *IngestLease) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	var buf []byte
	if r.ContentLength > 0 && r.ContentLength <= limit {
		// Exact-sized buffer avoids io.ReadAll doubling allocations (16 MiB for 10 MiB).
		// Add 1 byte so the final Read observes EOF or triggers MaxBytesReader without growing.
		buf = make([]byte, 0, r.ContentLength+1)
	} else {
		initial := min(limit, int64(4096))
		if initial < 512 {
			initial = 512
		}
		buf = make([]byte, 0, initial)
	}
	accounted := reserved
	for {
		if len(buf) == cap(buf) {
			// Grow up to limit+1 without doubling past the configured body bound.
			nextCap := cap(buf) * 2
			maxCap := int(limit) + 1
			if nextCap > maxCap {
				nextCap = maxCap
			}
			if nextCap <= cap(buf) {
				nextCap = cap(buf) + ingestChunkReadSize
			}
			grown := make([]byte, len(buf), nextCap)
			copy(grown, buf)
			buf = grown
		}
		avail := cap(buf) - len(buf)
		if lease != nil && int64(len(buf)) >= accounted && avail > ingestChunkReadSize {
			// For chunked bodies, read in bounded slices so excess bytes shed early.
			avail = ingestChunkReadSize
		}
		n, err := r.Body.Read(buf[len(buf) : len(buf)+avail])
		if n > 0 {
			newLen := int64(len(buf) + n)
			if lease != nil && newLen > accounted {
				delta := newLen - accounted
				if retryAfter, ok := lease.Reserve(delta); !ok {
					w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
					writeError(w, publicerr.RateLimited, nil)
					return nil, false
				}
				accounted = newLen
			}
			buf = buf[:len(buf)+n]
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			var large *http.MaxBytesError
			deadline, hasDeadline := r.Context().Deadline()
			if errors.As(err, &large) {
				writeError(w, publicerr.RequestTooLarge, nil)
			} else if r.Method == "POST" && r.URL.Path == "/v0/records/batch" &&
				(r.Context().Err() != nil || hasDeadline && !time.Now().Before(deadline)) {
				// The connection's read deadline may fire before the context timer.
				writeError(w, publicerr.ContentUnavailable, nil)
			} else {
				writeDecodeError(w, r, publicerr.MalformedJson)
			}
			return nil, false
		}
	}
	if lease != nil && int64(len(buf)) < accounted {
		lease.ReleaseBytes(accounted - int64(len(buf)))
	}
	if !utf8.Valid(buf) {
		writeDecodeError(w, r, publicerr.MalformedJson)
		return nil, false
	}
	return buf, true
}
