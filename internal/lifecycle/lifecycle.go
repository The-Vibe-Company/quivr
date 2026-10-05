// Package lifecycle separates process admission from work already in progress.
package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type workKey struct{}

// Group owns background loops and their shared shutdown deadline. Go calls
// finish before BeginDrain; no new loops may be registered while waiting.
type Group struct {
	ctx, work        context.Context
	stop, cancelWork context.CancelFunc
	wg               sync.WaitGroup
	draining         atomic.Bool
}

func New() *Group {
	work, cancel := context.WithCancel(context.Background())
	managed := &managedWork{Context: work}
	ctx, stop := context.WithCancel(WithWorkContext(context.Background(), managed))
	return &Group{ctx: ctx, work: managed, stop: stop, cancelWork: cancel}
}

type managedWork struct{ context.Context }

func (c *managedWork) Value(key any) any {
	if _, ok := key.(workKey); ok {
		return c
	}
	return c.Context.Value(key)
}

type workValues struct {
	context.Context
	values context.Context
}

func (c workValues) Value(key any) any {
	if value := c.Context.Value(key); value != nil {
		return value
	}
	return c.values.Value(key)
}

// WithWorkContext binds request cleanup to the process budget while keeping
// request cancellation and values for normal handler work.
func WithWorkContext(ctx, work context.Context) context.Context {
	return context.WithValue(ctx, workKey{}, work)
}

func (g *Group) Context() context.Context { return g.ctx }
func (g *Group) Draining() bool           { return g.draining.Load() }
func (g *Group) Go(run func(context.Context)) {
	g.wg.Add(1)
	go func() { defer g.wg.Done(); run(g.ctx) }()
}

// WorkContext carries cancellation at the process grace deadline, rather than
// at admission shutdown. Outside a managed process it preserves ctx behavior.
func WorkContext(ctx context.Context) context.Context {
	if work, ok := ctx.Value(workKey{}).(context.Context); ok {
		return workValues{Context: work, values: ctx}
	}
	return ctx
}

// CleanupContext lets durable outcome recording survive attempt cancellation,
// but never the managed process budget. Standalone callers retain the prior
// bounded cleanup behavior. Nested cleanup preserves the process marker.
func CleanupContext(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	parent := context.WithoutCancel(ctx)
	if _, ok := ctx.Value(workKey{}).(context.Context); ok {
		parent = WorkContext(ctx)
	}
	return context.WithTimeout(parent, limit)
}

func (g *Group) BeginDrain() {
	g.draining.Store(true)
	g.stop()
}

// Wait cancels outstanding work when the shared deadline expires. It never
// adds a per-component timeout that could extend the process grace period.
func (g *Group) Wait(deadline context.Context) error {
	done := make(chan struct{})
	go func() { g.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-deadline.Done():
		g.cancelWork()
		return deadline.Err()
	}
}

func (g *Group) Close() { g.BeginDrain(); g.cancelWork() }
