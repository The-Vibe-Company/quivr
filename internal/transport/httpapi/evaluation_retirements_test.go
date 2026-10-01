package httpapi_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

type evaluationAdministration struct {
	org     string
	corpora []string
	input   monitoring.EvaluationRetirementInput
	err     error
}

func (s *evaluationAdministration) EvaluationBacklog(_ context.Context, org string, corpora []string, _ string, _ int) ([]monitoring.EvaluationCounts, error) {
	s.org, s.corpora = org, corpora
	return []monitoring.EvaluationCounts{{PluginID: "alert-rules", Version: "0.1.0", Pending: 2, Unavailable: 2, Erroring: 2}}, s.err
}

func (s *evaluationAdministration) RetireEvaluations(_ context.Context, org string, corpora []string, input monitoring.EvaluationRetirementInput) (monitoring.EvaluationRetirement, error) {
	s.org, s.corpora, s.input = org, corpora, input
	return monitoring.EvaluationRetirement{EvaluationRetirementInput: input, ID: "retirement_1", Outcome: monitoring.OutcomeEvaluatorRetired, Items: []monitoring.RetiredEvaluation{}}, s.err
}

func (s *evaluationAdministration) EvaluationRetirement(_ context.Context, org string, corpora []string, _ string) (monitoring.EvaluationRetirement, error) {
	s.org, s.corpora = org, corpora
	return monitoring.EvaluationRetirement{}, s.err
}

func TestEvaluationRetirementHTTPAuthorizationAndContract(t *testing.T) {
	store := &evaluationAdministration{}
	admin := "retirement-admin-token-0123456789abcdef0123456789"
	keys := map[string]corpus.Scope{
		admin:             {Organization: "org_a", Corpora: []string{"corpus_a"}, Actions: []string{"plugins:admin"}},
		organizationAdmin: {Organization: "org_a", Corpora: []string{"*"}, Actions: []string{"monitoring:read", "monitoring:write"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithMonitoring(monitoring.Service{Evaluations: store}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base := "/v0/admin/subscriptions/"
	version := "1.0.0+" + strings.Repeat("a", 122)
	body := `{"key":"retire","plugin_id":"alert-rules","version":"` + version + `","reason":"Retire old build","dry_run":false}`
	res, result := operationCall(t, server, "POST", base+"evaluation-retirements", admin, "application/json", body)
	if res.StatusCode != 200 || res.Header.Get("Location") != base+"evaluation-retirements/retirement_1" || result["outcome"] != "evaluator_retired" || store.org != "org_a" || !reflect.DeepEqual(store.corpora, []string{"corpus_a"}) || store.input.Limit != 100 || store.input.Reason != "Retire old build" || store.input.Version != version {
		t.Fatalf("retirement transport: %d %v scope=%s/%v input=%+v", res.StatusCode, result, store.org, store.corpora, store.input)
	}
	conforms(t, "EvaluationRetirement", result)
	res, result = operationCall(t, server, "GET", base+"evaluation-backlog", admin, "", "")
	if res.StatusCode != 200 || len(result["items"].([]any)) != 1 {
		t.Fatalf("backlog transport: %d %v", res.StatusCode, result)
	}
	conforms(t, "EvaluationBacklogPage", result)
	for _, test := range []struct {
		method, path, token, body string
		storeError                error
		status                    int
		code                      string
	}{
		{"POST", "evaluation-retirements", organizationAdmin, body, nil, 403, "forbidden"},
		{"GET", "evaluation-backlog", organizationAdmin, "", nil, 403, "forbidden"},
		{"GET", "evaluation-retirements/retirement_1", organizationAdmin, "", nil, 403, "forbidden"},
		{"POST", "evaluation-retirements", admin, `{"key":"k","plugin_id":"alert-rules","version":"0.1.0","reason":"Reason"}`, nil, 422, "invalid_schema"},
		{"POST", "evaluation-retirements", admin, `{"key":"k","plugin_id":"alert-rules","version":"0.1.0","reason":"   ","dry_run":false}`, nil, 422, "invalid_input"},
		{"POST", "evaluation-retirements", admin, `{"key":"k","plugin_id":"alert-rules","version":"0.1.0","reason":"Reason","dry_run":false,"limit":501}`, nil, 422, "invalid_limit"},
		{"GET", "evaluation-backlog?limit=", admin, "", nil, 422, "invalid_limit"},
		{"GET", "evaluation-backlog?limit=1&limit=2", admin, "", nil, 422, "invalid_query"},
		{"GET", "evaluation-backlog?unknown=1", admin, "", nil, 422, "invalid_query"},
		{"GET", "evaluation-backlog?after=", admin, "", nil, 422, "invalid_query"},
		{"POST", "evaluation-retirements", admin, body, monitoring.ErrConflict, 409, "idempotency_conflict"},
		{"GET", "evaluation-retirements/hidden", admin, "", monitoring.ErrNotFound, 404, "not_found"},
		{"POST", "evaluation-retirements", admin, body, errors.New("database unavailable"), 503, "storage_unavailable"},
		{"DELETE", "evaluation-retirements", admin, "", nil, 405, "method_not_allowed"},
	} {
		store.err = test.storeError
		res, result := operationCall(t, server, test.method, base+test.path, test.token, "application/json", test.body)
		if res.StatusCode != test.status || result["code"] != test.code {
			t.Fatalf("%s %s: %d %v, want %d %s", test.method, test.path, res.StatusCode, result, test.status, test.code)
		}
	}
}
