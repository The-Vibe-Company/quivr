package acceptance

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLexicalSearchCanonicalExcerpt(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Lexical search", "idempotency_key": "search-corpus"}, 201)["corpus_id"].(string)
	text := "Éclipse 🌞 à Paris."
	r := request(t, "POST", "/v0/records", admin, inlineCommand(c, "search-first", "eclipse", text), 202)
	r = awaitReceipt(t, r["receipt_id"].(string))
	query := map[string]any{"query": "Éclipse", "corpus_ids": []string{c}, "mode": "lexical"}
	deadline := time.Now().Add(30 * time.Second)
	var result map[string]any
	for {
		result = request(t, "POST", "/v0/search", admin, query, 200)
		if len(result["items"].([]any)) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted text never searchable", r)
		}
		time.Sleep(100 * time.Millisecond)
	}
	hits := result["items"].([]any)
	if len(hits) != 1 {
		t.Fatal("duplicate logical hits", result)
	}
	h := hits[0].(map[string]any)
	if h["record_id"] != r["record_id"] || h["version_id"] != r["version_id"] || h["part_key"] != "body" || h["rank"] != float64(1) {
		t.Fatal(h)
	}
	ex := h["excerpt"].(map[string]any)
	if ex["text"] != text || ex["start"] != float64(0) || ex["end"] != float64(18) || ex["coordinate_system"] != "unicode_codepoint" {
		t.Fatal("excerpt coordinates changed", ex)
	}
	for _, key := range []string{"segment_id", "segmentation_id", "projection_generation_id"} {
		if h[key] == nil || h[key] == "" {
			t.Fatal("missing provenance", h)
		}
	}
	if (h["embedding_artifact_id"] == nil) != (h["vector_space_id"] == nil) {
		t.Fatal("unpaired vector provenance", h)
	}
	a := h["availability"].(map[string]any)
	if a["state"] != "retrieval_ready" || a["is_current"] != true || a["searchable"] != true {
		t.Fatal(a)
	}
	record := request(t, "GET", "/v0/records/"+r["record_id"].(string), admin, nil, 200)
	if record["current_version_id"] != r["version_id"] {
		t.Fatal(record)
	}
	version := request(t, "GET", "/v0/records/"+r["record_id"].(string)+"/versions/"+r["version_id"].(string), admin, nil, 200)
	canonical := version["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string)
	if string([]rune(canonical)[0:18]) != ex["text"] {
		t.Fatal("noncanonical excerpt")
	}
}

