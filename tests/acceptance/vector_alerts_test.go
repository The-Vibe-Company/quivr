package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func vectorArticles(t *testing.T) []map[string]any {
	t.Helper()
	keywordEvaluator(t)
	corpusID := os.Getenv("QUIVR_TEST_VECTOR_CORPUS")
	if corpusID == "" {
		t.Skip("vector alert outage scenario is prepared by the harness")
	}
	raw, err := os.ReadFile("../../plugins/alerts/calibration/set.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Articles []struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var receipts []map[string]any
	for _, index := range []int{0, 1, 9} {
		article := fixture.Articles[index]
		parts := []any{
			map[string]any{"key": "title", "role": "title", "content": map[string]any{"kind": "text", "text": article.Title}},
			map[string]any{"key": "body", "role": "body", "content": map[string]any{"kind": "text", "text": article.Text}},
		}
		key := fmt.Sprintf("local-vectors-%d", index)
		response := request(t, "POST", "/v0/records", os.Getenv("QUIVR_TEST_ADMIN"), manifestCommandBody(corpusID, key, "neutral-wire", key, parts, nil, nil), 202)
		receipts = append(receipts, awaitReady(t, response["receipt_id"].(string)))
	}
	return receipts
}

func vectorPreview(t *testing.T) map[string]any {
	t.Helper()
	id, version := keywordEvaluator(t)
	return request(t, "POST", "/v0/subscription-previews", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{
		"saved_query_id": os.Getenv("QUIVR_TEST_VECTOR_QUERY"), "saved_query_version_id": os.Getenv("QUIVR_TEST_VECTOR_VERSION"),
		"evaluator": map[string]any{"plugin_id": id, "version": version, "configuration": map[string]any{"wait_for_enrichment": false, "threshold": 0.8}},
	}, 200)
}

func TestVectorAlertsPending(t *testing.T) {
	vectorArticles(t)
	preview := vectorPreview(t)
	if preview["evaluated"] != float64(3) || preview["not_ready"] != float64(3) || len(preview["matches"].([]any)) != 0 {
		t.Fatalf("missing stored vectors must be not_ready, not a negative decision: %v", preview)
	}
	matches := request(t, "GET", matchesPath(os.Getenv("QUIVR_TEST_VECTOR_SUBSCRIPTION"), "", 0), os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
	if len(matches["items"].([]any)) != 0 {
		t.Fatalf("unready article raised an alert: %v", matches)
	}
}

func TestVectorAlertsEnrichmentMatchesOnce(t *testing.T) {
	receipts := vectorArticles(t)
	admin, corpusID, cursor := os.Getenv("QUIVR_TEST_ADMIN"), os.Getenv("QUIVR_TEST_VECTOR_CORPUS"), os.Getenv("QUIVR_TEST_VECTOR_CURSOR")
	for _, receipt := range receipts {
		awaitEnriched(t, admin, corpusID, cursor, receipt["record_id"].(string))
	}
	preview := vectorPreview(t)
	if preview["not_ready"] != float64(0) || preview["evaluated"] != float64(3) || len(preview["matches"].([]any)) != 2 {
		t.Fatalf("E5 must match the paraphrase and translation, but not the unrelated article: %v", preview)
	}
	seen, _ := awaitMatches(t, admin, corpusID, cursor, 2)
	created := matchCreatedFor(seen, os.Getenv("QUIVR_TEST_VECTOR_SUBSCRIPTION"))
	if len(created) != 2 {
		t.Fatalf("enrichment must create one Match per fitting article: %v", created)
	}
	wanted := map[string]bool{receipts[0]["record_id"].(string): true, receipts[1]["record_id"].(string): true}
	for _, event := range created {
		refs := event["monitoring"].(map[string]any)
		recordID := refs["record_id"].(string)
		if !wanted[recordID] {
			t.Fatalf("unexpected or duplicate Match for %s: %v", recordID, created)
		}
		delete(wanted, recordID)
		evidence := request(t, "GET", "/v0/matches/"+refs["match_id"].(string), admin, nil, 200)["evidence"].(map[string]any)
		if len(evidence["part_keys"].([]any)) != 1 {
			t.Fatalf("local evidence must identify the best Part: %v", evidence)
		}
		details := evidence["details"].(map[string]any)
		if details["similarity"] == nil || details["vector_space_id"] == nil {
			t.Fatalf("local evidence must identify similarity and space: %v", evidence)
		}
	}
}
