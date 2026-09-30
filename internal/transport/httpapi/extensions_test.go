package httpapi_test

import (
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// New hands the content extension registry to retrieval configuration, so a
// mapping may address a plugin-owned namespace. Client writes to that namespace
// are refused by the content service (internal/content registry_test), mapped
// by TestContentFailureCodesIgnoreDetail and proven end to end in acceptance.
func TestRetrievalMappingsMayAddressPluginOwnedNamespaces(t *testing.T) {
	registry := content.NewExtensionRegistry()
	if err := registry.Own("acme-md", "acme-md.outline"); err != nil {
		t.Fatal(err)
	}
	port := &acceptancePort{}
	store := &memoryOperations{byKey: map[string]operations.Operation{}, byID: map[string]operations.Operation{}}
	keys := map[string]corpus.Scope{
		configurer: {Organization: "org_a", Actions: []string{"corpora:write", "operations:write", "operations:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{Repository: port, BlobSource: verifiedBlobs{}, Extensions: registry}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithOperations(operations.Service{Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	mapping := `{"idempotency_key":"k","retrieval":{"fields":[{"name":"heading_levels","source_pointer":"/extensions/acme-md.outline/data/heading_levels","type":"string_array","roles":["search"]}]}}`
	if res, out := operationCall(t, server, "PUT", "/v0/corpora/corpus_a/retrieval", configurer, "application/json", mapping); res.StatusCode != 202 {
		t.Fatalf("mapping on a plugin namespace: %d %v", res.StatusCode, out)
	}
	undeclared := `{"idempotency_key":"k2","retrieval":{"fields":[{"name":"x","source_pointer":"/extensions/acme-md.other/data/x","type":"string","roles":["search"]}]}}`
	if res, out := operationCall(t, server, "PUT", "/v0/corpora/corpus_a/retrieval", configurer, "application/json", undeclared); res.StatusCode != 422 || out["code"] != "invalid_mapping" {
		t.Fatalf("mapping on an undeclared namespace: %d %v", res.StatusCode, out)
	}
}