func awaitSearchable(t *testing.T, r map[string]any) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		v := request(t, "GET", "/v0/records/"+r["record_id"].(string)+"/versions/"+r["version_id"].(string), os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
		if v["availability"].(map[string]any)["searchable"] == true {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatal("Version never searchable", v)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func TestLexicalScopeLimitsAndReplay(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Rank scope", "idempotency_key": "rank-corpus"}, 201)["corpus_id"].(string)
	var first map[string]any
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("constellation-%d", i)
		r := request(t, "POST", "/v0/records", admin, inlineCommand(c, key, key, "Constellation 🌌 témoin "+key), 202)
		r = awaitReceipt(t, r["receipt_id"].(string))
		awaitSearchable(t, r)
		if i == 0 {
			first = r
		}
	}
	q := map[string]any{"query": "constellation", "corpus_ids": []string{c}, "mode": "lexical"}
	result := request(t, "POST", "/v0/search", admin, q, 200)
	if len(result["items"].([]any)) != 10 {
		t.Fatal("wrong default limit", result)
	}
	q["limit"] = 50
	all := request(t, "POST", "/v0/search", admin, q, 200)
	if len(all["items"].([]any)) != 12 {
		t.Fatal("missing indexed texts", all)
	}
	// A source filter narrows before ranking: every text is in example-feed.
	q["filter"] = map[string]any{"source_namespaces": []string{"absent-feed", "example-feed"}}
	if n := len(request(t, "POST", "/v0/search", admin, q, 200)["items"].([]any)); n != 12 {
		t.Fatalf("source filter on example-feed returned %d hits, want 12", n)
	}
	q["filter"] = map[string]any{"source_namespaces": []string{"absent-feed"}}
	if n := len(request(t, "POST", "/v0/search", admin, q, 200)["items"].([]any)); n != 0 {
		t.Fatalf("source filter on absent-feed returned %d hits, want 0", n)
	}
	for _, invalid := range []map[string]any{{}, {"source_namespaces": []string{}}, {"source_namespaces": []string{"a", "a"}}, {"source_namespaces": []string{""}}, {"namespace": "a"}} {
		q["filter"] = invalid
		request(t, "POST", "/v0/search", admin, q, 422)
	}
	delete(q, "filter")
	segments := map[string]bool{}
	for i, item := range all["items"].([]any) {
		h := item.(map[string]any)
		if h["rank"] != float64(i+1) || segments[h["segment_id"].(string)] {
			t.Fatal("ranks or duplicates", all)
		}
		segments[h["segment_id"].(string)] = true
	}
	duplicate := request(t, "POST", "/v0/records", admin, inlineCommand(c, "constellation-replay", "constellation-0", "Constellation 🌌 témoin constellation-0"), 202)
	duplicate = awaitReceipt(t, duplicate["receipt_id"].(string))
	if duplicate["outcome"] != "duplicate" || duplicate["version_id"] != first["version_id"] {
		t.Fatal(duplicate)
	}
	again := request(t, "POST", "/v0/search", admin, q, 200)
	if len(again["items"].([]any)) != 12 {
		t.Fatal("duplicate added index segments", again)
	}
	for _, item := range again["items"].([]any) {
		if !segments[item.(map[string]any)["segment_id"].(string)] {
			t.Fatal("unstable segment identity")
		}
	}
	other := os.Getenv("QUIVR_TEST_OTHER")
	otherC := request(t, "POST", "/v0/corpora", other, map[string]any{"name": "Other org", "idempotency_key": "rank-other"}, 201)["corpus_id"].(string)
	for _, token := range []string{other, os.Getenv("QUIVR_TEST_SCOPED"), os.Getenv("QUIVR_TEST_DENIED"), os.Getenv("QUIVR_TEST_WRITER")} {
		request(t, "POST", "/v0/search", token, q, 403)
	}
	q["corpus_ids"] = []string{c, otherC}
	request(t, "POST", "/v0/search", admin, q, 403)
	q["corpus_ids"] = []string{otherC}
	empty := request(t, "POST", "/v0/search", other, q, 200)
	if len(empty["items"].([]any)) != 0 {
		t.Fatal("cross-organization leak", empty)
	}
	if empty["retrieval_profile"] == nil {
		t.Fatal("missing resolved profile")
	}
	q["corpus_ids"] = []string{c}
	q["limit"] = 1
	if len(request(t, "POST", "/v0/search", admin, q, 200)["items"].([]any)) != 1 {
		t.Fatal("limit ignored")
	}
	for _, limit := range []int{0, 51, -1} {
		q["limit"] = limit
		request(t, "POST", "/v0/search", admin, q, 422)
	}
	delete(q, "limit")
	for _, mode := range []string{"unknown"} {
		q["mode"] = mode
		request(t, "POST", "/v0/search", admin, q, 422)
	}
	delete(q, "mode")
	request(t, "POST", "/v0/search", admin, q, 200) // Hybrid is the default.
	q["mode"] = "lexical"
	for _, profile := range []string{"fast", "deep"} {
		q["profile"] = profile
		request(t, "POST", "/v0/search", admin, q, 422)
	}
	delete(q, "profile")
	q["query"] = strings.Repeat("bonjour ", 257)
	request(t, "POST", "/v0/search", admin, q, 422)
	q["query"] = "constellation"
	delete(q, "corpus_ids")
	request(t, "POST", "/v0/search", admin, q, 422)
	q["corpus_ids"] = []string{}
	request(t, "POST", "/v0/search", admin, q, 422)
	q["corpus_ids"] = []string{c, c}
	request(t, "POST", "/v0/search", admin, q, 422)
}
func TestLexicalUnsupportedTextRemainsReadable(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	text := strings.Repeat("x", 262145)
	r := request(t, "POST", "/v0/records", admin, inlineCommand(c, "beyond-short", "beyond-short", text), 202)
	r = awaitReceipt(t, r["receipt_id"].(string))
	deadline := time.Now().Add(30 * time.Second)
	for {
		view := request(t, "GET", "/v0/ingestion-receipts/"+r["receipt_id"].(string), admin, nil, 200)
		if view["processing"].(map[string]any)["state"] == "blocked" {
			if view["outcome"] != "created" || view["availability"].(map[string]any)["searchable"] != false || len(view["diagnostics"].([]any)) == 0 {
				t.Fatal(view)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unsupported processing not observable", view)
		}
		time.Sleep(100 * time.Millisecond)
	}
	v := request(t, "GET", "/v0/records/"+r["record_id"].(string)+"/versions/"+r["version_id"].(string), admin, nil, 200)
	if v["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"] != text {
		t.Fatal("unsupported text truncated")
	}
}
