package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
)

func (m *memoryOperations) AcceptRetrievalConfiguration(_ context.Context, org, corpusID, key string, _ []byte, resolved []byte) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	m.resolved = string(resolved)
	op := operations.Operation{ID: "operation_config_" + key, Organization: org, Kind: operations.KindRetrievalConfiguration, CorpusID: corpusID, State: operations.StateQueued, Counters: map[string]int{}, Errors: []operations.Error{}}
	m.byID[op.ID] = op
	return op, nil
}

const configurer = "retrieval-config-token-0123456789abcdef012345"

func TestConfigureRetrievalRoute(t *testing.T) {
	const mapping = `{"idempotency_key":"k1","retrieval":{"fields":[{"name":"title","source_pointer":"/extensions/example.editorial/data/headline","type":"string","roles":["search"]}]}}`
	for _, tc := range []struct {
		name, token, path, body string
		storeError              error
		status                  int
		code                    string
		retryable               bool
	}{
		{"without corpora:write", controller, "/v0/corpora/corpus_a/retrieval", `{`, nil, 403, "forbidden", false},
		{"without operations:write", noRebuild, "/v0/corpora/corpus_a/retrieval", `{`, nil, 403, "forbidden", false},
		{"Corpus outside scope", rebuildScoped, "/v0/corpora/corpus_a/retrieval", `{`, nil, 404, "not_found", false},
		{"absent Corpus", configurer, "/v0/corpora/corpus_missing/retrieval", mapping, fmt.Errorf("%w: missing Corpus", corpus.ErrNotFound), 404, "not_found", false},
		{"missing key", configurer, "/v0/corpora/corpus_a/retrieval", `{"retrieval":{}}`, nil, 422, "invalid_schema", false},
		{"undeclared namespace", configurer, "/v0/corpora/corpus_a/retrieval", `{"idempotency_key":"k","retrieval":{"fields":[{"name":"title","source_pointer":"/extensions/undeclared.ns/data/x","type":"string","roles":["search"]}]}}`, nil, 422, "invalid_mapping", false},
		{"unknown profile", configurer, "/v0/corpora/corpus_a/retrieval", `{"idempotency_key":"k","retrieval":{"plugin_profile":"missing.profile"}}`, nil, 422, "unsupported_profile", false},
		{"idempotency conflict", configurer, "/v0/corpora/corpus_a/retrieval", mapping, fmt.Errorf("%w: changed request", operations.ErrConflict), 409, "idempotency_conflict", false},
		{"storage unavailable", configurer, "/v0/corpora/corpus_a/retrieval", mapping, errors.New("storage offline"), 503, "storage_unavailable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryOperations{byID: map[string]operations.Operation{}, fail: tc.storeError}
			server := operationServer(t, store)
			res, body := operationCall(t, server, "PUT", tc.path, tc.token, "application/json", tc.body)
			if res.StatusCode != tc.status || body["code"] != tc.code || body["retryable"] != tc.retryable {
				t.Fatalf("%d %v, want %d %s retryable=%v", res.StatusCode, body, tc.status, tc.code, tc.retryable)
			}
			if len(store.byID) != 0 {
				t.Fatal("rejected configuration reached acceptance")
			}
		})
	}
	store := &memoryOperations{byID: map[string]operations.Operation{}}
	server := operationServer(t, store)
	res, body := operationCall(t, server, "PUT", "/v0/corpora/corpus_a/retrieval", configurer, "application/json", mapping)
	if res.StatusCode != 202 || res.Header.Get("Location") != "/v0/operations/operation_config_k1" || body["kind"] != operations.KindRetrievalConfiguration || body["state"] != "queued" || body["corpus_id"] != "corpus_a" {
		t.Fatalf("accept %d %v %s", res.StatusCode, body, res.Header.Get("Location"))
	}
	var resolved corpus.Retrieval
	want := corpus.Retrieval{Fields: []corpus.Field{{Name: "title", SourcePointer: "/extensions/example.editorial/data/headline", Type: "string", Roles: []string{"search"}}}}
	if err := json.Unmarshal([]byte(store.resolved), &resolved); err != nil || !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved %s", store.resolved)
	}
	if res, _ := operationCall(t, server, "POST", "/v0/corpora/corpus_a/retrieval", configurer, "application/json", mapping); res.StatusCode != 405 {
		t.Fatalf("wrong method %d", res.StatusCode)
	}
}
