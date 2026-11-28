package devhost

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

var configuredClient atomic.Pointer[http.Client]

// TransportError reports a Contribution POST send or response body read failure.
type TransportError struct{ err error }

func (e *TransportError) Error() string { return e.err.Error() }
func (e *TransportError) Unwrap() error { return e.err }

// SetTransport installs deployment trust for all plugin endpoints, including
// plugins registered after startup and work pinned to earlier plans. Call once
// before serving. A nil transport restores the default. Redirects stay refused.
func SetTransport(transport http.RoundTripper) {
	if transport == nil {
		configuredClient.Store(nil)
		return
	}
	configuredClient.Store(&http.Client{Transport: telemetry.Transport(transport, "plugin.call"), CheckRedirect: client.CheckRedirect})
}

type responseCheckKey struct{}

// WithResponseCheck checks a Contribution POST's headers before reading or
// validating its body.
func WithResponseCheck(ctx context.Context, check func(status int, header http.Header) error) context.Context {
	return context.WithValue(ctx, responseCheckKey{}, check)
}

type requestReleaseKey struct{}

type requestRelease struct {
	claimed atomic.Bool
	fn      func()
}

// WithRequestRelease attaches a release callback to ctx that invoke executes
// only after the HTTP transport has closed every reader over the request body
// (including retries via GetBody and cancelled writes). The returned cleanup
// function runs release immediately if invoke never claimed the request body.
func WithRequestRelease(ctx context.Context, release func()) (context.Context, func()) {
	if release == nil {
		return ctx, func() {}
	}
	rr := &requestRelease{fn: release}
	return context.WithValue(ctx, requestReleaseKey{}, rr), func() {
		if !rr.claimed.Swap(true) {
			rr.fn()
		}
	}
}

type trackedBytesBody struct {
	data    []byte
	refs    atomic.Int32
	release func()
}

func newTrackedBytesBody(data []byte, release func()) *trackedBytesBody {
	t := &trackedBytesBody{data: data, release: release}
	t.refs.Store(1) // held by invoke until invoke returns
	return t
}

func (t *trackedBytesBody) newReader() *trackedBytesReader {
	t.refs.Add(1)
	return &trackedBytesReader{tracker: t, r: bytes.NewReader(t.data)}
}

func (t *trackedBytesBody) dec() {
	if t.refs.Add(-1) == 0 && t.release != nil {
		t.release()
	}
}

type trackedBytesReader struct {
	tracker *trackedBytesBody
	mu      sync.Mutex
	r       *bytes.Reader
	closed  bool
}

func (r *trackedBytesReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	return r.r.Read(p)
}

func (r *trackedBytesReader) Close() error {
	r.mu.Lock()
	already := r.closed
	r.closed = true
	r.mu.Unlock()
	if !already {
		r.tracker.dec()
	}
	return nil
}

var _ io.ReadCloser = (*trackedBytesReader)(nil)

func httpClient() *http.Client {
	if configured := configuredClient.Load(); configured != nil {
		return configured
	}
	return client
}
