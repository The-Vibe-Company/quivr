package acceptance

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// lexicalHits maps version_id to the hit for a lexical query over corpora.
func lexicalHits(t *testing.T, corpora []string, query string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	result := request(t, "POST", "/v0/search", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"query": query, "corpus_ids": corpora, "mode": "lexical"}, 200)
	for _, item := range result["items"].([]any) {
		hit := item.(map[string]any)
		out[hit["version_id"].(string)] = hit
	}
	return out
}

func effectiveFields(t *testing.T, corpusID string) []any {
	t.Helper()
	c := request(t, "GET", "/v0/corpora/"+corpusID, os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
	fields, ok := c["effective_retrieval"].(map[string]any)["fields"].([]any)
	if !ok {
		t.Fatalf("effective_retrieval without fields: %v", c)
	}
	return fields
}

// A Corpus owner maps structured source fields to retrieval roles. The
// mapping takes effect only when its replacement generation is validated and
// routed, changes public search for that Corpus alone, and leaves every source
// field readable. Uses its own two Corpora.
func TestRetrievalConfigurationChangesSearchOnlyAfterCutover(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := fmt.Sprint(time.Now().UnixNano())
	a := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Mapping A", "idempotency_key": "mapping-a-" + run}, 201)["corpus_id"].(string)
	b := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Mapping B", "idempotency_key": "mapping-b-" + run}, 201)["corpus_id"].(string)
	const text = "Rapport annuel sur le trafic du port."
	versions := map[string]string{}
	for _, corpusID := range []string{a, b} {
		command := inlineCommand(corpusID, "mapping-"+corpusID, "rapport", text)
		command["extensions"] = map[string]any{"example.editorial": map[string]any{"schema_version": "1", "data": map[string]any{"headline": "Zircone au quai nord", "summary": "Basalte et granit"}}}
		accepted := request(t, "POST", "/v0/records", admin, command, 202)
		versions[corpusID] = awaitRetrievalReady(t, accepted["receipt_id"].(string))["version_id"].(string)
	}
	if hits := lexicalHits(t, []string{a, b}, "zircone"); len(hits) != 0 {
		t.Fatalf("unmapped source field was searchable: %v", hits)
	}
	before := lexicalHits(t, []string{a, b}, "trafic")
	if len(before) != 2 || len(effectiveFields(t, a)) != 0 {
		t.Fatalf("baseline %v", before)
	}

	mapping := map[string]any{"fields": []any{
		map[string]any{"name": "title", "source_pointer": "/extensions/example.editorial/data/headline", "type": "string", "roles": []string{"search"}},
		map[string]any{"name": "summary", "source_pointer": "/extensions/example.editorial/data/summary", "type": "string", "roles": []string{"search"}},
		map[string]any{"name": "published", "source_pointer": "/provenance/published", "type": "datetime", "roles": []string{"filter"}},
	}}
	put := func(token, corpusID, key string, retrieval map[string]any, want int) map[string]any {
		return request(t, "PUT", "/v0/corpora/"+corpusID+"/retrieval", token, map[string]any{"idempotency_key": key, "retrieval": retrieval}, want)
	}
	// Authorization and validation reject before any Operation exists.
	put(os.Getenv("QUIVR_TEST_READER"), a, "denied", mapping, 403)
	put(os.Getenv("QUIVR_TEST_CONFIGURER"), a, "no-operations-write", mapping, 403)
	put(os.Getenv("QUIVR_TEST_SCOPED"), a, "out-of-scope", mapping, 404)
	put(os.Getenv("QUIVR_TEST_OTHER"), a, "foreign", mapping, 404)
	for name, fields := range map[string]map[string]any{
		"raw engine path":      {"name": "title", "source_pointer": "/properties/title", "type": "string", "roles": []string{"search"}},
		"engine property name": {"name": "generationId", "source_pointer": "/provenance/summary", "type": "string", "roles": []string{"search"}},
		"undeclared namespace": {"name": "title", "source_pointer": "/extensions/undeclared.ns/data/headline", "type": "string", "roles": []string{"search"}},
		"search on number":     {"name": "score", "source_pointer": "/provenance/score", "type": "number", "roles": []string{"search"}},
	} {
		if got := put(admin, a, "invalid-"+name, map[string]any{"fields": []any{fields}}, 422); got["code"] != "invalid_mapping" {
			t.Fatalf("%s: %v", name, got)
		}
	}
	if got := put(admin, a, "engine-key", map[string]any{"fields": []any{}, "engine": map[string]any{"title": "x"}}, 422); got["code"] != "invalid_schema" {
		t.Fatalf("raw engine key: %v", got)
	}

	op := put(admin, a, "map-"+run, mapping, 202)
	location := "/v0/operations/" + op["operation_id"].(string)
	if op["kind"] != "retrieval_configuration" || op["corpus_id"] != a {
		t.Fatalf("accepted %v", op)
	}
	// Until cutover the prior configuration stays effective: once the Corpus
	// reports the new configuration, its Operation has already succeeded.
	if fields := effectiveFields(t, a); len(fields) != 0 {
		if state := request(t, "GET", location, admin, nil, 200)["state"]; state != "succeeded" {
			t.Fatalf("configuration effective while %v", state)
		}
	}
	if replay := put(admin, a, "map-"+run, mapping, 202); replay["operation_id"] != op["operation_id"] {
		t.Fatalf("replay %v", replay)
	}
	put(admin, a, "map-"+run, map[string]any{"plugin_profile": "example.editorial"}, 409)
	done := awaitOperation(t, location)
	if done["state"] != "succeeded" {
		t.Fatalf("configuration outcome %v", done)
	}
	generation := done["result"].(map[string]any)["projection_generation_id"].(string)
	fields := effectiveFields(t, a)
	if len(fields) != 3 || fields[0].(map[string]any)["source_pointer"] != "/extensions/example.editorial/data/headline" || fields[2].(map[string]any)["roles"].([]any)[0] != "filter" {
		t.Fatalf("effective after cutover %v", fields)
	}
	if len(effectiveFields(t, b)) != 0 {
		t.Fatal("neighbouring Corpus configuration changed")
	}

	// The mapped title and summary are searchable in A only; B is unchanged.
	for _, query := range []string{"zircone", "basalte"} {
		hits := lexicalHits(t, []string{a, b}, query)
		hit, ok := hits[versions[a]]
		if len(hits) != 1 || !ok || hit["projection_generation_id"] != generation {
			t.Fatalf("%s hits %v, want only %s on %s", query, hits, versions[a], generation)
		}
		// Excerpts remain canonical segment text, even when mapped text matched.
		if excerpt := hit["excerpt"].(map[string]any)["text"].(string); excerpt != text || strings.Contains(strings.ToLower(excerpt), query) {
			t.Fatalf("excerpt %q", excerpt)
		}
	}
	after := lexicalHits(t, []string{a, b}, "trafic")
	if after[versions[b]]["projection_generation_id"] != before[versions[b]]["projection_generation_id"] {
		t.Fatalf("neighbouring Corpus rerouted %v", after)
	}
	// Other source fields stay readable through the immutable Version.
	version := request(t, "GET", "/v0/records/"+after[versions[a]]["record_id"].(string)+"/versions/"+versions[a], admin, nil, 200)
	if version["extensions"].(map[string]any)["example.editorial"].(map[string]any)["data"].(map[string]any)["headline"] != "Zircone au quai nord" {
		t.Fatalf("source extension %v", version["extensions"])
	}

	// A profile resolves its default fields; explicit fields override by name.
	profiled := put(admin, b, "profile-"+run, map[string]any{"plugin_profile": "example.editorial"}, 202)
	if awaitOperation(t, "/v0/operations/"+profiled["operation_id"].(string))["state"] != "succeeded" {
		t.Fatal("profile configuration failed")
	}
	c := request(t, "GET", "/v0/corpora/"+b, admin, nil, 200)["effective_retrieval"].(map[string]any)
	if c["plugin_profile"] != "example.editorial" || len(c["fields"].([]any)) != 1 {
		t.Fatalf("profile config %v", c)
	}
	if hits := lexicalHits(t, []string{b}, "zircone"); len(hits) != 1 {
		t.Fatalf("profile title not applied %v", hits)
	}
}
