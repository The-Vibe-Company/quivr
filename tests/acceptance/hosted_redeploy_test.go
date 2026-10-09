package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// One lifecycle regression, split around the harness's real build replacement.
// Pure registry tests cannot see discovery, query encoding or projection cutover.
type hostedRedeployState struct {
	Corpus, Version, OldRegistration, CoreRegistration, RollbackPlan, PromotedPlan string
}

func hostedRedeploySetup(t *testing.T) (string, string, string, string) {
	t.Helper()
	space := os.Getenv("QUIVR_TEST_HOSTED_REDEPLOY_SPACE")
	if space == "" {
		t.Skip("hosted build redeploy stack step")
	}
	// Keep the primary organization's persistence fixture pristine.
	return os.Getenv("QUIVR_TEST_OTHER"), os.Getenv("QUIVR_TEST_OPERATOR"), space + "@1", filepath.Join(os.Getenv("QUIVR_TEST_CAPTURES"), "hosted-redeploy-state.json")
}

func hostedRegistration(t *testing.T, operator, plugin string) string {
	t.Helper()
	list := request(t, "GET", "/v0/admin/plugins", operator, nil, 200)
	for _, item := range list["items"].([]any) {
		r := item.(map[string]any)
		if r["plugin_id"] == plugin && r["state"] == "active" {
			return r["registration_id"].(string)
		}
	}
	t.Fatalf("no active %s registration: %v", plugin, list)
	return ""
}

func hostedRedeploySearch(t *testing.T, admin string, s hostedRedeployState, space string) {
	t.Helper()
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		result := request(t, "POST", "/v0/search", admin, map[string]any{"query": "The library opens downtown on Wednesday.", "corpus_ids": []string{s.Corpus}, "mode": mode}, 200)
		hits := result["items"].([]any)
		if len(hits) != 1 || hits[0].(map[string]any)["version_id"] != s.Version {
			t.Fatalf("%s search after build replacement: %v", mode, result)
		}
		if mode != "lexical" && hits[0].(map[string]any)["vector_space_id"] != space {
			t.Fatalf("%s search used wrong space: %v, want %s", mode, result, space)
		}
	}
}

