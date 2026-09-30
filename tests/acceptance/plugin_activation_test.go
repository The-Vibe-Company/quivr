package acceptance

import (
	"os"
	"testing"
	"time"
)

// activationSetup is what scripts/ingestion_plugin.py gives the activation
// scenario: the sample ingestion plugin at 0.1.0 is pinned, and the same
// plugin built as 0.2.0 runs at a second address.
func activationSetup(t *testing.T) (operator, endpoint string, pinned, next []byte) {
	t.Helper()
	operator, endpoint = os.Getenv("QUIVR_TEST_OPERATOR"), os.Getenv("QUIVR_TEST_ACTIVATION_ENDPOINT")
	if os.Getenv("QUIVR_TEST_URL") == "" || operator == "" || endpoint == "" {
		t.Skip("make verify runs a second version of the sample ingestion plugin")
	}
	var err error
	if pinned, err = os.ReadFile(os.Getenv("QUIVR_TEST_ACTIVATION_PINNED_MANIFEST")); err != nil {
		t.Fatal(err)
	}
	if next, err = os.ReadFile(os.Getenv("QUIVR_TEST_ACTIVATION_MANIFEST")); err != nil {
		t.Fatal(err)
	}
	return operator, endpoint, pinned, next
}

// awaitCheck waits for a registration's Contract Runner check to settle.
func awaitCheck(t *testing.T, operator, location string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := request(t, "GET", location, operator, nil, 200)
		if r["state"] != "registered" {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("the registration check never settled: %v", r)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func planRoles(plan map[string]any) map[string]string {
	out := map[string]string{}
	for _, raw := range plan["roles"].([]any) {
		role := raw.(map[string]any)
		out[role["role"].(string)] = role["plugin_id"].(string) + "@" + role["version"].(string)
	}
	return out
}

// TestPluginActivation registers a second version of the pinned ingestion
// plugin through the operator API. Registered with another build's manifest,
// the Contract Runner rejects it on the discovery check and it cannot be
// activated; registered with its own, it is validated, and activating it
// records a new plan naming it while the previous plan stays readable.
func TestPluginActivation(t *testing.T) {
	operator, endpoint, pinned, next := activationSetup(t)
	run := os.Getenv("QUIVR_TEST_INGESTION_RUN")
	spaces := map[string]string{"example.hash_embedder.small": "served", "example.hash_embedder.large": "evaluation"}
	before := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200)
	if got := planRoles(before)["ingestion"]; got != "example.hash_embedder@0.1.0" {
		t.Fatalf("before the activation ingestion is served by %q, want the pinned 0.1.0 (plan %v)", got, before)
	}
	register := func(key string, manifest []byte) map[string]any {
		t.Helper()
		body := map[string]any{"idempotency_key": key + "-" + run, "endpoint": endpoint, "manifest": string(manifest), "spaces": spaces}
		accepted := request(t, "POST", "/v0/admin/plugins", operator, body, 202)
		return awaitCheck(t, operator, "/v0/admin/plugins/"+accepted["registration_id"].(string))
	}

	// 0.1.0's manifest at 0.2.0's address: discovery reports another digest.
	mismatched := register("activation-mismatch", pinned)
	check, _ := mismatched["check"].(map[string]any)
	failing := map[string]bool{}
	if check != nil {
		for _, raw := range check["checks"].([]any) {
			c := raw.(map[string]any)
			if c["status"] == "fail" && len(c["issues"].([]any)) > 0 {
				failing[c["id"].(string)] = true
			}
		}
	}
	if mismatched["state"] != "rejected" || check == nil || check["certified"] != false || !failing["discovery"] {
		t.Fatalf("a plugin built from another manifest: %v, want rejected with the discovery check failing", mismatched)
	}
	refused := request(t, "POST", "/v0/admin/plugins/"+mismatched["registration_id"].(string)+"/activate", operator, map[string]any{}, 409)
	if refused["code"] != "registration_not_validated" {
		t.Fatalf("activating a rejected registration: %v", refused)
	}

	validated := register("activation-next", next)
	if validated["state"] != "validated" || validated["version"] != "0.2.0" || validated["check"].(map[string]any)["certified"] != true {
		t.Fatalf("the 0.2.0 build: %v, want validated", validated)
	}
	request(t, "GET", "/v0/admin/plugins", os.Getenv("QUIVR_TEST_ADMIN"), nil, 403)
	request(t, "POST", "/v0/admin/plugins/"+validated["registration_id"].(string)+"/activate", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{}, 403)
	plan := request(t, "POST", "/v0/admin/plugins/"+validated["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
	if plan["plan_id"] == before["plan_id"] || plan["source"] != "activation" || planRoles(plan)["ingestion"] != "example.hash_embedder@0.2.0" {
		t.Fatalf("activation: %v, want a new plan with 0.2.0 serving ingestion", plan)
	}
	if active := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200); active["plan_id"] != plan["plan_id"] {
		t.Fatalf("active plan %v, want the activated %v", active["plan_id"], plan["plan_id"])
	}
	earlier := request(t, "GET", "/v0/admin/plugins/plans/"+before["plan_id"].(string), operator, nil, 200)
	if planRoles(earlier)["ingestion"] != "example.hash_embedder@0.1.0" {
		t.Fatalf("the previous plan changed: %v", earlier)
	}
}

// TestPluginActivationIngests runs once the harness stopped 0.1.0: only 0.2.0
// can segment and embed, so a new Record that becomes searchable by meaning,
// with its query encoded by the plugin too, went through the activated
// version in both the worker and the api, neither restarted.
func TestPluginActivationIngests(t *testing.T) {
	activationSetup(t)
	corpusID, run := ingestionPluginCorpus(t)
	orchard := ingestEnriched(t, corpusID, "orchard-"+run, "orchard", "The orchard picked its last apples before the frost.")
	hits := request(t, "POST", "/v0/search", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"query": "orchard apples frost", "corpus_ids": []string{corpusID}, "mode": "semantic"}, 200)["items"].([]any)
	if len(hits) == 0 || hits[0].(map[string]any)["version_id"] != orchard || hits[0].(map[string]any)["vector_space_id"] != pluginServedSpace {
		t.Fatalf("a Record ingested after the activation, with only 0.2.0 running: %v", hits)
	}
}
