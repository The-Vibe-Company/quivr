package acceptance

import (
	"os"
	"testing"
	"time"
)

// rollbackSetup is what scripts/ingestion_plugin.py gives the rollback
// scenario once the pinning one ran: 0.1.0 (A) serves the plan at its
// address, and 0.2.0 (B), which plays a bad release, runs again at its own.
func rollbackSetup(t *testing.T) (operator, a, b, corpusID, run string) {
	t.Helper()
	operator, b, _, _ = activationSetup(t)
	a = os.Getenv("QUIVR_TEST_ROLLBACK_ENDPOINT")
	if a == "" {
		t.Skip("make verify runs both versions for the rollback scenario")
	}
	corpusID, run = ingestionPluginCorpus(t)
	return operator, a, b, corpusID, run
}

// gullsText is the Record ingested through B; posting it again with its key
// replays its receipt.
const gullsText = "Gulls circled the trawler until the nets came up empty."

// TestRollbackStarts activates B, the bad release, and ingests a Record
// through it while A still runs.
func TestRollbackStarts(t *testing.T) {
	operator, _, b, corpusID, run := rollbackSetup(t)
	plan := request(t, "POST", "/v0/admin/plugins/"+registrationAt(t, operator, "0.2.0", b)["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
	if planRoles(plan)["ingestion"] != "example.hash_embedder@0.2.0" {
		t.Fatalf("activating B: %v", plan)
	}
	ingestEnriched(t, corpusID, "bad-release-"+run, "gulls", gullsText)
}

// TestRollback runs once the harness stopped B. A Record pinned to B's plan
// retries; one call rolls back to A with pinned_work=stop, recording a plan
// that names B's as the one it replaced, which the key replays and the plan
// history lists. The pinned Record stops without reaching B, the next Record
// goes through A alone, and the Record B produced stays searchable. Rolling
// back again would return to B, which does not answer: refused, and the plan
// stays.
func TestRollback(t *testing.T) {
	operator, _, _, corpusID, run := rollbackSetup(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	bad := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200)
	if planRoles(bad)["ingestion"] != "example.hash_embedder@0.2.0" {
		t.Fatalf("before the rollback B serves ingestion: %v", bad)
	}
	receipt := awaitReceipt(t, request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "stopped-"+run, "stopped", "The ferry waited for the tide to turn."), 202)["receipt_id"].(string))
	version := "/v0/records/" + receipt["record_id"].(string) + "/versions/" + receipt["version_id"].(string)
	deadline := time.Now().Add(30 * time.Second)
	for request(t, "GET", version, admin, nil, 200)["processing"].(map[string]any)["state"] != "retrying" {
		if time.Now().After(deadline) {
			t.Fatalf("the Version never retried with B stopped: %v", request(t, "GET", version, admin, nil, 200))
		}
		time.Sleep(100 * time.Millisecond)
	}

	body := map[string]any{"idempotency_key": "rollback-" + run, "pinned_work": "stop"}
	back := request(t, "POST", "/v0/admin/plugins/plan/rollback", operator, body, 200)
	if back["source"] != "rollback" || back["previous_plan_id"] != bad["plan_id"] || planRoles(back)["ingestion"] != "example.hash_embedder@0.1.0" {
		t.Fatalf("rollback: %v, want a rollback plan after %v with A serving ingestion", back, bad["plan_id"])
	}
	if replay := request(t, "POST", "/v0/admin/plugins/plan/rollback", operator, body, 200); replay["plan_id"] != back["plan_id"] {
		t.Fatalf("the same key again: %v, want plan %v", replay["plan_id"], back["plan_id"])
	}
	history := request(t, "GET", "/v0/admin/plugins/plans?limit=2", operator, nil, 200)["items"].([]any)
	if len(history) != 2 || history[0].(map[string]any)["plan_id"] != back["plan_id"] || history[1].(map[string]any)["plan_id"] != bad["plan_id"] {
		t.Fatalf("plan history %v, want the rollback then B's plan", history)
	}

	deadline = time.Now().Add(60 * time.Second)
	var v map[string]any
	for {
		v = request(t, "GET", version, admin, nil, 200)
		if v["availability"].(map[string]any)["state"] == "quarantined" {
			break
		}
		if v["availability"].(map[string]any)["searchable"] == true || time.Now().After(deadline) {
			t.Fatalf("the Version pinned to B: %v, want it stopped, never processed by A", v)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if d := v["diagnostics"].([]any); len(d) != 1 || d[0].(map[string]any)["code"] != "pinned_plan_stopped" || d[0].(map[string]any)["plan"] != bad["plan_id"] || d[0].(map[string]any)["plugin_version"] != "0.2.0" {
		t.Fatalf("diagnostics %v, want pinned_plan_stopped naming B's plan and 0.2.0", d)
	}

	// Only A runs: a Record found by meaning went through it.
	next := ingestEnriched(t, corpusID, "after-rollback-"+run, "kelp", "Kelp forests swayed below the pier at low tide.")
	hits := request(t, "POST", "/v0/search", admin, map[string]any{"query": "kelp forests pier", "corpus_ids": []string{corpusID}, "mode": "semantic"}, 200)["items"].([]any)
	if len(hits) == 0 || hits[0].(map[string]any)["version_id"] != next {
		t.Fatalf("a Record ingested after the rollback, with only A running: %v", hits)
	}
	replayed := awaitReceipt(t, request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "bad-release-"+run, "gulls", gullsText), 202)["receipt_id"].(string))
	if r := request(t, "GET", "/v0/records/"+replayed["record_id"].(string)+"/versions/"+replayed["version_id"].(string), admin, nil, 200); r["availability"].(map[string]any)["searchable"] != true {
		t.Fatalf("the Record B produced after the rollback: %v, want it still searchable", r)
	}

	refused := request(t, "POST", "/v0/admin/plugins/plan/rollback", operator, map[string]any{"idempotency_key": "rollback-to-b-" + run}, 409)
	if refused["code"] != "plugin_unreachable" {
		t.Fatalf("rolling back to B while it is down: %v, want plugin_unreachable", refused)
	}
	if active := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200); active["plan_id"] != back["plan_id"] {
		t.Fatalf("a refused rollback changed the plan to %v", active["plan_id"])
	}
}
