package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

const evaluatorMigrationsPath = "/v0/admin/subscriptions/evaluator-migrations"

func (a *API) migrateEvaluators(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var in transport.SubscriptionEvaluatorMigrationRequest
	m, err := a.Monitoring.MigrateEvaluator(r.Context(), scope, monitoring.EvaluatorMigrationInput{}, func() (monitoring.EvaluatorMigrationInput, error) {
		if !decodeInto(w, r, a.schemas["SubscriptionEvaluatorMigrationRequest"], &in) {
			return monitoring.EvaluatorMigrationInput{}, errResponseWritten
		}
		request := monitoring.EvaluatorMigrationInput{PluginID: in.PluginId, FromVersion: in.FromVersion, DryRun: in.DryRun}
		if in.Limit != nil {
			request.Limit = *in.Limit
		}
		if in.After != nil {
			request.After = *in.After
		}

		return request, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	if !m.DryRun {
		slog.InfoContext(r.Context(), "subscriptions moved to another alert-rule version", "organization", scope.Organization, "plugin", m.PluginID, "from", m.FromVersion, "to", m.ToVersion, "moved", len(m.Moved), "refused", len(m.Refused))
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
