package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// memoryOperations answers as the operation store would, without its rules:
// replay, conflicts, Corpus existence and the cancel and rerun state machine
// are owned by the postgres operations tests and scripts/operation_control.py.
// fail injects the store's answer for the next write.
type memoryOperations struct {
	byKey    map[string]operations.Operation
	byID     map[string]operations.Operation
	fail     error
	resolved string
}

func (m *memoryOperations) AcceptRebuild(_ context.Context, org, corpusID, key string, _ []byte) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	op := operations.Operation{ID: "operation_" + corpusID + "_" + key, Organization: org, Kind: operations.KindProjectionRebuild, CorpusID: corpusID, State: operations.StateQueued, Counters: map[string]int{}, Errors: []operations.Error{}}
	m.byID[op.ID] = op
	return op, nil
}
func (m *memoryOperations) Operation(_ context.Context, _, id string) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	if op, ok := m.byID[id]; ok {
		return op, nil
	}
	return operations.Operation{}, corpus.ErrNotFound
}

func (m *memoryOperations) CancelOperation(_ context.Context, _, id string) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	return m.byID[id], nil
}
func (m *memoryOperations) PauseOperation(_ context.Context, _, id string) (operations.Operation, error) {
	op := m.byID[id]
	op.State = operations.StatePaused
	return op, m.fail
}
func (m *memoryOperations) ResumeOperation(_ context.Context, _, id string) (operations.Operation, error) {
	op := m.byID[id]
	op.State = operations.StateRunning
	return op, m.fail
}
func (m *memoryOperations) AcceptRerun(_ context.Context, org, source, key string, _ []byte) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	src := m.byID[source]
	op := operations.Operation{ID: "operation_rerun_" + key, Organization: org, Kind: src.Kind, CorpusID: src.CorpusID, State: operations.StateQueued, PreviousID: source, Counters: map[string]int{}, Errors: []operations.Error{}}
	m.byID[op.ID] = op
	return op, nil
}

const (
	rebuilder     = "rebuild-operator-token-0123456789abcdef012345"
	rebuildScoped = "rebuild-scoped-b-token-0123456789abcdef012345"
	noRebuild     = "rebuild-denied-token-0123456789abcdef01234567"
	controller    = "operations-writer-token-0123456789abcdef0123"
	backfiller    = "backfill-operator-token-0123456789abcdef0123"
)

