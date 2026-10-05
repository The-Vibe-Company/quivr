package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
)

// applyPluginConfiguration records the startup pins in the plugin registry
// and applies the roles they changed since the configuration last applied
// (registry.Reconcile), then resolves the active Pipeline Plan: the plugins
// this process serves. A plan that cannot be resolved (for example a plugin
// this engine version no longer accepts) leaves the process on the pins,
// with an error in the log, until the plan changes.
func applyPluginConfiguration(ctx context.Context, service registry.Service, pins *plugins.PinSet) (string, *plugins.PinSet, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	applied, err := service.Store.ApplyConfiguration(ctx, registry.FromPins(pins))
	if err != nil {
		return "", nil, fmt.Errorf("apply the plugin configuration: %w", err)
	}
	for _, o := range applied.Overridden {
		slog.Warn("the configuration replaces a plugin an operator activated", "role", o.Role, "activated", o.Active, "configured", o.Configured)
	}
	if applied.Whole {
		slog.Warn("the configured plugins replace the whole pipeline plan", "reason", applied.Reason)
	}
	if applied.Changed {
		slog.Info("pipeline plan recorded from the configuration", "plan", applied.Plan, "roles", describeRoles(applied.Roles))
	}
	plan, set, err := resolvePlan(ctx, service.Store)
	switch {
	case errors.Is(err, registry.ErrNoPlan):
		return "", pins, nil
	case err != nil:
		slog.Error("the active pipeline plan cannot be resolved; serving the configured plugins until it changes", "plan", plan, "error", err)
		return plan, pins, nil
	}
	return plan, set, nil
}

// resolvePlan loads the active plan's plugins.
func resolvePlan(ctx context.Context, store registry.Store) (string, *plugins.PinSet, error) {
	plan, members, err := store.ActiveMembers(ctx)
	if err != nil {
		return "", nil, err
	}
	set, _, err := registry.Resolve(plan.Roles, members)
	return plan.ID, set, err
}

// resolvePlanByID loads any plan's plugins, for work pinned to it.
func resolvePlanByID(ctx context.Context, store registry.Store, id string) (*plugins.PinSet, error) {
	plan, members, err := store.PlanMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	set, _, err := registry.Resolve(plan.Roles, members)
	return set, err
}

// WorkStore records the plan each piece of work is pinned to.
type WorkStore interface {
	PinWork(ctx context.Context, kind, org, id, plan string) (pinned string, stopped bool, err error)
	ReleaseWork(ctx context.Context, kind, org, id string) error
	CountUnavailable(ctx context.Context, kind, org, id string) (int, error)
	// WorkStopped reports whether a rollback stopped a piece of work.
	WorkStopped(ctx context.Context, kind, org, id string) (bool, error)
}

// pluginWorkStore provides registration-specific drain and retry accounting.
type pluginWorkStore interface {
	BindIngestionWork(context.Context, string, string, string, string) error
	CountPluginUnavailable(context.Context, string, string, string, string) (int, error)
}

// workPins pins each piece of work to the Pipeline Plan the process follows
// when the work is first seen (Spec 5), in PostgreSQL, so its retries,
// its later activities and a worker restart resolve plugins in that plan.
// A process that follows no plan leaves work unpinned.
type workPins struct {
	store WorkStore
	live  *plugins.Live
	// budget is how many attempts pinned work gets once a plugin of its plan
	// left the active plan and cannot be reached (plugins.Unreachable).
	budget int
}

