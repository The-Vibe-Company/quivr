package plugins

import (
	"context"
	"sync/atomic"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// Live is the plugin set a running api or worker follows: the active
// Pipeline Plan, swapped whole when the plan changes (Spec 5). Every lookup
// reads one immutable snapshot, so a call that already resolved its pin
// finishes on it while later calls see the new plan.
type Live struct {
	current atomic.Pointer[snapshot]
}

type snapshot struct {
	plan       string
	set        *PinSet
	extensions *content.ExtensionRegistry
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
	extensions, err := PinsExtensionRegistry(set)
	if err != nil {
		return err
	}
	l.current.Store(&snapshot{plan: plan, set: set, extensions: extensions})
	return nil
}

// Plan is the id of the plan the current set was resolved from.
func (l *Live) Plan() string { return l.current.Load().plan }

// Set is the current plugin set.
func (l *Live) Set() *PinSet { return l.current.Load().set }

// Routed reports whether a Blob media type is routed to a normalizer of the
// current set (content.NormalizerRoutes).
func (l *Live) Routed(mediaType string) bool { return l.Set().Routed(mediaType) }

// Normalizer resolves the normalizer of a media type in the current set.
func (l *Live) Normalizer(mediaType string) (*Pin, RouteConfig, bool) {
	return l.Set().Normalizer(mediaType)
}

// Validate checks extensions against the namespaces the current set owns
// beside the built-in ones (content.ExtensionValidator).
func (l *Live) Validate(ctx context.Context, exts content.Extensions) error {
	return l.current.Load().extensions.Validate(ctx, exts)
}

// Declared reports whether a namespace is built in or owned by a plugin of
// the current set, as retrieval mappings may address it.
func (l *Live) Declared(namespace string) bool {
	return l.current.Load().extensions.Declared(namespace)
}
