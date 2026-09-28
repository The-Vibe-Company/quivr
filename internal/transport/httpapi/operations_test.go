package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

type memoryOperations struct {
	byKey map[string]operations.Operation
	byID  map[string]operations.Operation
	fail  error
}

func (m *memoryOperations) AcceptRebuild(_ context.Context, org, corpusID, key string, canonical []byte) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	if corpusID != "corpus_a" && corpusID != "corpus_b" {
		return operations.Operation{}, corpus.ErrNotFound
	}
	if string(canonical) != `{"idempotency_key":"`+key+`"}` {
		return operations.Operation{}, errors.New("unexpected canonical request " + string(canonical))
	}
	if op, ok := m.byKey[corpusID+"/"+key]; ok {
		return op, nil
	}
	op := operations.Operation{ID: "operation_" + corpusID + "_" + key, Organization: org, Kind: operations.KindProjectionRebuild, CorpusID: corpusID, State: operations.StateQueued, Counters: map[string]int{}, Errors: []operations.Error{}}
	m.byKey[corpusID+"/"+key], m.byID[op.ID] = op, op
	return op, nil
}
func (m *memoryOperations) Operation(_ context.Context, org, id string) (operations.Operation, error) {
	if op, ok := m.byID[id]; ok && op.Organization == org {
		return op, nil
	}
	return operations.Operation{}, corpus.ErrNotFound
}

func (m *memoryOperations) CancelOperation(_ context.Context, org, id string) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	op := m.byID[id]
	switch op.State {
	case operations.StateQueued:
		op.State = operations.StateCanceled
	case operations.StateRunning:
		op.State = operations.StateCancelRequested
	}
	m.byID[id] = op
	return op, nil
}
func (m *memoryOperations) AcceptRerun(_ context.Context, org, source, key string, canonical []byte) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	if op, ok := m.byKey["rerun/"+source+"/"+key]; ok {
		return op, nil
	}
	src := m.byID[source]
	if !operations.Terminal(src.State) {
		return operations.Operation{}, operations.ErrNotTerminal
	}
	op := operations.Operation{ID: "operation_rerun_" + key, Organization: org, Kind: src.Kind, CorpusID: src.CorpusID, State: operations.StateQueued, PreviousID: source, Counters: map[string]int{}, Errors: []operations.Error{}}
	m.byKey["rerun/"+source+"/"+key], m.byID[op.ID] = op, op
	return op, nil
}

const (
	rebuilder     = "rebuild-operator-token-0123456789abcdef012345"
	rebuildScoped = "rebuild-scoped-b-token-0123456789abcdef012345"
	noRebuild     = "rebuild-denied-token-0123456789abcdef01234567"
	controller    = "operations-writer-token-0123456789abcdef0123"
)

