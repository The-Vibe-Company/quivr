package plugins

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// Live is the plugin set a running api or worker follows: the active
// Pipeline Plan, swapped whole when the plan changes (Spec 5). Every lookup
// reads one immutable snapshot, so a call that already resolved its pin
// finishes on it while later calls see the new plan. Work pinned to a plan
// (Pin) resolves every lookup in that plan instead.
type Live struct {
	current atomic.Pointer[snapshot]
	// Resolve loads a plan that is not the current one, for work pinned to
	// it; nil resolves only the current plan.
	Resolve func(ctx context.Context, plan string) (*PinSet, error)
	mu      sync.Mutex
	// plans caches at most 32 historical snapshots in least-recently-used
	// order. Work contexts retain their snapshots after eviction.
	plans     map[string]*snapshot
	planOrder []string
}

type snapshot struct {
	plan       string
	set        *PinSet
	extensions *content.ExtensionRegistry
	// registrations are the registration ids of the set's pins.
	registrations map[string]bool
}

func newSnapshot(plan string, set *PinSet) (*snapshot, error) {
	extensions, err := PinsExtensionRegistry(set)
	if err != nil {
		return nil, err
	}
	registrations := map[string]bool{}
	for _, pin := range set.Pins() {
		if pin.Registration != "" {
			registrations[pin.Registration] = true
		}
	}
	return &snapshot{plan: plan, set: set, extensions: extensions, registrations: registrations}, nil
}

// NewLive follows set, resolved from plan. It fails when two pins claim the
// same extension namespace.
func NewLive(plan string, set *PinSet) (*Live, error) {
	l := &Live{}
	return l, l.Store(plan, set)
}

// Store swaps in set, resolved from plan; the previous set stays with the
// calls that already hold it.
func (l *Live) Store(plan string, set *PinSet) error {
	s, err := newSnapshot(plan, set)
	if err != nil {
		return err
	}
	l.current.Store(s)
	return nil
}

// at is the snapshot a lookup made under ctx resolves in: the plan of the
// work ctx carries, or else the current one.
func (l *Live) at(ctx context.Context) *snapshot {
	if w, ok := WorkOf(ctx); ok && w.live == l {
		return w.snapshot
	}
	return l.current.Load()
}

// Plan is the id of the plan the current set was resolved from.
func (l *Live) Plan() string { return l.current.Load().plan }

// Set is the current plugin set.
func (l *Live) Set() *PinSet { return l.current.Load().set }

// SetFor is the set a lookup made under ctx resolves in: the plan of the
// work ctx carries, or else the current set.
func (l *Live) SetFor(ctx context.Context) *PinSet { return l.at(ctx).set }

// Routed reports whether a Blob media type is routed to a normalizer
// (content.NormalizerRoutes), in the plan of the work ctx carries.
func (l *Live) Routed(ctx context.Context, mediaType string) bool {
	return l.at(ctx).set.Routed(mediaType)
}

// Normalizer resolves the normalizer of a media type, in the plan of the
// work ctx carries.
func (l *Live) Normalizer(ctx context.Context, mediaType string) (*Pin, RouteConfig, bool) {
	return l.at(ctx).set.Normalizer(mediaType)
}

// Validate checks extensions against the namespaces the plan's plugins own
// beside the built-in ones (content.ExtensionValidator).
func (l *Live) Validate(ctx context.Context, exts content.Extensions) error {
	return l.at(ctx).extensions.Validate(ctx, exts)
}

// Declared reports whether a namespace is built in or owned by a plugin of
// the current set, as retrieval mappings may address it.
func (l *Live) Declared(namespace string) bool {
	return l.current.Load().extensions.Declared(namespace)
}

// Active reports whether a pin's registration serves the current plan. A
// pin loaded from the configuration rather than the registry counts as
// active.
func (l *Live) Active(pin *Pin) bool {
	return pin == nil || pin.Registration == "" || l.current.Load().registrations[pin.Registration]
}

// snapshotOf resolves a plan: the current one, a cached one, or Resolve.
func (l *Live) snapshotOf(ctx context.Context, plan string) (*snapshot, error) {
	if current := l.current.Load(); current.plan == plan {
		return current, nil
	}
	l.mu.Lock()
	cached := l.plans[plan]
	if cached != nil {
		l.remember(plan, cached)
	}
	l.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	if l.Resolve == nil {
		return nil, fmt.Errorf("pipeline plan %s is not the current plan and cannot be resolved", plan)
	}
	set, err := l.Resolve(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("pipeline plan %s: %w", plan, err)
	}
	s, err := newSnapshot(plan, set)
	if err != nil {
		return nil, fmt.Errorf("pipeline plan %s: %w", plan, err)
	}
	l.mu.Lock()
	// Concurrent resolutions of the same immutable plan share one snapshot.
	if cached := l.plans[plan]; cached != nil {
		s = cached
	}
	l.remember(plan, s)
	l.mu.Unlock()
	return s, nil
}

// remember runs with mu held. Eviction drops only the cache reference;
// already pinned work owns its snapshot until its context is released.
func (l *Live) remember(plan string, s *snapshot) {
	if l.plans == nil {
		l.plans = map[string]*snapshot{}
	}
	if i := slices.Index(l.planOrder, plan); i >= 0 {
		l.planOrder = slices.Delete(l.planOrder, i, i+1)
	}
	l.plans[plan] = s
	l.planOrder = append(l.planOrder, plan)
	if len(l.planOrder) > 32 {
		delete(l.plans, l.planOrder[0])
		l.planOrder = slices.Delete(l.planOrder, 0, 1)
	}
}

// Pin returns ctx carrying work pinned to work.Plan: every lookup of this
// Live made under it resolves in that plan, whatever the current one.
// attempt durably counts one attempt that found a plugin of the plan
// unreachable after it left the active plan, and budget is how many such
// attempts the work gets (Unreachable).
func (l *Live) Pin(ctx context.Context, work Work, attempt func(context.Context) (int, error), budget int) (context.Context, error) {
	s, err := l.snapshotOf(ctx, work.Plan)
	if err != nil {
		return ctx, err
	}
	work.snapshot, work.live, work.attempt, work.budget = s, l, attempt, budget
	return context.WithValue(ctx, workKey{}, &work), nil
}
