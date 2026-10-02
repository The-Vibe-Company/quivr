package plugins

import (
	"context"
	"fmt"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// Kinds of work pinned to the plan they started on.
const (
	// WorkIngestion is the processing of one ingestion receipt.
	WorkIngestion = "ingestion"
	// WorkConnectorRun is one acquisition run of a Connector Instance.
	WorkConnectorRun = "connector_run"
	// WorkOperation is one Operation, such as a rebuild.
	WorkOperation = "operation"
)

// CodePinnedPluginUnavailable is the diagnostic of work stopped because a
// plugin of the plan it is pinned to could not be reached, or could no longer
// serve it, after that plugin left the active plan. The work is never moved
// to another plugin version.
const CodePinnedPluginUnavailable = "pinned_plugin_unavailable"

// CodePinnedPlanStopped is the diagnostic of work a rollback stopped
// (pinned_work=stop): at its first call to a plugin of its plan that left the
// active plan, the work stops instead of calling that version. The work is
// never moved to another plugin version.
const CodePinnedPlanStopped = "pinned_plan_stopped"

// Work is a piece of work pinned to the Pipeline Plan it started on (Spec 5):
// its retries and restarts resolve every plugin in that plan, never in a
// plan activated since.
type Work struct {
	Kind, Organization, ID string
	// Plan is the id of the plan the work is pinned to.
	Plan string
	// Stopped reports that a rollback stopped the work when it was pinned:
	// it never calls a plugin of its plan that left the active plan again.
	Stopped bool
	// BindIngestion records the routed owner for precise drain accounting.
	BindIngestion func(context.Context, string) error
	// PluginAttempt keeps failure budgets separate for each registration.
	PluginAttempt func(context.Context, string) (int, error)
	// StopMarked reads whether a rollback stopped the work since it was
	// pinned, so an attempt already running stops at its next call to such a
	// plugin; nil reads Stopped only.
	StopMarked func(context.Context) bool

	snapshot *snapshot
	live     *Live
	attempt  func(context.Context) (int, error)
	budget   int
}

type workKey struct{}

// WorkOf returns the pinned work ctx carries.
func WorkOf(ctx context.Context) (*Work, bool) {
	w, ok := ctx.Value(workKey{}).(*Work)
	return w, ok
}

// Unreachable decides what happens to the work ctx carries when a plugin of
// its plan could not be reached, or can no longer serve it. While the plugin
// serves the active plan, the work keeps retrying: a nil diagnostic. Once the
// plugin has left the active plan, each call counts one attempt; when the
// work's budget is spent, the diagnostic names the plan and the plugin, and
// the caller stops the work with it instead of moving it to another plugin
// version. Work that is not pinned always keeps retrying.
func Unreachable(ctx context.Context, pin *Pin, contribution string) (*content.Diagnostic, error) {
	w, ok := WorkOf(ctx)
	if !ok || pin == nil || w.live.Active(pin) {
		return nil, nil
	}
	if w.stopped(ctx) {
		return &content.Diagnostic{Code: CodePinnedPlanStopped, Retryable: true, Plan: w.Plan, Plugin: pin.Manifest.ID, PluginVersion: pin.Manifest.Version, Contribution: contribution,
			Message: fmt.Sprintf("a rollback stopped the work pinned to plan %s before it called %s@%s again; the work was not moved to another plugin version.", w.Plan, pin.Manifest.ID, pin.Manifest.Version)}, nil
	}
	attempts := 1
	if w.PluginAttempt != nil {
		var err error
		if attempts, err = w.PluginAttempt(ctx, pin.Registration); err != nil {
			return nil, err
		}
	} else if w.attempt != nil {
		var err error
		if attempts, err = w.attempt(ctx); err != nil {
			return nil, err
		}
	}
	if attempts < w.budget {
		return nil, nil
	}
	return &content.Diagnostic{Code: CodePinnedPluginUnavailable, Retryable: true, Plan: w.Plan, Plugin: pin.Manifest.ID, PluginVersion: pin.Manifest.Version, Contribution: contribution,
		Message: fmt.Sprintf("%s@%s, named by plan %s, could not be reached or could no longer serve this work after it left the active plan (%d attempts); the work was not moved to another plugin version.", pin.Manifest.ID, pin.Manifest.Version, w.Plan, attempts)}, nil
}

// Stopped reports whether the work ctx carries must not call pin: a rollback
// stopped it and pin has left the active plan. The caller fails the call as
// unavailable, and Unreachable then stops the work.
func Stopped(ctx context.Context, pin *Pin) bool {
	w, ok := WorkOf(ctx)
	return ok && pin != nil && !w.live.Active(pin) && w.stopped(ctx)
}

// stopped reports a stop recorded when the work was pinned or since. It is
// read only for a plugin that left the active plan, so work calling the
// plugins of the active plan never pays for it.
func (w *Work) stopped(ctx context.Context) bool {
	return w.Stopped || (w.StopMarked != nil && w.StopMarked(ctx))
}

// BindIngestion records the owner actually selected by the pinned plan.
func BindIngestion(ctx context.Context, pin *Pin) error {
	if w, ok := WorkOf(ctx); ok && w.BindIngestion != nil && pin != nil && pin.Registration != "" {
		return w.BindIngestion(ctx, pin.Registration)
	}
	return nil
}