func TestHostedEmbeddingRedeployBefore(t *testing.T) {
	admin, operator, space, statePath := hostedRedeploySetup(t)
	run := monitoringRun()
	s := hostedRedeployState{OldRegistration: hostedRegistration(t, operator, "hosted.embed"), CoreRegistration: hostedRegistration(t, operator, "core.ingest")}
	s.Corpus = request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Build redeploy", "idempotency_key": "redeploy-" + run}, 201)["corpus_id"].(string)
	cursor := request(t, "GET", changesPath(s.Corpus, "", 0), admin, nil, 200)["next_cursor"].(string)
	accepted := request(t, "POST", "/v0/records", admin, inlineCommand(s.Corpus, "redeploy-"+run, "Library", "The library opens downtown on Wednesday."), 202)
	ready := awaitReceiptAs(t, admin, accepted["receipt_id"].(string))
	awaitEnriched(t, admin, s.Corpus, cursor, ready["record_id"].(string))
	s.Version = ready["version_id"].(string)
	// Evaluation is asynchronous; wait for its public coverage before promotion.
	deadline := time.Now().Add(10 * time.Second)
	for {
		spaces := request(t, "GET", "/v0/corpora/"+s.Corpus+"/vector-spaces", admin, nil, 200)
		covered := false
		for _, item := range spaces["items"].([]any) {
			sp := item.(map[string]any)
			covered = covered || sp["vector_space_id"] == space && coverage(sp) > 0
		}
		if covered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hosted evaluation never covered the document: %v", spaces)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.RollbackPlan = request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200)["plan_id"].(string)
	s.PromotedPlan = routingRequest(t, "POST", "/v0/admin/plugins/"+s.OldRegistration+"/activate", operator, map[string]any{}, 200)["plan_id"].(string)
	hostedRedeploySearch(t, admin, s, space)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostedEmbeddingRedeployAfter(t *testing.T) {
	admin, operator, space, statePath := hostedRedeploySetup(t)
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var s hostedRedeployState
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	next := hostedRegistration(t, operator, "hosted.embed")
	if next == s.OldRegistration {
		t.Fatal("redeploy did not replace the exact build registration")
	}
	plan := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200)
	if plan["plan_id"] == s.PromotedPlan || planRoles(plan)["ingestion-route:text/plain"] != "hosted.embed@1.2.0" || planRoles(plan)["ingestion-evaluation:text/plain:core.ingest"] == "" {
		t.Fatalf("redeploy lost promoted routing: %v", plan)
	}
	hostedRedeploySearch(t, admin, s, space)
	// The old registration really is stale; accepting it would break queries.
	refused := routingRequest(t, "POST", "/v0/admin/plugins/"+s.OldRegistration+"/activate", operator, map[string]any{}, 409)
	if refused["code"] != "plugin_unreachable" {
		t.Fatalf("stale activation: %v", refused)
	}
	refused = routingRequest(t, "POST", "/v0/admin/plugins/plan/rollback", operator, map[string]any{"idempotency_key": "redeploy-rollback-" + monitoringRun(), "plan_id": s.RollbackPlan, "pinned_work": "stop"}, 409)
	if refused["code"] != "plugin_unreachable" {
		t.Fatalf("exact rollback must explain stale build recovery: %v", refused)
	}
	if after := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200); after["plan_id"] != plan["plan_id"] {
		t.Fatalf("refused commands changed the plan: %v", after)
	}
	// Revert the serving owner while retaining the new hosted build for evaluation.
	back := routingRequest(t, "POST", "/v0/admin/plugins/"+s.CoreRegistration+"/activate", operator, map[string]any{}, 200)
	if planRoles(back)["ingestion-route:text/plain"] != "core.ingest@1.0.0" {
		t.Fatalf("previous owner was not promoted back: %v", back)
	}
	hostedRedeploySearch(t, admin, s, coreIngestSpace)
	// Start the coverage regression from a promoted, reachable current build.
	s.RollbackPlan = back["plan_id"].(string)
	s.PromotedPlan = routingRequest(t, "POST", "/v0/admin/plugins/"+next+"/activate", operator, map[string]any{}, 200)["plan_id"].(string)
	if err = os.WriteFile(statePath, []byte(mustJSON(t, s)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The harness now changes the endpoint, an incompatible startup replacement.
// Existing Corpora keep their serving routes; the plan's default owner must
// also fill new documents so returning to that owner needs no backfill.
func TestHostedEmbeddingRollbackCoverage(t *testing.T) {
	admin, operator, space, statePath := hostedRedeploySetup(t)
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var s hostedRedeployState
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	plan := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200)
	if planRoles(plan)["ingestion-default"] != "core.ingest@1.0.0" || planRoles(plan)["ingestion-route:text/plain"] != "" || planRoles(plan)["ingestion-evaluation:text/plain:hosted.embed"] == "" {
		t.Fatalf("expected default/evaluation plan after endpoint replacement: %v", plan)
	}
	cursor := request(t, "GET", changesPath(s.Corpus, "", 0), admin, nil, 200)["next_cursor"].(string)
	accepted := request(t, "POST", "/v0/records", admin, inlineCommand(s.Corpus, "rollback-coverage-"+monitoringRun(), "Tram", "The mountain tram arrives every Friday."), 202)
	ready := awaitReceiptAs(t, admin, accepted["receipt_id"].(string))
	awaitEnriched(t, admin, s.Corpus, cursor, ready["record_id"].(string))
	// Optional projection work is asynchronous. Its public coverage must
	// eventually include both current documents in both owners' spaces.
	deadline := time.Now().Add(10 * time.Second)
	for {
		spaces := request(t, "GET", "/v0/corpora/"+s.Corpus+"/vector-spaces", admin, nil, 200)
		covered := map[string]bool{}
		for _, item := range spaces["items"].([]any) {
			sp := item.(map[string]any)
			covered[sp["vector_space_id"].(string)] = sp["coverage"].(map[string]any)["versions_covered"] == float64(2)
		}
		if covered[coreIngestSpace] && covered[space] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new document lacks rollback coverage: %v", spaces)
		}
		time.Sleep(20 * time.Millisecond)
	}
	current := hostedRegistration(t, operator, "hosted.embed")
	routingRequest(t, "POST", "/v0/admin/plugins/"+current+"/activate", operator, map[string]any{}, 200)
	routingRequest(t, "POST", "/v0/admin/plugins/plan/rollback", operator, map[string]any{"idempotency_key": "coverage-rollback-" + monitoringRun(), "plan_id": s.RollbackPlan, "pinned_work": "drain"}, 200)
	result := request(t, "POST", "/v0/search", admin, map[string]any{"query": "The mountain tram arrives every Friday.", "corpus_ids": []string{s.Corpus}, "mode": "semantic", "limit": 10}, 200)
	found := false
	for _, raw := range result["items"].([]any) {
		hit := raw.(map[string]any)
		found = found || hit["version_id"] == ready["version_id"] && hit["vector_space_id"] == coreIngestSpace
	}
	if !found {
		t.Fatalf("rollback lost new document: %v", result)
	}
	// Restore the harness's original exact plan for later plugin scenarios.
	routingRequest(t, "POST", "/v0/admin/plugins/plan/rollback", operator, map[string]any{"idempotency_key": "redeploy-cleanup-" + monitoringRun(), "plan_id": os.Getenv("QUIVR_TEST_HOSTED_REDEPLOY_ORIGINAL_PLAN"), "pinned_work": "stop"}, 200)
}
