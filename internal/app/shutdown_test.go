package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
)

// Owns readiness ordering and HTTP drain at the actual server boundary. The
// lifecycle unit test cannot see premature listener closure or response loss.
func TestDrainMakesReadinessFailWhileAnAdmittedRequestCompletes(t *testing.T) {
	loops := lifecycle.New()
	defer loops.Close()
	probe := httptest.NewServer(readinessProbe(loops, func(context.Context) error { return nil }))
	t.Cleanup(probe.Close)
	started, finish := make(chan struct{}), make(chan struct{})
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-finish
		_, _ = io.WriteString(w, "complete")
	}))
	t.Cleanup(public.Close)
	response := make(chan string, 1)
	go func() {
		r, err := http.Get(public.URL)
		if err != nil {
			response <- err.Error()
			return
		}
		defer r.Body.Close()
		b, err := io.ReadAll(r.Body)
		if err != nil {
			response <- err.Error()
			return
		}
		response <- string(b)
	}()
	<-started
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		drainProcess(deadline, loops, []*http.Server{probe.Config, public.Config}, func(context.Context) {})
		close(done)
	}()
	<-loops.Context().Done()
	r, err := http.Get(probe.URL)
	if err != nil {
		close(finish)
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 503 {
		close(finish)
		t.Fatalf("readiness during drain: %d", r.StatusCode)
	}
	close(finish)
	if got := <-response; got != "complete" {
		t.Fatalf("admitted response lost: %q", got)
	}
	select {
	case <-done:
	case <-deadline.Done():
		t.Fatal("successful drain exceeded its budget")
	}
}
