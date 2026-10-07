package workqueue

import (
	"context"
	"errors"
)

// ErrLeaseLost reports that a tracked attempt no longer owns its durable
// attempt row. The callback receives a canceled context when this happens so
// its caller can stop before committing another effect.
var (
	ErrLeaseLost = errors.New("workqueue: lease lost")
	ErrLeaseHeld = errors.New("workqueue: lease held")
)

// Tracker records the bounded, durable lease for one document attempt. The
// adapter owns the lease implementation; this package only defines the
// execution boundary used by workers.
type Tracker interface {
	Track(context.Context, string, string, string, string, func(context.Context) error) error
}

type trackerKey struct{}

// WithTracker attaches a durable attempt tracker to a worker context.
func WithTracker(ctx context.Context, tracker Tracker) context.Context {
	return context.WithValue(ctx, trackerKey{}, tracker)
}

// TrackerFrom returns the tracker attached to ctx, when one exists.
func TrackerFrom(ctx context.Context) (Tracker, bool) {
	tracker, ok := ctx.Value(trackerKey{}).(Tracker)
	return tracker, ok && tracker != nil
}

// Track runs run under the tracker attached to ctx. Calls without a tracker
// retain the worker's normal execution semantics, which keeps local and
// legacy callers compatible while deployments add durable attempt tracking.
func Track(ctx context.Context, org, kind, workID, documentID string, run func(context.Context) error) error {
	if run == nil {
		return errors.New("workqueue: nil tracked function")
	}
	if tracker, ok := TrackerFrom(ctx); ok {
		return tracker.Track(ctx, org, kind, workID, documentID, run)
	}
	return run(ctx)
}
