package content

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
)

// errBlobReadPanicked fails a hydration whose blob read or cut panicked, so
// one faulty reader fails its batch instead of the process.
var errBlobReadPanicked = errors.New("canonical blob read panicked")

// recoveredBlobPanic logs a recovered panic and returns errBlobReadPanicked.
// The panic value may quote canonical text, so only its type is logged.
func recoveredBlobPanic(r any) error {
	slog.Error("canonical blob read panicked; failing the hydration batch", "component", "hydration", "panic_type", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
	return errBlobReadPanicked
}

// blobFlights coalesces concurrent cold reads of one immutable blob across
// hydration batches, so a stampede of searches over the same uncached blob
// costs one canonical read. A read runs on a context detached from the
// caller that started it and is cancelled only when every waiter has left.
type blobFlights struct {
	mu    sync.Mutex
	calls map[blobCacheKey]*blobFlight
}

type blobFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	entry   *blobCacheEntry
	err     error
}

// do returns the shared result of read for key, starting it when no read of
// key is in flight. A waiter whose ctx ends leaves with ctx.Err(); the read
// goes on for the others and is cancelled with the last one. A waiter whose
// ctx has already ended neither joins nor starts a read.
func (g *blobFlights) do(ctx context.Context, key blobCacheKey, read func(context.Context) (*blobCacheEntry, error)) (*blobCacheEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	f, ok := g.calls[key]
	if !ok {
		work, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &blobFlight{done: make(chan struct{}), cancel: cancel}
		if g.calls == nil {
			g.calls = map[blobCacheKey]*blobFlight{}
		}
		g.calls[key] = f
		go g.fly(key, f, work, read)
	}
	f.waiters++
	g.mu.Unlock()
	select {
	case <-f.done:
		// Both may be ready at once: a waiter that has left gets its own error.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return f.entry, f.err
	case <-ctx.Done():
		g.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			// Forget the abandoned read so the next caller starts afresh.
			if g.calls[key] == f {
				delete(g.calls, key)
			}
			f.cancel()
		}
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (g *blobFlights) fly(key blobCacheKey, f *blobFlight, work context.Context, read func(context.Context) (*blobCacheEntry, error)) {
	var result *blobCacheEntry
	var err error
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, recoveredBlobPanic(r)
		}
		f.cancel()
		g.mu.Lock()
		if g.calls[key] == f {
			delete(g.calls, key)
		}
		f.entry, f.err = result, err
		close(f.done)
		g.mu.Unlock()
	}()
	result, err = read(work)
}
