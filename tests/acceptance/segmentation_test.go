package acceptance

import (
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestLongTextExactSegments(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Long text", "idempotency_key": "long-corpus"}, 201)["corpus_id"].(string)
	text := "Première ligne conservée comme corps.\n\n" + strings.Repeat("La constellation 🌌 éclaire Paris. Les étoiles restent visibles au-dessus du fleuve.\n\n", 80) + "La constellation finale clôt ce texte.\n"
	cmd := inlineCommand(c, "long-first", "long-record", text)
	r := request(t, "POST", "/v0/records", admin, cmd, 202)
	r = awaitReceipt(t, r["receipt_id"].(string))
	deadline := time.Now().Add(30 * time.Second)
	for {
		view := request(t, "GET", "/v0/ingestion-receipts/"+r["receipt_id"].(string), admin, nil, 200)
		if view["processing"].(map[string]any)["state"] == "blocked" {
			t.Fatal("long text processing blocked", view)
		}
		if view["availability"].(map[string]any)["searchable"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("long text never searchable", view)
		}
		time.Sleep(100 * time.Millisecond)
	}
	q := map[string]any{"query": "constellation", "corpus_ids": []string{c}, "mode": "lexical", "limit": 50}
	result := request(t, "POST", "/v0/search", admin, q, 200)
	items := result["items"].([]any)
	if len(items) < 3 {
		t.Fatal("long text not segmented", result)
	}
	runes := []rune(text)
	ids := map[string]bool{}
	ranges := [][2]int{}
	for _, item := range items {
		h := item.(map[string]any)
		ex := h["excerpt"].(map[string]any)
		start, end := int(ex["start"].(float64)), int(ex["end"].(float64))
		if start < 0 || end > len(runes) || end <= start || string(runes[start:end]) != ex["text"] {
			t.Fatal("noncanonical slice", h)
		}
		if h["version_id"] != r["version_id"] || h["part_key"] != "body" {
			t.Fatal("source identity changed", h)
		}
		id := h["segment_id"].(string)
		if ids[id] {
			t.Fatal("duplicate segment")
		}
		ids[id] = true
		ranges = append(ranges, [2]int{start, end})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	if ranges[0][0] != 0 || ranges[len(ranges)-1][1] != len(runes) {
		t.Fatal("source ends truncated", ranges)
	}
	for i := 1; i < len(ranges); i++ {
		if ranges[i][0] >= ranges[i-1][1] || ranges[i][1] <= ranges[i-1][1] {
			t.Fatal("missing overlap or contained segment", ranges)
		}
	}
	cmd["idempotency_key"] = "long-replay"
	replay := request(t, "POST", "/v0/records", admin, cmd, 202)
	replay = awaitReceipt(t, replay["receipt_id"].(string))
	if replay["version_id"] != r["version_id"] {
		t.Fatal("derivation created a Version")
	}
	again := request(t, "POST", "/v0/search", admin, q, 200)["items"].([]any)
	if len(again) != len(items) {
		t.Fatal("unstable segment count")
	}
	for _, item := range again {
		if !ids[item.(map[string]any)["segment_id"].(string)] {
			t.Fatal("unstable segment identity")
		}
	}
	v := request(t, "GET", "/v0/records/"+r["record_id"].(string)+"/versions/"+r["version_id"].(string), admin, nil, 200)
	parts := v["manifest"].(map[string]any)["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["content"].(map[string]any)["text"] != text {
		t.Fatal("Manifest rewritten")
	}
}
