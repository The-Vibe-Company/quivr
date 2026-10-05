package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
)

type processEvent struct {
	at      time.Time
	message string
	attrs   []slog.Attr
}

// Each process emits at most four lifecycle events. Queueing them under the
// state lock preserves their order without allowing a blocked sink to delay
// admission cancellation or consume the shutdown budget before cleanup starts.
type processEvents struct {
	mu         sync.Mutex
	queue      chan processEvent
	done       chan struct{}
	readySent  bool
	drainStart time.Time
}

func newProcessEvents(logger *slog.Logger, summary slog.Attr) *processEvents {
	e := &processEvents{queue: make(chan processEvent, 4), done: make(chan struct{})}
	e.emit("process starting", "quivr.start", summary)
	go func() {
		defer close(e.done)
		for event := range e.queue {
			record := slog.NewRecord(event.at, slog.LevelInfo, event.message, 0)
			record.AddAttrs(event.attrs...)
			if logger.Enabled(context.Background(), record.Level) {
				_ = logger.Handler().Handle(context.Background(), record)
			}
		}
	}()
	return e
}

// emit is called only while holding mu, except before the instance is shared.
func (e *processEvents) emit(message, code string, attrs ...slog.Attr) {
	attrs = append([]slog.Attr{slog.String("event", code)}, attrs...)
	e.queue <- processEvent{at: time.Now(), message: message, attrs: attrs}
}

func (e *processEvents) ready(ctx context.Context, message string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ctx.Err() == nil && e.drainStart.IsZero() && !e.readySent {
		e.readySent = true
		e.emit(message, "quivr.ready")
	}
}

func (e *processEvents) drain(loops *lifecycle.Group) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.drainStart.IsZero() {
		e.drainStart = time.Now()
		loops.BeginDrain()
		e.emit("process draining", "quivr.draining")
	}
}

func (e *processEvents) shutdownStart() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.drainStart.IsZero() {
		return time.Now()
	}
	return e.drainStart
}

func (e *processEvents) stop(deadline context.Context, graceExpired bool) {
	e.mu.Lock()
	e.emit("process stopped", "quivr.stop", slog.Bool("grace_expired", graceExpired))
	close(e.queue)
	e.mu.Unlock()
	select {
	case <-e.done:
	case <-deadline.Done():
	}
}
