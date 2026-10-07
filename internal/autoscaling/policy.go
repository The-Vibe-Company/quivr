// Package autoscaling controls stateless workers from a document backlog.
// It has no orchestrator-specific dependencies.
package autoscaling

import (
	"errors"
	"time"
)

type Config struct {
	Min, Max            int
	DocumentsPerReplica int64
	DownscaleWindow     time.Duration
}

type recommendation struct {
	at       time.Time
	replicas int
}

// Policy scales up immediately and down to the highest recommendation still
// inside the stabilization window. A fresh policy first holds current capacity
// for one window, so restarts cannot discard evidence and scale down early.
// A Policy is used by one serial control loop.
type Policy struct {
	config  Config
	history []recommendation
}

func NewPolicy(config Config) (*Policy, error) {
	if config.Min < 1 || config.Max < config.Min || config.DocumentsPerReplica < 1 || config.DownscaleWindow < 0 {
		return nil, errors.New("invalid autoscaling policy")
	}
	return &Policy{config: config}, nil
}

// Reset discards downscale evidence after an observation or write failure.
func (p *Policy) Reset() { p.history = nil }

func (p *Policy) Decide(waiting int64, current int, now time.Time) (int, error) {
	if waiting < 0 || current < 0 {
		return current, errors.New("invalid backlog or replica count")
	}
	// Division before addition avoids overflowing for a large backlog.
	desired := waiting / p.config.DocumentsPerReplica
	if waiting%p.config.DocumentsPerReplica != 0 {
		desired++
	}
	if desired < int64(p.config.Min) {
		desired = int64(p.config.Min)
	}
	if desired > int64(p.config.Max) {
		desired = int64(p.config.Max)
	}
	target := int(desired)
	if p.config.DownscaleWindow == 0 {
		return target, nil
	}
	if len(p.history) == 0 {
		p.history = append(p.history, recommendation{now, current})
	}
	kept := p.history[:0]
	for _, r := range p.history {
		if now.Sub(r.at) < p.config.DownscaleWindow {
			kept = append(kept, r)
		}
	}
	p.history = append(kept, recommendation{now, target})
	for _, r := range p.history {
		if r.replicas > target {
			target = r.replicas
		}
	}
	// Stabilization must never create capacity above the present count unless
	// today's backlog needs it (e.g. after a manual capacity reduction).
	if int(desired) >= current {
		target = int(desired)
	} else if target > current {
		target = current
	}
	return target, nil
}
