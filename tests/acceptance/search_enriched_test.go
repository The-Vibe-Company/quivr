package acceptance

import (
	"os"
	"testing"
)

// An enriched Version is projected as a lexical object plus an enriched object
// (THE-690). Correcting or withdrawing the Record must hide both from every
// search mode.
func TestEnrichedVersionsLeaveSearchWhenCorrectedOrWithdrawn(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Enriched suppression", "idempotency_key": "enriched-suppression-" + run}, 201)["corpus_id"].(string)
	record := "enriched-" + run
	hits := func(mode, query string) []map[string]any {
		t.Helper()
		out := []map[string]any{}
		result := request(t, "POST", "/v0/search", admin, map[string]any{"query": query, "corpus_ids": []string{c}, "mode": mode}, 200)
		for _, item := range result["items"].([]any) {
			out = append(out, item.(map[string]any))
		}
		return out
	}
	modes := []string{"lexical", "semantic", "hybrid"}

	first := ingestEnriched(t, c, "enriched-first-"+run, record, "Le phare de la baie guide les navires la nuit")
	for _, mode := range modes {
		found := false
		for _, h := range hits(mode, "phare de la baie") {
			found = found || h["version_id"] == first
		}
		if !found {
			t.Fatalf("%s search did not return the enriched Version", mode)
		}
	}

	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	correction := inlineCommand(c, "enriched-correction-"+run, record, "Une recette de tarte aux pommes caramelisees")
	correction["source_revision"] = "2"
	accepted := request(t, "POST", "/v0/records", admin, correction, 202)
	ready := awaitRetrievalReady(t, accepted["receipt_id"].(string))
	awaitEnriched(t, admin, c, start, ready["record_id"].(string))
	next := ready["version_id"].(string)
	for _, mode := range modes {
		sawNext := false
		for _, query := range []string{"phare de la baie", "tarte aux pommes"} {
			for _, h := range hits(mode, query) {
				if h["version_id"] == first {
					t.Fatalf("%s search %q returned the superseded Version", mode, query)
				}
				sawNext = sawNext || h["version_id"] == next
			}
		}
		if !sawNext {
			t.Fatalf("%s search did not return the correction", mode)
		}
	}

	accepted = request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(c, "enriched-withdraw-"+run, "example-feed", record, "retraction"), 202)
	if resolved := awaitReceipt(t, accepted["receipt_id"].(string)); resolved["outcome"] != "withdrawal_applied" {
		t.Fatal(resolved)
	}
	for _, mode := range modes {
		for _, query := range []string{"phare de la baie", "tarte aux pommes"} {
			if found := hits(mode, query); len(found) != 0 {
				t.Fatalf("%s search %q returned a withdrawn Record: %v", mode, query, found)
			}
		}
	}
}
