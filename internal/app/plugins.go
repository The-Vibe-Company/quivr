package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
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
	mu    sync.Mutex
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
		slog.Error("the active pipeline plan cannot be resolved; keeping the current plugins", "plan", id, "current", f.live.Plan(), "error", err)
		return
	}
	if plan == id {
		slog.Info("following pipeline plan", "plan", plan, "plugins", set.Describe())
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
