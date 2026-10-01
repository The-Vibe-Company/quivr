package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

const evaluatorMigrationsPath = "/v0/admin/subscriptions/evaluator-migrations"

// evaluatorMigrationRoutes serves POST /v0/admin/subscriptions/evaluator-migrations
// (plugins:admin): moving an Organization's Subscriptions to the alert-rule
// version the active plan serves (THE-805).
func (a *API) evaluatorMigrationRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.URL.Path != evaluatorMigrationsPath {
		return false
	}
	switch {
	case r.Method != "POST":
		failure(w, 405, "method_not_allowed")
	case !scope.Allows(monitoring.MigrationAction):
		failure(w, 403, "forbidden")
	case a.Monitoring.Moves == nil:
		failure(w, 404, "not_found")
	default:
		a.migrateEvaluators(w, r, scope)
	}
	return true
}

func (a *API) migrateEvaluators(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var in transport.SubscriptionEvaluatorMigrationRequest
	if !decodeInto(w, r, a.evaluatorMigrationSchema, &in) {
		return
	}
	request := monitoring.EvaluatorMigrationInput{PluginID: in.PluginId, FromVersion: in.FromVersion, DryRun: in.DryRun}
	if in.Limit != nil {
		request.Limit = *in.Limit
	}
	if in.After != nil {
		request.After = *in.After
	}
	m, err := a.Monitoring.MigrateEvaluator(r.Context(), scope, request)
	if err != nil {
		monitoringFailure(w, err)
		return
	}
	if !m.DryRun {
		slog.Info("subscriptions moved to another alert-rule version", "organization", scope.Organization, "plugin", m.PluginID, "from", m.FromVersion, "to", m.ToVersion, "moved", len(m.Moved), "refused", len(m.Refused))
	}
	out := transport.SubscriptionEvaluatorMigration{PluginId: m.PluginID, FromVersion: m.FromVersion, ToVersion: m.ToVersion, DryRun: m.DryRun, NextAfter: optionalString(m.Next),
		Moved: make([]transport.SubscriptionEvaluatorMove, 0, len(m.Moved)), Refused: make([]transport.SubscriptionEvaluatorRefusal, 0, len(m.Refused))}
	for _, moved := range m.Moved {
		out.Moved = append(out.Moved, transport.SubscriptionEvaluatorMove{SubscriptionId: moved.SubscriptionID, FromVersionId: moved.FromVersionID, VersionId: optionalString(moved.VersionID)})
	}
	for _, refused := range m.Refused {
		out.Refused = append(out.Refused, transport.SubscriptionEvaluatorRefusal{SubscriptionId: refused.SubscriptionID, VersionId: refused.VersionID, Code: refused.Code, Field: optionalString(refused.Pointer), Message: optionalString(refused.Message)})
	}
	send(w, 200, out)
}