func operationServer(t *testing.T, store *memoryOperations) *httptest.Server {
	t.Helper()
	keys := map[string]corpus.Scope{
		rebuilder:     {Organization: "org_a", Actions: []string{"projections:rebuild", "operations:read", "operations:write"}, Corpora: []string{"*"}},
		rebuildScoped: {Organization: "org_a", Actions: []string{"projections:rebuild", "operations:read", "operations:write", "corpora:write"}, Corpora: []string{"corpus_b"}},
		controller:    {Organization: "org_a", Actions: []string{"operations:write"}, Corpora: []string{"*"}},
		backfiller:    {Organization: "org_a", Actions: []string{"operations:write", operations.BackfillPermission}, Corpora: []string{"*"}},
		noRebuild:     {Organization: "org_a", Actions: []string{"corpora:read", "corpora:write"}, Corpora: []string{"*"}},
		configurer:    {Organization: "org_a", Actions: []string{"corpora:write", "operations:write", "operations:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithOperations(operations.Service{Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(checkedAPI(t, handler))
	t.Cleanup(server.Close)
	return server
}

func operationCall(t *testing.T, server *httptest.Server, method, path, token, contentType, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func TestRebuildInitiationAndOperationReads(t *testing.T) {
	store := &memoryOperations{byKey: map[string]operations.Operation{}, byID: map[string]operations.Operation{}}
	server := operationServer(t, store)
	res, op := operationCall(t, server, "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k1"}`)
	if res.StatusCode != 202 || op["state"] != "queued" || op["kind"] != "projection_rebuild" || op["corpus_id"] != "corpus_a" || op["operation_id"] == "" {
		t.Fatalf("initiation %d %v", res.StatusCode, op)
	}
	if _, ok := op["counters"].(map[string]any); !ok {
		t.Fatalf("counters missing: %v", op)
	}
	if errs, ok := op["errors"].([]any); !ok || len(errs) != 0 {
		t.Fatalf("errors missing: %v", op)
	}
	if _, ok := op["result"]; ok {
		t.Fatalf("queued operation has result: %v", op)
	}
	location := res.Header.Get("Location")
	if location != "/v0/operations/"+op["operation_id"].(string) {
		t.Fatalf("Location %q", location)
	}
	res, read := operationCall(t, server, "GET", location, rebuilder, "", "")
	if res.StatusCode != 200 || read["operation_id"] != op["operation_id"] || read["state"] != "queued" {
		t.Fatalf("read %d %v", res.StatusCode, read)
	}
	// A succeeded rebuild names its activated logical generation.
	done := store.byID[op["operation_id"].(string)]
	done.State, done.ResultGenerationID = operations.StateSucceeded, "generation_2"
	store.byID[done.ID] = done
	if _, read = operationCall(t, server, "GET", location, rebuilder, "", ""); read["result"].(map[string]any)["projection_generation_id"] != "generation_2" {
		t.Fatalf("succeeded read %v", read)
	}
	// A failed Operation renders each error with its retryability.
	store.byID["operation_failed"] = operations.Operation{ID: "operation_failed", Organization: "org_a", Kind: operations.KindProjectionRebuild, CorpusID: "corpus_a", State: operations.StateFailed, Errors: []operations.Error{{Code: "projection_failed", Message: "embedding outage", Retryable: true}}}
	if _, read = operationCall(t, server, "GET", "/v0/operations/operation_failed", rebuilder, "", ""); read["state"] != "failed" {
		t.Fatalf("failed read %v", read)
	} else if errs, _ := read["errors"].([]any); len(errs) != 1 || errs[0].(map[string]any)["code"] != "projection_failed" || errs[0].(map[string]any)["message"] != "embedding outage" || errs[0].(map[string]any)["retryable"] != true {
		t.Fatalf("failed read errors %v", read)
	}
	for _, tc := range []struct {
		name, method, path, token, contentType, body string
		store                                        error
		status                                       int
		code                                         string
	}{
		{"missing rebuild permission", "POST", "/v0/corpora/corpus_a/rebuilds", noRebuild, "application/json", `{"idempotency_key":"k2"}`, nil, 403, "forbidden"},
		{"Corpus outside key scope", "POST", "/v0/corpora/corpus_a/rebuilds", rebuildScoped, "application/json", `{"idempotency_key":"k2"}`, nil, 404, "not_found"},
		{"absent Corpus", "POST", "/v0/corpora/corpus_missing/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k2"}`, corpus.ErrNotFound, 404, "not_found"},
		{"conflicting key reuse", "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k1"}`, operations.ErrConflict, 409, "idempotency_conflict"},
		{"storage outage", "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k3"}`, errors.New("database unavailable"), 503, "storage_unavailable"},
		{"missing key", "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{}`, nil, 422, "invalid_schema"},
		{"unknown field", "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k2","mode":"x"}`, nil, 422, "invalid_schema"},
		{"method", "GET", "/v0/corpora/corpus_a/rebuilds", rebuilder, "", "", nil, 405, "method_not_allowed"},
		{"read permission", "GET", location, noRebuild, "", "", nil, 403, "forbidden"},
		{"read out of scope", "GET", location, rebuildScoped, "", "", nil, 404, "not_found"},
		{"unknown Operation", "GET", "/v0/operations/operation_missing", rebuilder, "", "", nil, 404, "not_found"},
		{"read storage outage", "GET", location, rebuilder, "", "", errors.New("database unavailable"), 503, "storage_unavailable"},
	} {
		store.fail = tc.store
		res, body := operationCall(t, server, tc.method, tc.path, tc.token, tc.contentType, tc.body)
		if res.StatusCode != tc.status || body["code"] != tc.code || body["retryable"] != (tc.status == 503) {
			t.Errorf("%s: %d %v, want %d %s", tc.name, res.StatusCode, body, tc.status, tc.code)
		}
	}
}

func TestCancelAndRerunOperationRoutes(t *testing.T) {
	store := &memoryOperations{byKey: map[string]operations.Operation{}, byID: map[string]operations.Operation{}}
	server := operationServer(t, store)
	seed := func(id, kind, state string) string {
		store.byID[id] = operations.Operation{ID: id, Organization: "org_a", Kind: kind, CorpusID: "corpus_a", State: state, Counters: map[string]int{}, Errors: []operations.Error{}}
		return id
	}
	running := seed("operation_running", operations.KindProjectionRebuild, operations.StateCancelRequested)
	done := seed("operation_done", operations.KindProjectionRebuild, operations.StateSucceeded)
	future := seed("operation_future", "cold_restoration", operations.StateSucceeded)
	filling := seed("operation_backfill", operations.KindBackfill, operations.StateRunning)

	// Cancel renders the state the store reports and names no new resource.
	res, body := operationCall(t, server, "POST", "/v0/operations/"+running+"/cancel", controller, "application/json", `{"idempotency_key":"c1"}`)
	if res.StatusCode != 202 || body["operation_id"] != running || body["state"] != "cancel_requested" || res.Header.Get("Location") != "" {
		t.Fatalf("cancel %d %v Location %q", res.StatusCode, body, res.Header.Get("Location"))
	}
	// Pause and resume render the state the store reports.
	for action, state := range map[string]string{"pause": "paused", "resume": "running"} {
		res, body = operationCall(t, server, "POST", "/v0/operations/"+filling+"/"+action, backfiller, "application/json", `{"idempotency_key":"p1"}`)
		if res.StatusCode != 202 || body["operation_id"] != filling || body["state"] != state {
			t.Fatalf("%s %d %v", action, res.StatusCode, body)
		}
	}
	// A rerun is a new Operation, linked to its source and located by its own identity.
	res, rerun := operationCall(t, server, "POST", "/v0/operations/"+done+"/rerun", rebuilder, "application/json", `{"idempotency_key":"r1"}`)
	if res.StatusCode != 202 || rerun["operation_id"] == done || rerun["previous_operation_id"] != done {
		t.Fatalf("rerun %d %v", res.StatusCode, rerun)
	}
	if res.Header.Get("Location") != "/v0/operations/"+rerun["operation_id"].(string) {
		t.Fatalf("rerun Location %q", res.Header.Get("Location"))
	}
	for _, tc := range []struct {
		name, method, path, token, contentType, body string
		store                                        error
		status                                       int
		code                                         string
	}{
		{"cancel without operations:write", "POST", "/v0/operations/" + done + "/cancel", noRebuild, "application/json", `{"idempotency_key":"x"}`, nil, 403, "forbidden"},
		{"rerun refused by the service", "POST", "/v0/operations/" + done + "/rerun", controller, "application/json", `{"idempotency_key":"x"}`, nil, 403, "forbidden"},
		{"cancel outside key scope", "POST", "/v0/operations/" + done + "/cancel", rebuildScoped, "application/json", `{"idempotency_key":"x"}`, nil, 404, "not_found"},
		{"kind without control support", "POST", "/v0/operations/" + future + "/cancel", rebuilder, "application/json", `{"idempotency_key":"x"}`, nil, 422, "unsupported_operation_kind"},
		{"non-terminal rerun", "POST", "/v0/operations/" + running + "/rerun", rebuilder, "application/json", `{"idempotency_key":"x"}`, operations.ErrNotTerminal, 409, "operation_not_terminal"},
		{"conflicting rerun key", "POST", "/v0/operations/" + done + "/rerun", rebuilder, "application/json", `{"idempotency_key":"r1"}`, operations.ErrConflict, 409, "idempotency_conflict"},
		{"cancel storage outage", "POST", "/v0/operations/" + done + "/cancel", rebuilder, "application/json", `{"idempotency_key":"x"}`, errors.New("database unavailable"), 503, "storage_unavailable"},
		{"rerun storage outage", "POST", "/v0/operations/" + done + "/rerun", rebuilder, "application/json", `{"idempotency_key":"x"}`, errors.New("database unavailable"), 503, "storage_unavailable"},
		{"cancel missing key", "POST", "/v0/operations/" + done + "/cancel", rebuilder, "application/json", `{}`, nil, 422, "invalid_schema"},
		{"cancel method", "GET", "/v0/operations/" + done + "/cancel", rebuilder, "", "", nil, 405, "method_not_allowed"},
		{"unknown action", "POST", "/v0/operations/" + done + "/suspend", rebuilder, "application/json", `{"idempotency_key":"x"}`, nil, 404, "not_found"},
		{"backfill rerun while another runs", "POST", "/v0/operations/" + done + "/rerun", rebuilder, "application/json", `{"idempotency_key":"x"}`, backfill.ErrInProgress, 409, "backfill_in_progress"},
		{"backfill rerun after its plugin left the plan", "POST", "/v0/operations/" + done + "/rerun", rebuilder, "application/json", `{"idempotency_key":"x"}`, backfill.ErrRegistrationNotActive, 409, "registration_not_active"},
		{"a rebuild cannot pause", "POST", "/v0/operations/" + done + "/pause", backfiller, "application/json", `{"idempotency_key":"x"}`, nil, 422, "unsupported_operation_kind"},
		{"pause without plugins:admin", "POST", "/v0/operations/" + filling + "/pause", controller, "application/json", `{"idempotency_key":"x"}`, nil, 403, "forbidden"},
	} {
		store.fail = tc.store
		res, body := operationCall(t, server, tc.method, tc.path, tc.token, tc.contentType, tc.body)
		if res.StatusCode != tc.status || body["code"] != tc.code || body["retryable"] != (tc.status == 503) {
			t.Errorf("%s: %d %v, want %d %s", tc.name, res.StatusCode, body, tc.status, tc.code)
		}
	}
}