func (p workPins) Pin(ctx context.Context, kind, org, id string) (context.Context, error) {
	current := p.live.Plan()
	if current == "" {
		return ctx, nil
	}
	plan, stopped, err := p.store.PinWork(ctx, kind, org, id, current)
	if err != nil {
		return ctx, err
	}
	attempt := func(ctx context.Context) (int, error) { return p.store.CountUnavailable(ctx, kind, org, id) }
	// A read that fails keeps the work going: stop is best effort within an
	// attempt, and the next attempt reads the mark again when it is pinned.
	marked := func(ctx context.Context) bool {
		s, err := p.store.WorkStopped(ctx, kind, org, id)
		return err == nil && s
	}
	var bind func(context.Context, string) error
	var pluginAttempt func(context.Context, string) (int, error)
	if store, ok := p.store.(pluginWorkStore); ok {
		bind = func(ctx context.Context, registration string) error {
			return store.BindIngestionWork(ctx, kind, org, id, registration)
		}
		pluginAttempt = func(ctx context.Context, registration string) (int, error) {
			return store.CountPluginUnavailable(ctx, kind, org, id, registration)
		}
	}
	pinned, err := p.live.Pin(ctx, plugins.Work{BindIngestion: bind, PluginAttempt: pluginAttempt, Kind: kind, Organization: org, ID: id, Plan: plan, Stopped: stopped, StopMarked: marked}, attempt, p.budget)
	if err != nil {
		slog.Error("the plan this work is pinned to cannot be resolved; it retries", "kind", kind, "work_id", id, "plan", plan, "error", err)
	}
	return pinned, err
}

func (p workPins) Release(ctx context.Context, kind, org, id string) error {
	return p.store.ReleaseWork(ctx, kind, org, id)
}

// planKinds resolves the connector kinds of the plan a connector run is
// pinned to, built once per plan from that plan's plugins; a run that is not
// pinned uses the process's registry. The process's registry is not used for
// pinned runs even on the current plan: a plan change swaps it just before
// the plan itself.
type planKinds struct {
	live    *plugins.Live
	current *connectors.Registry
	kindsOf func(*plugins.PinSet) []connectors.Connector
	mu      sync.Mutex
	plans   map[string]*connectors.Registry
}

func (k *planKinds) at(ctx context.Context) *connectors.Registry {
	w, ok := plugins.WorkOf(ctx)
	if !ok {
		return k.current
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if r, ok := k.plans[w.Plan]; ok {
		return r
	}
	r, err := connectors.NewRegistry(k.kindsOf(k.live.SetFor(ctx))...)
	if err != nil {
		// Never another plan's provider: the run finds no provider for its
		// kind and fails.
		slog.Error("the connector kinds of the plan this run is pinned to cannot be resolved", "plan", w.Plan, "error", err)
		return nil
	}
	if k.plans == nil {
		k.plans = map[string]*connectors.Registry{}
	}
	k.plans[w.Plan] = r
	return r
}

func describeRoles(roles []registry.Assignment) string {
	parts := make([]string, len(roles))
	for i, a := range roles {
		parts[i] = a.Role + "=" + a.PluginID + "@" + a.Version
	}
	return strings.Join(parts, " ")
}

// planFollower keeps a process on the active Pipeline Plan without restart
// (Spec 5): it polls the active plan's id and, when it changes, resolves the
// new plan and swaps it in. Polling one primary-key read survives database
// reconnects and connection poolers, where LISTEN/NOTIFY needs a dedicated
// session, and a switch a second or two later changes nothing that matters.
type planFollower struct {
	store registry.Store
	live  *plugins.Live
	// apply prepares what else follows the plan (connector kinds) before
	// the swap; an error keeps the current plan.
	apply func(*plugins.PinSet) error
	// followed runs once the process follows a new plan.
	followed func(context.Context, *plugins.PinSet)
	mu       sync.Mutex
	// failed is the last plan id that could not be resolved, logged once.
	failed string
}

// Refresh follows the active plan now.
func (f *planFollower) Refresh(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, err := f.store.ActivePlanID(ctx)
	if err != nil || id == "" || id == f.live.Plan() || id == f.failed {
		return
	}
	plan, set, err := resolvePlan(ctx, f.store)
	if err == nil && plan == id && f.apply != nil {
		err = f.apply(set)
	}
	if err == nil && plan == id {
		err = f.live.Store(plan, set)
	}
	if err != nil {
		f.failed = id
		slog.ErrorContext(ctx, "the active pipeline plan cannot be resolved; keeping the current plugins", "plan", id, "current", f.live.Plan(), "error", err)
		return
	}
	if plan == id {
		slog.InfoContext(ctx, "following pipeline plan", "plan", plan, "plugins", set.Describe())
		if f.followed != nil {
			f.followed(ctx, set)
		}
	}
}

// Run refreshes every interval until ctx ends.
func (f *planFollower) Run(ctx context.Context, interval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			f.Refresh(ctx)
		}
	}
}
