package temporal

import (
	"context"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

type blockedStopWorker struct {
	worker.Worker
	started chan<- struct{}
	finish  <-chan struct{}
}

func (w blockedStopWorker) Stop() {
	w.started <- struct{}{}
	<-w.finish
}

type shutdownClient struct {
	client.Client
	closed chan struct{}
}

func (c shutdownClient) Close() { close(c.closed) }

// Owns Temporal shutdown at the runtime boundary: independent workers stop
// concurrently, and a process deadline aborts their join and outstanding RPCs.
func TestRuntimeShutdownStopsWorkersConcurrentlyWithinTheProcessBudget(t *testing.T) {
	started, finish := make(chan struct{}, 4), make(chan struct{})
	defer close(finish)
	closed, returned := make(chan struct{}), make(chan struct{})
	w := blockedStopWorker{started: started, finish: finish}
	dispatchDone := make(chan struct{})
	defer close(dispatchDone)
	runtime := Runtime{QueueWorkers: []worker.Worker{w, w, w, w},
		Client: shutdownClient{closed: closed}, dispatchDone: dispatchDone}
	ctx, expire := context.WithCancel(context.Background())
	defer expire()
	watchdog, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { runtime.Close(ctx); close(returned) }()
	for i := range 4 {
		select {
		case <-started:
		case <-watchdog.Done():
			t.Fatalf("only %d of 4 workers began stopping; shutdown must stop them concurrently", i)
		}
	}
	expire()
	select {
	case <-closed:
	case <-watchdog.Done():
		t.Fatal("Temporal client did not close after the process budget expired")
	}
	select {
	case <-returned:
	case <-watchdog.Done():
		t.Fatal("runtime shutdown did not return after the process budget expired")
	}
}
