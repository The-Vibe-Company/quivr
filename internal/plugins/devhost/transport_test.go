package devhost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

func TestDefaultClientRetainsConnectionsForConcurrentInvocations(t *testing.T) {
	pool, ok := httpClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default plugin transport = %T, want a dedicated HTTP pool", httpClient().Transport)
	}
	if pool.MaxIdleConns != 512 || pool.MaxIdleConnsPerHost != 128 || pool.IdleConnTimeout != 90*time.Second {
		t.Fatalf("idle pool total=%d per host=%d timeout=%s, want 512, 128, 90s", pool.MaxIdleConns, pool.MaxIdleConnsPerHost, pool.IdleConnTimeout)
	}
}

func TestReadBoundedBodyContentLengthAndCaps(t *testing.T) {
	t.Run("exact Content-Length under and at limit", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			size   int
			maxLen int
		}{
			{name: "empty", size: 0, maxLen: 1024},
			{name: "small", size: 128, maxLen: 1024},
			{name: "270KB", size: 270 * 1024, maxLen: 1 << 20},
			{name: "exact_limit", size: 4096, maxLen: 4096},
		} {
			t.Run(tc.name, func(t *testing.T) {
				payload := bytes.Repeat([]byte("a"), tc.size)
				got, err := readBoundedBody(bytes.NewReader(payload), int64(len(payload)), tc.maxLen)
				if err != nil {
					t.Fatalf("readBoundedBody: %v", err)
				}
				if got == nil {
					t.Fatal("returned nil slice; want non-nil slice like io.ReadAll")
				}
				if !bytes.Equal(got, payload) {
					t.Fatalf("len=%d, want %d", len(got), len(payload))
				}
			})
		}
	})

	t.Run("untrusted huge Content-Length cannot OOM and preserves CodeResponseTooLarge", func(t *testing.T) {
		const maxBytes = 1024
		// 1 TiB declared Content-Length with only 64 bytes then unexpected EOF.
		body, err := readBoundedBody(io.MultiReader(bytes.NewReader(make([]byte, 64)), errReader{io.ErrUnexpectedEOF}), 1<<40, maxBytes)
		if !errors.Is(err, io.ErrUnexpectedEOF) || body != nil {
			t.Fatalf("premature EOF with huge Content-Length = (%d bytes, %v), want ErrUnexpectedEOF", len(body), err)
		}

		// 1 TiB declared Content-Length streaming more than maxBytes -> caps at maxBytes+1.
		got, err := readBoundedBody(bytes.NewReader(bytes.Repeat([]byte("z"), maxBytes+512)), 1<<40, maxBytes)
		if err != nil {
			t.Fatalf("oversized stream err = %v", err)
		}
		if len(got) != maxBytes+1 {
			t.Fatalf("len = %d, want %d (maxBytes+1 sentinel)", len(got), maxBytes+1)
		}
	})

	t.Run("chunked unknown Content-Length enforces limit", func(t *testing.T) {
		const maxBytes = 2048
		okPayload := bytes.Repeat([]byte("c"), maxBytes)
		got, err := readBoundedBody(bytes.NewReader(okPayload), -1, maxBytes)
		if err != nil || !bytes.Equal(got, okPayload) {
			t.Fatalf("chunked at limit = (%d, %v), want %d", len(got), err, maxBytes)
		}

		overPayload := bytes.Repeat([]byte("c"), maxBytes+10)
		got, err = readBoundedBody(bytes.NewReader(overPayload), -1, maxBytes)
		if err != nil || len(got) != maxBytes+1 {
			t.Fatalf("chunked over limit = (%d, %v), want %d", len(got), err, maxBytes+1)
		}
	})

	t.Run("boundary overflow probe preserves non-EOF read errors", func(t *testing.T) {
		const maxBytes = 64
		r := io.MultiReader(
			bytes.NewReader(bytes.Repeat([]byte("a"), maxBytes)),
			oneByteThenErrReader{b: 'x', err: io.ErrUnexpectedEOF},
		)
		got, err := readBoundedBody(r, -1, maxBytes)
		if !errors.Is(err, io.ErrUnexpectedEOF) || got != nil {
			t.Fatalf("boundary probe with error = (%d bytes, %v), want ErrUnexpectedEOF", len(got), err)
		}
	})

	t.Run("InvokeSearch enforces CodeResponseTooLarge with Content-Length and chunked", func(t *testing.T) {
		const maxBytes = 256
		for _, chunked := range []bool{false, true} {
			name := "content_length"
			if chunked {
				name = "chunked"
			}
			t.Run(name, func(t *testing.T) {
				payload := []byte(`{"padding":"` + strings.Repeat("x", maxBytes+64) + `"}`)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if !chunked {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					}
					w.WriteHeader(http.StatusOK)
					if chunked {
						if f, ok := w.(http.Flusher); ok {
							f.Flush()
						}
					}
					_, _ = w.Write(payload)
				}))
				defer srv.Close()

				res, err := InvokeSearch(context.Background(), srv.URL, []byte(`{}`), maxBytes, func([]byte) []plugins.Issue { return nil })
				if err != nil {
					t.Fatalf("InvokeSearch: %v", err)
				}
				if res.Body != nil || len(res.Issues) != 1 || res.Issues[0].Code != CodeResponseTooLarge {
					t.Fatalf("result = %+v, want CodeResponseTooLarge with nil body", res)
				}
			})
		}
	})
}

func TestWithRequestReleaseLifecycle(t *testing.T) {
	// Released once after normal InvokeSearch completion.
	var releases atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ranking":{"hits":[]}}`))
	}))
	defer srv.Close()

	ctx, cleanup := WithRequestRelease(context.Background(), func() { releases.Add(1) })
	res, err := InvokeSearch(ctx, srv.URL, []byte(`{"round":1}`), 1024, func([]byte) []plugins.Issue { return nil })
	cleanup()
	if err != nil || res == nil || res.Status != http.StatusOK {
		t.Fatalf("InvokeSearch = (%+v, %v)", res, err)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("releases after InvokeSearch = %d, want 1", got)
	}

	// Released once via cleanup when invoke never runs.
	var unclaimed atomic.Int32
	_, cleanupUnclaimed := WithRequestRelease(context.Background(), func() { unclaimed.Add(1) })
	cleanupUnclaimed()
	cleanupUnclaimed() // idempotent
	if got := unclaimed.Load(); got != 1 {
		t.Fatalf("unclaimed releases = %d, want 1", got)
	}

	// Concurrent Read and Close must synchronize before releasing the buffer.
	data := bytes.Repeat([]byte("a"), 4096)
	var released atomic.Bool
	tracker := newTrackedBytesBody(data, func() {
		released.Store(true)
		// Mutate buffer on release to catch any race under -race.
		for i := range data {
			data[i] = 'b'
		}
	})
	reader := tracker.newReader()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 128)
		for {
			if _, err := reader.Read(buf); err != nil {
				return
			}
		}
	}()
	_ = reader.Close()
	tracker.dec()
	<-done
	if !released.Load() {
		t.Fatal("buffer was not released after Close and owner dec")
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

type oneByteThenErrReader struct {
	b   byte
	err error
}

func (r oneByteThenErrReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.b
	return 1, r.err
}
