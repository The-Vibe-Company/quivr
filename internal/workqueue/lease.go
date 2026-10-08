package workqueue

import (
	"context"
	"errors"
)

// Tracker records the bounded, durable lease for one document attempt. The
// adapter owns the lease implementation; this package only defines the
// execution boundary used by workers. Tracking is an observation: an
// implementation runs run with the context it received and returns run's
// result, whatever happens to the lease.
type Tracker interface {
	Track(context.Context, string, string, string, string, func(context.Context) error) error
}

type trackerKey struct{}

// WithTracker attaches a durable attempt tracker to a worker context.
func WithTracker(ctx context.Context, tracker Tracker) context.Context {
	return context.WithValue(ctx, trackerKey{}, tracker)
}

func trackerFrom(ctx context.Context) (Tracker, bool) {
	tracker, ok := ctx.Value(trackerKey{}).(Tracker)
	return tracker, ok && tracker != nil
}

// Track runs run under the tracker attached to ctx, or directly when ctx has
// none.
func Track(ctx context.Context, org, kind, workID, documentID string, run func(context.Context) error) error {
	if run == nil {
		return errors.New("workqueue: nil tracked function")
	}
	if tracker, ok := trackerFrom(ctx); ok {
		return tracker.Track(ctx, org, kind, workID, documentID, run)
	}
	return run(ctx)
}
