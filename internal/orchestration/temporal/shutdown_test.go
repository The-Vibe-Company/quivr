package temporal

import (
	"context"
	"testing"

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
	closed, returned := make(chan struct{}), make(chan struct{})
	w := blockedStopWorker{started: started, finish: finish}
	dispatchDone := make(chan struct{})
	runtime := Runtime{Worker: w, EvaluationWorker: w, ConnectorWorker: w, BackfillWorker: w,
		Client: shutdownClient{closed: closed}, dispatchDone: dispatchDone}
	ctx, expire := context.WithCancel(context.Background())
	defer expire()
	go func() { runtime.Close(ctx); close(returned) }()
	for range 4 {
		<-started
	}
	expire()
	<-closed
	<-returned
	close(finish)
	close(dispatchDone)
}