func operationServer(t *testing.T, store *memoryOperations) *httptest.Server {
	t.Helper()
	keys := map[string]corpus.Scope{
		rebuilder:     {Organization: "org_a", Actions: []string{"projections:rebuild", "operations:read", "operations:write"}, Corpora: []string{"*"}},
		rebuildScoped: {Organization: "org_a", Actions: []string{"projections:rebuild", "operations:read", "operations:write"}, Corpora: []string{"corpus_b"}},
		controller:    {Organization: "org_a", Actions: []string{"operations:write"}, Corpora: []string{"*"}},
		noRebuild:     {Organization: "org_a", Actions: []string{"corpora:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithOperations(operations.Service{Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
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

func TestRebuildInitiationReplayAndOperationReads(t *testing.T) {
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
	res, replay := operationCall(t, server, "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k1"}`)
	if res.StatusCode != 202 || replay["operation_id"] != op["operation_id"] || res.Header.Get("Location") != location {
		t.Fatalf("replay %d %v", res.StatusCode, replay)
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
	for _, tc := range []struct {
		name, method, path, token, contentType, body string
		status                                       int
		code                                         string
	}{
		{"missing rebuild permission", "POST", "/v0/corpora/corpus_a/rebuilds", noRebuild, "application/json", `{"idempotency_key":"k2"}`, 403, "forbidden"},
		{"Corpus outside key scope", "POST", "/v0/corpora/corpus_a/rebuilds", rebuildScoped, "application/json", `{"idempotency_key":"k2"}`, 404, "not_found"},
		{"absent Corpus", "POST", "/v0/corpora/corpus_missing/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k2"}`, 404, "not_found"},
		{"missing key", "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{}`, 422, "invalid_schema"},
		{"unknown field", "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k2","mode":"x"}`, 422, "invalid_schema"},
		{"media type", "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "text/plain", `{"idempotency_key":"k2"}`, 415, "unsupported_media_type"},
		{"method", "GET", "/v0/corpora/corpus_a/rebuilds", rebuilder, "", "", 405, "method_not_allowed"},
		{"read permission", "GET", location, noRebuild, "", "", 403, "forbidden"},
		{"read out of scope", "GET", location, rebuildScoped, "", "", 404, "not_found"},
		{"unknown Operation", "GET", "/v0/operations/operation_missing", rebuilder, "", "", 404, "not_found"},
	} {
		res, body := operationCall(t, server, tc.method, tc.path, tc.token, tc.contentType, tc.body)
		if res.StatusCode != tc.status || body["code"] != tc.code {
			t.Errorf("%s: %d %v, want %d %s", tc.name, res.StatusCode, body, tc.status, tc.code)
		}
	}
	store.fail = operations.ErrConflict
	if res, body := operationCall(t, server, "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k1"}`); res.StatusCode != 409 || body["code"] != "idempotency_conflict" {
		t.Fatalf("conflict %d %v", res.StatusCode, body)
	}
	store.fail = errors.New("database unavailable")
	if res, body := operationCall(t, server, "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"k3"}`); res.StatusCode != 503 || body["retryable"] != true {
		t.Fatalf("storage failure %d %v", res.StatusCode, body)
	}
}

func TestCancelAndRerunOperationRoutes(t *testing.T) {
	store := &memoryOperations{byKey: map[string]operations.Operation{}, byID: map[string]operations.Operation{}}
	server := operationServer(t, store)
	accept := func(key string) string {
		t.Helper()
		_, op := operationCall(t, server, "POST", "/v0/corpora/corpus_a/rebuilds", rebuilder, "application/json", `{"idempotency_key":"`+key+`"}`)
		return op["operation_id"].(string)
	}
	queued, running := accept("queued"), accept("running")
	op := store.byID[running]
	op.State = operations.StateRunning
	store.byID[running] = op

	// Queued cancels immediately; running requests cancellation; repeats return state.
	for _, tc := range []struct{ id, key, want string }{{queued, "c1", "canceled"}, {queued, "c2", "canceled"}, {running, "c1", "cancel_requested"}, {running, "c3", "cancel_requested"}} {
		res, body := operationCall(t, server, "POST", "/v0/operations/"+tc.id+"/cancel", controller, "application/json", `{"idempotency_key":"`+tc.key+`"}`)
		if res.StatusCode != 202 || body["operation_id"] != tc.id || body["state"] != tc.want {
			t.Fatalf("cancel %s: %d %v", tc.id, res.StatusCode, body)
		}
	}
	// A non-terminal Operation cannot be rerun.
	if res, body := operationCall(t, server, "POST", "/v0/operations/"+running+"/rerun", rebuilder, "application/json", `{"idempotency_key":"r1"}`); res.StatusCode != 409 || body["code"] != "operation_not_terminal" || body["retryable"] != false {
		t.Fatalf("non-terminal rerun %d %v", res.StatusCode, body)
	}
	// A terminal Operation reruns under a new linked identity, replayed per key.
	res, rerun := operationCall(t, server, "POST", "/v0/operations/"+queued+"/rerun", rebuilder, "application/json", `{"idempotency_key":"r1"}`)
	if res.StatusCode != 202 || rerun["operation_id"] == queued || rerun["previous_operation_id"] != queued || rerun["state"] != "queued" || rerun["corpus_id"] != "corpus_a" || rerun["kind"] != "projection_rebuild" {
		t.Fatalf("rerun %d %v", res.StatusCode, rerun)
	}
	if res.Header.Get("Location") != "/v0/operations/"+rerun["operation_id"].(string) {
		t.Fatalf("rerun Location %q", res.Header.Get("Location"))
	}
	if res, replay := operationCall(t, server, "POST", "/v0/operations/"+queued+"/rerun", rebuilder, "application/json", `{"idempotency_key":"r1"}`); res.StatusCode != 202 || replay["operation_id"] != rerun["operation_id"] {
		t.Fatalf("rerun replay %d %v", res.StatusCode, replay)
	}
	for _, tc := range []struct {
		name, method, path, token, contentType, body string
		status                                       int
		code                                         string
	}{
		{"cancel without operations:write", "POST", "/v0/operations/" + queued + "/cancel", noRebuild, "application/json", `{"idempotency_key":"x"}`, 403, "forbidden"},
		{"rerun without projections:rebuild", "POST", "/v0/operations/" + queued + "/rerun", controller, "application/json", `{"idempotency_key":"x"}`, 403, "forbidden"},
		{"cancel outside key scope", "POST", "/v0/operations/" + queued + "/cancel", rebuildScoped, "application/json", `{"idempotency_key":"x"}`, 404, "not_found"},
		{"rerun outside key scope", "POST", "/v0/operations/" + queued + "/rerun", rebuildScoped, "application/json", `{"idempotency_key":"x"}`, 404, "not_found"},
		{"cancel unknown", "POST", "/v0/operations/operation_missing/cancel", rebuilder, "application/json", `{"idempotency_key":"x"}`, 404, "not_found"},
		{"rerun unknown", "POST", "/v0/operations/operation_missing/rerun", rebuilder, "application/json", `{"idempotency_key":"x"}`, 404, "not_found"},
		{"cancel missing key", "POST", "/v0/operations/" + queued + "/cancel", rebuilder, "application/json", `{}`, 422, "invalid_schema"},
		{"rerun unknown field", "POST", "/v0/operations/" + queued + "/rerun", rebuilder, "application/json", `{"idempotency_key":"x","force":true}`, 422, "invalid_schema"},
		{"cancel malformed", "POST", "/v0/operations/" + queued + "/cancel", rebuilder, "application/json", `{`, 400, "malformed_json"},
		{"cancel media type", "POST", "/v0/operations/" + queued + "/cancel", rebuilder, "text/plain", `{"idempotency_key":"x"}`, 415, "unsupported_media_type"},
		{"cancel method", "GET", "/v0/operations/" + queued + "/cancel", rebuilder, "", "", 405, "method_not_allowed"},
		{"rerun method", "DELETE", "/v0/operations/" + queued + "/rerun", rebuilder, "", "", 405, "method_not_allowed"},
		{"unknown action", "POST", "/v0/operations/" + queued + "/pause", rebuilder, "application/json", `{"idempotency_key":"x"}`, 404, "not_found"},
	} {
		res, body := operationCall(t, server, tc.method, tc.path, tc.token, tc.contentType, tc.body)
		if res.StatusCode != tc.status || body["code"] != tc.code {
			t.Errorf("%s: %d %v, want %d %s", tc.name, res.StatusCode, body, tc.status, tc.code)
		}
	}
	store.fail = operations.ErrConflict
	if res, body := operationCall(t, server, "POST", "/v0/operations/"+queued+"/rerun", rebuilder, "application/json", `{"idempotency_key":"r1"}`); res.StatusCode != 409 || body["code"] != "idempotency_conflict" {
		t.Fatalf("rerun conflict %d %v", res.StatusCode, body)
	}
	store.fail = errors.New("database unavailable")
	for _, action := range []string{"cancel", "rerun"} {
		if res, body := operationCall(t, server, "POST", "/v0/operations/"+queued+"/"+action, rebuilder, "application/json", `{"idempotency_key":"r9"}`); res.StatusCode != 503 || body["retryable"] != true {
			t.Fatalf("%s storage failure %d %v", action, res.StatusCode, body)
		}
	}
}
