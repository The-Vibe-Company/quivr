package autoscaling

import (
	"context"
	"fmt"
	"time"
)

// Backlog and Backend are the external boundaries required by the control loop.
type Backlog interface {
	Waiting(context.Context) (int64, error)
}
type Backend interface {
	Replicas(context.Context) (int, error)
	SetReplicas(context.Context, int) error
}

type Decision struct {
	Waiting         int64
	Current, Target int
	Outcome         string
}

// Controller reconciles the provider's observed count, never a guessed count.
// One instance must own a target; it is called serially, once per poll.
type Controller struct {
	policy   *Policy
	backlog  Backlog
	backend  Backend
	previous int
}

func NewController(policy *Policy, backlog Backlog, backend Backend) *Controller {
	return &Controller{policy: policy, backlog: backlog, backend: backend, previous: -1}
}

// Step returns unknown counts as -1. Failed reads never trigger a write; a
// failed write keeps the last observed count and clears downscale evidence.
func (c *Controller) Step(ctx context.Context, now time.Time) (Decision, error) {
	d := Decision{Waiting: -1, Current: -1, Target: -1, Outcome: "replicas_error"}
	current, err := c.backend.Replicas(ctx)
	if err != nil {
		c.policy.Reset()
		return d, fmt.Errorf("read replicas: %w", err)
	}
	d.Current = current
	d.Target = current
	if c.previous != current {
		c.policy.Reset()
	}
	c.previous = current
	d.Outcome = "backlog_error"
	waiting, err := c.backlog.Waiting(ctx)
	if err != nil {
		c.policy.Reset()
		return d, fmt.Errorf("read backlog: %w", err)
	}
	d.Waiting = waiting
	desired, err := c.policy.Decide(waiting, current, now)
	if err != nil {
		c.policy.Reset()
		return d, err
	}
	d.Target = desired
	d.Outcome = "hold"
	if desired == current {
		return d, nil
	}
	d.Outcome = "update_error"
	if err = c.backend.SetReplicas(ctx, desired); err != nil {
		c.policy.Reset()
		return d, fmt.Errorf("set replicas: %w", err)
	}
	c.previous = desired
	d.Outcome = "scaled"
	return d, nil
}
