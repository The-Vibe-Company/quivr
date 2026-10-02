package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

const evaluationBacklogPath = "/v0/admin/subscriptions/evaluation-backlog"
const evaluationRetirementsPath = "/v0/admin/subscriptions/evaluation-retirements"

func (a *API) evaluationAdministrationRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	path := r.URL.Path
	if path != evaluationBacklogPath && path != evaluationRetirementsPath && !strings.HasPrefix(path, evaluationRetirementsPath+"/") {
		return false
	}
	if !scope.Allows(monitoring.MigrationAction) {
		writeError(w, publicerr.Forbidden, nil)
		return true
	}
	switch {
	case path == evaluationBacklogPath && r.Method == http.MethodGet:
		query := r.URL.Query()
		for key, values := range query {
			if len(values) != 1 || (key != "limit" && key != "after") || (key == "after" && (values[0] == "" || len(values[0]) > 256)) {
				writeError(w, publicerr.InvalidQuery, nil)
				return true
			}
		}
		limit, ok := pageLimit(w, query, monitoring.DefaultMigrationLimit, monitoring.MaxMigrationLimit)
		if !ok {
			return true
		}
		out, err := a.Monitoring.EvaluationBacklog(r.Context(), scope, query.Get("after"), limit)
		if err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
		} else {
			send(w, 200, out)
		}
	case path == evaluationRetirementsPath && r.Method == http.MethodPost:
		var body transport.EvaluationRetirementRequest
		raw, payload, ok := readJSON(w, r, maxRequestBytes)
		if !ok {
			return true
		}
		if object, ok := raw.(map[string]any); ok {
			if value, present := object["limit"]; present {
				number, valid := value.(json.Number)
				limit, err := number.Int64()
				if !valid || err != nil || limit < 1 || limit > monitoring.MaxMigrationLimit {
					writeError(w, publicerr.InvalidLimit, nil)
					return true
				}
			}
		}
		if a.evaluationRetirementSchema.Validate(raw) != nil || json.Unmarshal(payload, &body) != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return true
		}
		input := monitoring.EvaluationRetirementInput{Key: body.Key, PluginID: body.PluginId, Version: body.Version, Reason: body.Reason, DryRun: body.DryRun}
		if body.Limit != nil {
			input.Limit = *body.Limit
		}
		out, err := a.Monitoring.RetireEvaluations(r.Context(), scope, input)
		if err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
		} else {
			if !out.DryRun {
				w.Header().Set("Location", evaluationRetirementsPath+"/"+out.ID)
			}
			send(w, 200, out)
		}
	case strings.HasPrefix(path, evaluationRetirementsPath+"/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, evaluationRetirementsPath+"/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, publicerr.NotFound, nil)
			return true
		}
		out, err := a.Monitoring.EvaluationRetirement(r.Context(), scope, id)
		if err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
		} else {
			send(w, 200, out)
		}
	default:
		writeError(w, publicerr.MethodNotAllowed, nil)
	}
	return true
}
