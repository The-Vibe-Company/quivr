package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
)

func (m *memoryOperations) AcceptRetrievalConfiguration(_ context.Context, org, corpusID, key string, canonical, resolved []byte) (operations.Operation, error) {
	if m.fail != nil {
		return operations.Operation{}, m.fail
	}
	if corpusID != "corpus_a" && corpusID != "corpus_b" {
		return operations.Operation{}, corpus.ErrNotFound
	}
	if op, ok := m.byKey["config/"+corpusID+"/"+key]; ok {
		if m.byKey["config-canonical/"+corpusID+"/"+key].ID != string(canonical) {
			return operations.Operation{}, operations.ErrConflict
		}
		return op, nil
	}
	m.resolved = string(resolved)
	op := operations.Operation{ID: "operation_config_" + key, Organization: org, Kind: operations.KindRetrievalConfiguration, CorpusID: corpusID, State: operations.StateQueued, Counters: map[string]int{}, Errors: []operations.Error{}}
	m.byKey["config/"+corpusID+"/"+key], m.byID[op.ID] = op, op
	m.byKey["config-canonical/"+corpusID+"/"+key] = operations.Operation{ID: string(canonical)}
	return op, nil
}

const configurer = "retrieval-config-token-0123456789abcdef012345"

func TestConfigureRetrievalRoute(t *testing.T) {
	store := &memoryOperations{byKey: map[string]operations.Operation{}, byID: map[string]operations.Operation{}}
	server := operationServer(t, store)
	const mapping = `{"idempotency_key":"k1","retrieval":{"fields":[{"name":"title","source_pointer":"/extensions/example.editorial/data/headline","type":"string","roles":["search"]}]}}`
	for _, tc := range []struct {
		name, token, path, body string
		status                  int
		code                    string
	}{
		{"without corpora:write", controller, "/v0/corpora/corpus_a/retrieval", mapping, 403, "forbidden"},
		{"without operations:write", noRebuild, "/v0/corpora/corpus_a/retrieval", mapping, 403, "forbidden"},
		{"Corpus outside scope", rebuildScoped, "/v0/corpora/corpus_a/retrieval", mapping, 404, "not_found"},
		{"absent Corpus", configurer, "/v0/corpora/corpus_missing/retrieval", mapping, 404, "not_found"},
		{"missing key", configurer, "/v0/corpora/corpus_a/retrieval", `{"retrieval":{}}`, 422, "invalid_schema"},
		{"raw engine field", configurer, "/v0/corpora/corpus_a/retrieval", `{"idempotency_key":"k","retrieval":{"fields":[{"name":"title","source_pointer":"/properties/title","type":"string","roles":["search"]}]}}`, 422, "invalid_mapping"},
		{"engine name", configurer, "/v0/corpora/corpus_a/retrieval", `{"idempotency_key":"k","retrieval":{"fields":[{"name":"generationId","source_pointer":"/provenance/g","type":"string","roles":["search"]}]}}`, 422, "invalid_mapping"},
		{"undeclared namespace", configurer, "/v0/corpora/corpus_a/retrieval", `{"idempotency_key":"k","retrieval":{"fields":[{"name":"title","source_pointer":"/extensions/undeclared.ns/data/x","type":"string","roles":["search"]}]}}`, 422, "invalid_mapping"},
		{"search on number", configurer, "/v0/corpora/corpus_a/retrieval", `{"idempotency_key":"k","retrieval":{"fields":[{"name":"score","source_pointer":"/provenance/s","type":"number","roles":["search"]}]}}`, 422, "invalid_mapping"},
		{"unknown profile", configurer, "/v0/corpora/corpus_a/retrieval", `{"idempotency_key":"k","retrieval":{"plugin_profile":"missing.profile"}}`, 422, "unsupported_profile"},
	} {
		res, body := operationCall(t, server, "PUT", tc.path, tc.token, "application/json", tc.body)
		if res.StatusCode != tc.status || body["code"] != tc.code {
			t.Errorf("%s: %d %v, want %d %s", tc.name, res.StatusCode, body, tc.status, tc.code)
		}
	}
	if len(store.byID) != 0 {
		t.Fatal("rejected configuration created an Operation")
	}
	res, body := operationCall(t, server, "PUT", "/v0/corpora/corpus_a/retrieval", configurer, "application/json", mapping)
	if res.StatusCode != 202 || res.Header.Get("Location") != "/v0/operations/operation_config_k1" || body["kind"] != operations.KindRetrievalConfiguration || body["state"] != "queued" || body["corpus_id"] != "corpus_a" {
		t.Fatalf("accept %d %v %s", res.StatusCode, body, res.Header.Get("Location"))
	}
	var resolved corpus.Retrieval
	if err := json.Unmarshal([]byte(store.resolved), &resolved); err != nil || len(resolved.Fields) != 1 {
		t.Fatalf("resolved %s", store.resolved)
	}
	if res, again := operationCall(t, server, "PUT", "/v0/corpora/corpus_a/retrieval", configurer, "application/json", mapping); res.StatusCode != 202 || again["operation_id"] != body["operation_id"] {
		t.Fatalf("replay %d %v", res.StatusCode, again)
	}
	changed := `{"idempotency_key":"k1","retrieval":{"plugin_profile":"example.editorial"}}`
	if res, out := operationCall(t, server, "PUT", "/v0/corpora/corpus_a/retrieval", configurer, "application/json", changed); res.StatusCode != 409 || out["code"] != "idempotency_conflict" {
		t.Fatalf("changed request %d %v", res.StatusCode, out)
	}
	if res, _ := operationCall(t, server, "POST", "/v0/corpora/corpus_a/retrieval", configurer, "application/json", mapping); res.StatusCode != 405 {
		t.Fatalf("wrong method %d", res.StatusCode)
	}
}
