package acceptance

import (
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"testing"
)

// Owns the public cross-Corpus contract: common fields work across sources;
// typed mappings exclude undeclared Corpora; all search modes and catalog
// pages agree, and a rebuild makes a newly declared field usable.
func TestMetadataFiltersAcrossCorpora(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	create := func(name string, fields []any) string {
		return request(t, "POST", "/v0/corpora", admin, map[string]any{"name": name, "idempotency_key": run + name, "retrieval": map[string]any{"fields": fields}}, 201)["corpus_id"].(string)
	}
	urgency := map[string]any{"name": "urgency", "source_pointer": "/extensions/example.editorial/data/flags/urgency", "type": "number", "roles": []string{"filter"}}
	a := create("Metadata A", []any{urgency})
	b := create("Metadata B", []any{})
	ids := []string{a, b}
	versions := map[string]bool{}
	for i, id := range ids {
		cmd := inlineCommand(id, run+id, "harbour", "The harbour ferry report.")
		cmd["extensions"] = map[string]any{
			"quivr.metadata":    map[string]any{"schema_version": "1", "data": map[string]any{"language": "en", "published_at": "2026-09-30T12:00:00Z", "source_type": "rss", "tags": []string{"sea weather"}}},
			"example.editorial": map[string]any{"schema_version": "1", "data": map[string]any{"flags": map[string]any{"urgency": 2.0}}},
		}
		if i == 0 {
			// The real normalizer must supply common metadata from XML; copying
			// it into the command would conceal a regression in its mapping.
			const mediaType = "application/vnd.iptc.g2.newsmessage+xml"
			xml := `<newsItem xmlns="http://iptc.org/std/nar/2006-10-01/" guid="urn:example:harbour" version="1" xml:lang="en"><itemMeta><firstCreated>2026-09-30T14:00:00+02:00</firstCreated><versionCreated>2026-10-01T12:00:00Z</versionCreated></itemMeta><contentMeta><keyword>sea weather</keyword></contentMeta><contentSet><inlineXML><p xmlns="">The harbour ferry report.</p></inlineXML></contentSet></newsItem>`
			xml = `<newsMessage xmlns="http://iptc.org/std/nar/2006-10-01/"><itemSet>` + xml + `</itemSet></newsMessage>`
			cmd["content"] = map[string]any{"kind": "blob", "blob_id": uploadBlob(t, admin, []byte(xml), mediaType), "media_type": mediaType}
			delete(cmd["extensions"].(map[string]any), "quivr.metadata")
		}
		start := request(t, "GET", changesPath(id, "", 0), admin, nil, 200)["next_cursor"].(string)
		receipt := request(t, "POST", "/v0/records", admin, cmd, 202)
		version := awaitRetrievalReady(t, receipt["receipt_id"].(string))["version_id"].(string)
		awaitEnriched(t, admin, id, start, receipt["record_id"].(string))
		versions[version] = true
	}
	common := []any{map[string]any{"field": "metadata.language", "any_of": []any{"fr", "en"}}, map[string]any{"field": "metadata.published_at", "gte": "2026-09-30T12:00:00Z", "lte": "2026-09-30T12:00:00Z"}, map[string]any{"field": "metadata.tags", "any_of": []any{"sea weather"}}}
	search := func(mode string, filters []any, want int, excluded string) map[string]any {
		t.Helper()
		result := request(t, "POST", "/v0/search", admin, map[string]any{"query": "harbour", "corpus_ids": ids, "mode": mode, "filter": map[string]any{"metadata": filters}}, 200)
		items := result["items"].([]any)
		if len(items) != want {
			t.Fatalf("%s metadata hits %v, want %d", mode, result, want)
		}
		for _, item := range items {
			if !versions[item.(map[string]any)["version_id"].(string)] {
				t.Fatalf("unexpected version: %v", item)
			}
		}
		assertExcluded := func() {
			if excluded == "" {
				return
			}
			list, ok := result["excluded_corpora"].([]any)
			if !ok || len(list) != 1 || list[0].(map[string]any)["corpus_id"] != excluded {
				t.Fatalf("exclusions: %v", result)
			}
		}
		assertExcluded()
		return result
	}
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		search(mode, common, 2, "")
		for _, negative := range [][]any{
			{map[string]any{"field": "metadata.language", "any_of": []any{"de"}}},
			{map[string]any{"field": "metadata.published_at", "gte": "2026-10-01T00:00:00Z"}},
		} {
			search(mode, negative, 0, "")
		}
	}
	typed := append(append([]any{}, common...), map[string]any{"field": "urgency", "any_of": []any{2.0}})
	search("lexical", typed, 1, b)
	list := func(filters []any, limit int, cursor string, wantStatus int) map[string]any {
		raw, _ := json.Marshal(filters)
		params := url.Values{"corpus_ids": {a + "," + b}, "order": {"accepted_at_desc"}, "metadata": {string(raw)}, "limit": {strconv.Itoa(limit)}}
		if cursor != "" {
			params.Set("page_cursor", cursor)
		}
		return request(t, "GET", "/v0/records?"+params.Encode(), admin, nil, wantStatus)
	}
	for _, negative := range [][]any{
		{map[string]any{"field": "metadata.language", "any_of": []any{"de"}}},
		{map[string]any{"field": "metadata.published_at", "gte": "2026-10-01T00:00:00Z"}},
	} {
		if got := list(negative, 10, "", 200); len(got["items"].([]any)) != 0 {
			t.Fatalf("nonmatching common metadata returned catalog items: %v", got)
		}
	}
	page := list(common, 1, "", 200)
	if len(page["items"].([]any)) != 1 {
		t.Fatalf("first filtered page %v", page)
	}
	cursor := page["next_page_cursor"].(string)
	next := list(common, 1, cursor, 200)
	if len(next["items"].([]any)) != 1 || page["items"].([]any)[0].(map[string]any)["record_id"] == next["items"].([]any)[0].(map[string]any)["record_id"] {
		t.Fatalf("catalog pagination %v %v", page, next)
	}
	changed := list(typed, 1, cursor, 409)
	if changed["code"] != "cursor_scope_changed" {
		t.Fatalf("filter cursor change %v", changed)
	}
	typedPage := list(typed, 10, "", 200)
	excluded, ok := typedPage["excluded_corpora"].([]any)
	if len(typedPage["items"].([]any)) != 1 || !ok || len(excluded) != 1 || excluded[0].(map[string]any)["corpus_id"] != b {
		t.Fatalf("typed catalog %v", typedPage)
	}
	op := request(t, "PUT", "/v0/corpora/"+b+"/retrieval", admin, map[string]any{"idempotency_key": run + "declare", "retrieval": map[string]any{"fields": []any{urgency}}}, 202)
	if done := awaitOperation(t, "/v0/operations/"+op["operation_id"].(string)); done["state"] != "succeeded" {
		t.Fatalf("rebuild failed %v", done)
	}
	search("lexical", typed, 2, "")
	if got := list(typed, 10, "", 200); len(got["items"].([]any)) != 2 {
		t.Fatalf("rebuilt catalog %v", got)
	}
	if got := list(common, 1, cursor, 409); got["code"] != "cursor_scope_changed" {
		t.Fatalf("generation cursor %v", got)
	}
	missing := search("lexical", []any{map[string]any{"field": "unknown", "any_of": []any{"x"}}}, 0, "")
	all, ok := missing["excluded_corpora"].([]any)
	if !ok || len(all) != 2 {
		t.Fatalf("all excluded: %v", missing)
	}
	seen := map[string]bool{}
	for _, item := range all {
		entry := item.(map[string]any)
		fields, ok := entry["fields"].([]any)
		if !ok || len(fields) != 1 || fields[0] != "unknown" {
			t.Fatalf("missing field reason: %v", entry)
		}
		seen[entry["corpus_id"].(string)] = true
	}
	if !seen[a] || !seen[b] {
		t.Fatalf("missing Corpus explanations: %v", missing)
	}
	request(t, "POST", "/v0/search", admin, map[string]any{"query": "harbour", "corpus_ids": ids, "filter": map[string]any{"metadata": []any{map[string]any{"field": "urgency", "any_of": []any{"2"}}}}}, 422)
	request(t, "GET", "/v0/records?corpus_ids="+a+","+b, os.Getenv("QUIVR_TEST_SCOPED"), nil, 404)
}
