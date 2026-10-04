package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

const evaluationBacklogPath = "/v0/admin/subscriptions/evaluation-backlog"
const evaluationRetirementsPath = "/v0/admin/subscriptions/evaluation-retirements"

func (a *API) handleListEvaluationBacklog(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	out, err := a.Monitoring.EvaluationBacklog(r.Context(), scope, "", 0, func() (string, int, error) {
		query := r.URL.Query()
		for key, values := range query {
			if len(values) != 1 || (key != "limit" && key != "after") || (key == "after" && (values[0] == "" || len(values[0]) > 256)) {
				writeError(w, publicerr.InvalidQuery, nil)
				return "", 0, errResponseWritten
			}
		}
		limit, ok := pageLimit(w, query, monitoring.DefaultMigrationLimit, monitoring.MaxMigrationLimit)
		if !ok {
			return "", 0, errResponseWritten
		}

		return query.Get("after"), limit, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
	} else {
		send(w, 200, out)
	}
}

func (a *API) handleRetireEvaluations(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var body transport.EvaluationRetirementRequest
	out, err := a.Monitoring.RetireEvaluations(r.Context(), scope, monitoring.EvaluationRetirementInput{}, func() (monitoring.EvaluationRetirementInput, error) {
		raw, payload, ok := readJSON(w, r, maxRequestBytes)
		if !ok {
			return monitoring.EvaluationRetirementInput{}, errResponseWritten
		}
		if object, ok := raw.(map[string]any); ok {
			if value, present := object["limit"]; present {
				number, valid := value.(json.Number)
				limit, err := number.Int64()
				if !valid || err != nil || limit < 1 || limit > monitoring.MaxMigrationLimit {
					writeError(w, publicerr.InvalidLimit, nil)
					return monitoring.EvaluationRetirementInput{}, errResponseWritten
				}
			}
		}
		if a.schemas["EvaluationRetirementRequest"].Validate(raw) != nil || json.Unmarshal(payload, &body) != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return monitoring.EvaluationRetirementInput{}, errResponseWritten
		}
		input := monitoring.EvaluationRetirementInput{Key: body.Key, PluginID: body.PluginId, Version: body.Version, Reason: body.Reason, DryRun: body.DryRun}
		if body.Limit != nil {
			input.Limit = *body.Limit
		}

		return input, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
	} else {
		if !out.DryRun {
			w.Header().Set("Location", evaluationRetirementsPath+"/"+out.ID)
		}
		send(w, 200, out)
	}
}

func (a *API) handleGetEvaluationRetirement(w http.ResponseWriter, r *http.Request, scope corpus.Scope, retirementID string) {
	out, err := a.Monitoring.EvaluationRetirement(r.Context(), scope, "", func() (string, error) {
		if retirementID == "" || strings.Contains(retirementID, "/") {
			writeError(w, publicerr.NotFound, nil)
			return "", errResponseWritten
		}
		return retirementID, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
	} else {
		send(w, 200, out)
	}
}

// evaluationAdministrationFallback preserves the old router's authorization
// boundary for recognized paths with an unsupported method. The service is
// called before the 405 is written, so forbidden requests retain precedence.
func (a *API) evaluationAdministrationFallback(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	_, err := a.Monitoring.EvaluationBacklog(r.Context(), scope, "", 0, func() (string, int, error) {
		writeError(w, publicerr.MethodNotAllowed, nil)
		return "", 0, errResponseWritten
	})
	if err != nil && !errors.Is(err, errResponseWritten) {
		writeError(w, err, publicerr.StorageUnavailable)
	}
}
