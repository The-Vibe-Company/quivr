package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
)

func readinessProbe(loops *lifecycle.Group, ready func(context.Context) error, indexes *IndexMaintenance) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Quivr-Index-Setup", indexes.State())
		if loops.Draining() {
			http.Error(w, "process draining", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		err := ready(ctx)
		if loops.Draining() {
			http.Error(w, "process draining", 503)
			return
		}
		if errors.Is(err, errWarming) {
			http.Error(w, "search warming up", 503)
			return
		}
		if err != nil {
			http.Error(w, "dependencies unavailable", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// drainProcess keeps probes available while public HTTP and background work
// drain concurrently against one deadline. The recorder flushes after both.
func drainProcess(deadline context.Context, loops *lifecycle.Group, servers []*http.Server, flush func(context.Context)) {
	loops.BeginDrain()
	stopExpiry := context.AfterFunc(deadline, loops.Close)
	defer stopExpiry()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = loops.Wait(deadline) }()
	// The first server owns private probes and shuts down last.
	for _, server := range servers[min(1, len(servers)):] {
		wg.Add(1)
		go func() { defer wg.Done(); _ = server.Shutdown(deadline) }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-deadline.Done():
	}
	if deadline.Err() != nil {
		loops.Close()
	}
	flush(deadline)
	if deadline.Err() != nil {
		loops.Close()
	}
	if len(servers) > 0 {
		_ = servers[0].Shutdown(deadline)
	}
	for _, server := range servers {
		_ = server.Close()
	}
	loops.Close()
}

// shutdownDeadline uses the same starting instant on every cleanup path.
func shutdownDeadline(start time.Time, grace time.Duration) (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), start.Add(grace))
}
