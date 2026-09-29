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

// A plugin-owned namespace is refused on every client write path with its own
// explicit code, while retrieval mappings may address it.
func TestPluginOwnedNamespaces(t *testing.T) {
	registry := content.NewExtensionRegistry()
	if err := registry.Own("acme-md", "acme-md.outline"); err != nil {
		t.Fatal(err)
	}
	port := &acceptancePort{}
	store := &memoryOperations{byKey: map[string]operations.Operation{}, byID: map[string]operations.Operation{}}
	keys := map[string]corpus.Scope{
		adminKey:   {Organization: "org_a", Actions: []string{"content:read", "content:write"}, Corpora: []string{"*"}},
		configurer: {Organization: "org_a", Actions: []string{"corpora:write", "operations:write", "operations:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{Repository: port, BlobSource: verifiedBlobs{}, Extensions: registry}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithOperations(operations.Service{Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	with := func(key, ns string, data map[string]any) map[string]any {
		c := inline(key, key, "Texte")
		c["extensions"] = map[string]any{ns: map[string]any{"schema_version": "1", "data": data}}
		return c
	}
	owned := with("owned", "acme-md.outline", map[string]any{"heading_count": 2})
	if status, body := postJSON(t, handler, "/v0/records", adminKey, owned); status != 422 || body["code"] != "extension_namespace_owned" {
		t.Fatalf("single owned write: %d %v", status, body)
	}
	builtin := with("builtin", "example.editorial", map[string]any{"headline": "Titre"})
	if status, body := postJSON(t, handler, "/v0/records", adminKey, builtin); status != 202 {
		t.Fatalf("built-in write: %d %v", status, body)
	}
	status, body := postJSON(t, handler, "/v0/records/batch", adminKey, map[string]any{"items": []any{with("batch-owned", "acme-md.outline", map[string]any{}), with("batch-builtin", "example.editorial", map[string]any{})}})
	if status != 200 {
		t.Fatalf("batch %d %v", status, body)
	}
	items := entries(t, body)
	if code := entryError(t, items[0])["code"]; code != "extension_namespace_owned" {
		t.Fatalf("batch owned entry %v", items[0])
	}
	if _, ok := items[1]["receipt"]; !ok {
		t.Fatalf("batch built-in entry %v", items[1])
	}
	if len(port.accepted) != 2 {
		t.Fatalf("accepted %v", port.accepted)
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
