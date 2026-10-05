package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/logging"
)

type blockedProcessOutput struct {
	bytes.Buffer
	entered chan struct{}
	release chan struct{}
}

func (w *blockedProcessOutput) Write(b []byte) (int, error) {
	select {
	case <-w.entered:
	default:
		close(w.entered)
		<-w.release
	}
	return w.Buffer.Write(b)
}

// Owns lifecycle ordering with an actual JSON logger and a blocked output
// dependency. Readiness, work cancellation and cleanup must not wait on stdout.
func TestLifecycleEventsPreserveOrderWithoutBlockingDrain(t *testing.T) {
	for _, readyFirst := range []bool{false, true} {
		name := "drain-before-ready"
		if readyFirst {
			name = "ready-before-drain"
		}
		t.Run(name, func(t *testing.T) {
			output := &blockedProcessOutput{entered: make(chan struct{}), release: make(chan struct{})}
			logger, err := logging.New(output, logging.Options{})
			if err != nil {
				t.Fatal(err)
			}
			events := newProcessEvents(logger, slog.Group("effective_config"))
			startup, cancelStartup := context.WithTimeout(context.Background(), time.Second)
			defer cancelStartup()
			select {
			case <-output.entered:
			case <-startup.Done():
				close(output.release)
				t.Fatal("startup event not written")
			}
			loops := lifecycle.New()
			defer loops.Close()
			if readyFirst {
				events.ready(loops.Context(), "ready")
			}
			drained := make(chan struct{})
			go func() { events.drain(loops); close(drained) }()
			guard, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			select {
			case <-drained:
			case <-guard.Done():
				close(output.release)
				t.Fatal("blocked stdout delayed drain")
			}
			if loops.Context().Err() == nil {
				t.Fatal("drain did not cancel admission")
			}
			events.ready(context.Background(), "late ready")
			expired, expire := context.WithCancel(context.Background())
			expire()
			stopped := make(chan struct{})
			go func() { events.stop(expired, true); close(stopped) }()
			select {
			case <-stopped:
			case <-guard.Done():
				close(output.release)
				t.Fatal("blocked stdout delayed stop beyond budget")
			}
			close(output.release)
			select {
			case <-events.done:
			case <-guard.Done():
				t.Fatal("event logging did not finish after sink release")
			}
			want := []string{"quivr.start", "quivr.draining", "quivr.stop"}
			if readyFirst {
				want = []string{"quivr.start", "quivr.ready", "quivr.draining", "quivr.stop"}
			}
			decoder := json.NewDecoder(&output.Buffer)
			for _, code := range want {
				var record map[string]any
				if err := decoder.Decode(&record); err != nil {
					t.Fatal(err)
				}
				if record["event"] != code {
					t.Fatalf("event = %v, want %s", record["event"], code)
				}
			}
			if decoder.More() {
				t.Fatal("unexpected lifecycle event")
			}
		})
	}
}
