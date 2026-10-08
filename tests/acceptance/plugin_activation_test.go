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

// inFlightCommand is the Record whose processing starts on 0.1.0 and is
// still running when 0.2.0 is activated; posting it again replays it.
func inFlightCommand(t *testing.T) map[string]any {
	t.Helper()
	corpusID, run := ingestionPluginCorpus(t)
	return inlineCommand(corpusID, "in-flight-"+run, "in-flight", "The pilot boat waited at the harbour mouth for the freighter.")
}

// TestPluginActivation registers a second version of the pinned ingestion
// plugin through the operator API. Registered with another build's manifest,
// the Contract Runner rejects it on the discovery check and it cannot be
// activated; registered with its own, it is validated, and activating it
// records a new plan naming it while the previous plan stays readable.
//
// The harness froze 0.1.0, so its calls hang rather than fail: a Record
// whose processing started before the activation stays pinned to 0.1.0,
// which reads as draining, while a search answers at once through 0.2.0.
func TestPluginActivation(t *testing.T) {
	operator, endpoint, pinned, next := activationSetup(t)
	run := os.Getenv("QUIVR_TEST_INGESTION_RUN")
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	spaces := map[string]string{"example.hash_embedder.small": "served", "example.hash_embedder.large": "evaluation"}
	before := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200)
	if got := planRoles(before)["ingestion:core.ingest"]; got != "core.ingest@1.0.0" {
		t.Fatalf("core stays pinned beside the fixture: %v", before)
	}
	if got := planRoles(before)["ingestion:example.hash_embedder"]; got != "example.hash_embedder@0.1.0" {
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
	request(t, "GET", "/v0/admin/plugins", admin, nil, 403)
	request(t, "POST", "/v0/admin/plugins/"+validated["registration_id"].(string)+"/activate", admin, map[string]any{}, 403)
	// The receipt resolves in the processing's first step, which pins the
	// plan; its segmentation then waits on the frozen 0.1.0.
	awaitReceipt(t, request(t, "POST", "/v0/records", admin, inFlightCommand(t), 202)["receipt_id"].(string))
	plan := request(t, "POST", "/v0/admin/plugins/"+validated["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
	if planRoles(plan)["ingestion:core.ingest"] != planRoles(before)["ingestion:core.ingest"] {
		t.Fatalf("hash activation changed core ingestion: %v", plan)
	}
	if plan["plan_id"] == before["plan_id"] || plan["source"] != "activation" || planRoles(plan)["ingestion:example.hash_embedder"] != "example.hash_embedder@0.2.0" {
		t.Fatalf("activation: %v, want a new plan with 0.2.0 serving ingestion", plan)
	}
	if active := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200); active["plan_id"] != plan["plan_id"] {
		t.Fatalf("active plan %v, want the activated %v", active["plan_id"], plan["plan_id"])
	}
	earlier := request(t, "GET", "/v0/admin/plugins/plans/"+before["plan_id"].(string), operator, nil, 200)
	if planRoles(earlier)["ingestion:example.hash_embedder"] != "example.hash_embedder@0.1.0" {
		t.Fatalf("the previous plan changed: %v", earlier)
	}
	if old := registrationAt(t, operator, "0.1.0", os.Getenv("QUIVR_TEST_ROLLBACK_ENDPOINT")); old["state"] != "draining" || old["pinned_work"].(float64) < 1 {
		t.Fatalf("0.1.0 with a Record in flight: %v, want draining", old)
	}
	corpusID, _ := ingestionPluginCorpus(t)
	// The api that served the activation follows it at once, so this query
	// is encoded by 0.2.0; sent to the frozen 0.1.0 it would time out.
	hits := request(t, "POST", "/v0/search", admin, map[string]any{"query": "vineyard harvest", "corpus_ids": []string{corpusID}, "mode": "semantic"}, 200)["items"].([]any)
	if len(hits) == 0 || hits[0].(map[string]any)["vector_space_id"] != pluginServedSpace {
		t.Fatalf("a semantic search while 0.1.0 drains: %v", hits)
	}
}

// TestPluginActivationDrains runs once the harness let 0.1.0 run again, both
// versions up: the original receipt drains 0.1.0 while an independently pinned
// serving projection uses 0.2.0. The timeline names that served projection.
func TestPluginActivationDrains(t *testing.T) {
	operator, _, _, _ := activationSetup(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receipt := awaitRetrievalReady(t, request(t, "POST", "/v0/records", admin, inFlightCommand(t), 202)["receipt_id"].(string))
	timeline := request(t, "GET", "/v0/admin/documents/"+receipt["version_id"].(string)+"/timeline", admin, nil, 200)
	segmentedBy := ""
	for _, raw := range timeline["steps"].([]any) {
		if step := raw.(map[string]any); step["step"] == "segmented" {
			segmentedBy, _ = step["plugin_version"].(string)
		}
	}
	if segmentedBy != "0.2.0" {
		t.Fatalf("the current serving projection uses %q, want 0.2.0 after the historical receipt handoff: %v", segmentedBy, timeline)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		old := registrationAt(t, operator, "0.1.0", os.Getenv("QUIVR_TEST_ROLLBACK_ENDPOINT"))
		if old["state"] == "inactive" && old["pinned_work"].(float64) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("0.1.0 never finished draining: %v", old)
		}
		time.Sleep(100 * time.Millisecond)
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

// registrationAt finds the registration of a plugin version at an endpoint.
func registrationAt(t *testing.T, operator, version, endpoint string) map[string]any {
	t.Helper()
	for _, raw := range request(t, "GET", "/v0/admin/plugins", operator, nil, 200)["items"].([]any) {
		r := raw.(map[string]any)
		if r["plugin_id"] == "example.hash_embedder" && r["version"] == version && r["endpoint"] == endpoint && r["state"] != "rejected" {
			return r
		}
	}
	t.Fatalf("no registration of example.hash_embedder@%s at %s", version, endpoint)
	return nil
}

// pinnedSetup is what scripts/ingestion_plugin.py gives the pinning scenario:
// 0.2.0 (A) serves the plan and is stopped; 0.1.0 (B), whose registration
// the configuration seeded, runs again at its address.
func pinnedSetup(t *testing.T) (operator, a, b string, record map[string]any) {
	t.Helper()
	operator, a, _, _ = activationSetup(t)
	b = os.Getenv("QUIVR_TEST_ROLLBACK_ENDPOINT")
	if b == "" {
		t.Skip("make verify restarts 0.1.0 for the pinning scenario")
	}
	corpusID, run := ingestionPluginCorpus(t)
	return operator, a, b, inlineCommand(corpusID, "pinned-"+run, "pinned", "The ferry crossed the fjord twice before the storm.")
}

// TestPinnedWorkStarts ingests a Record while A, which the active plan names,
// is stopped: its processing pins A's plan and retries. B is then activated,
// and A reads as draining, with that work pinned to it.
func TestPinnedWorkStarts(t *testing.T) {
	operator, a, b, record := pinnedSetup(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receipt := awaitReceipt(t, request(t, "POST", "/v0/records", admin, record, 202)["receipt_id"].(string))
	version := "/v0/records/" + receipt["record_id"].(string) + "/versions/" + receipt["version_id"].(string)
	deadline := time.Now().Add(30 * time.Second)
	for {
		// Retrying: its processing ran on A's plan and could not reach A.
		v := request(t, "GET", version, admin, nil, 200)
		if v["processing"].(map[string]any)["state"] == "retrying" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Version never retried with A stopped: %v", v)
		}
		time.Sleep(100 * time.Millisecond)
	}
	rollback := registrationAt(t, operator, "0.1.0", b)
	plan := request(t, "POST", "/v0/admin/plugins/"+rollback["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
	if planRoles(plan)["ingestion:example.hash_embedder"] != "example.hash_embedder@0.1.0" {
		t.Fatalf("activating 0.1.0 again: %v", plan)
	}
	if drained := registrationAt(t, operator, "0.2.0", a); drained["state"] != "draining" || drained["pinned_work"].(float64) < 1 {
		t.Fatalf("A once B is active: %v, want draining with the Record's processing pinned to it", drained)
	}
}

// TestPinnedWorkRetries observes the original receipt after a worker restart:
// B handles new work while A stays draining and its import keeps retrying.
func TestPinnedWorkRetries(t *testing.T) {
	operator, a, _, record := pinnedSetup(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	corpusID, run := ingestionPluginCorpus(t)
	ingestEnriched(t, corpusID, "after-switch-"+run, "lighthouse", "The lighthouse keeper logged every passing ship.")
	receipt := request(t, "POST", "/v0/records", admin, record, 202)
	version := "/v0/records/" + receipt["record_id"].(string) + "/versions/" + receipt["version_id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	for {
		v := request(t, "GET", version, admin, nil, 200)
		availability := v["availability"].(map[string]any)
		if availability["state"] == "quarantined" || availability["searchable"] == true {
			t.Fatalf("the import pinned to A must retry, never quarantine or move to B: %v", v)
		}
		if v["processing"].(map[string]any)["state"] == "retrying" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the import never retried after the restart: %v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if old := registrationAt(t, operator, "0.2.0", a); old["state"] != "draining" || old["pinned_work"].(float64) < 1 {
		t.Fatalf("unreachable A must retain its pending import: %v", old)
	}
}

// TestPinnedWorkDrains runs once the exact A build returns at its old address.
// Its receipt finishes and releases A while B continues serving the active plan.
// The existing 60s drain bound covers the real activity's 10s retry backoff;
// THE-1308's HTTP owner regression exercises budget exhaustion without time waits.
func TestPinnedWorkDrains(t *testing.T) {
	operator, a, _, record := pinnedSetup(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	corpusID, _ := ingestionPluginCorpus(t)
	original := request(t, "POST", "/v0/records", admin, record, 202)
	receipt := awaitRetrievalReady(t, original["receipt_id"].(string))
	if receipt["version_id"] != original["version_id"] || receipt["record_id"] != original["record_id"] {
		t.Fatalf("the resumed import changed identity: original %v, resumed %v", original, receipt)
	}
	awaitEnriched(t, admin, corpusID, "", receipt["record_id"].(string))
	version := request(t, "GET", "/v0/records/"+receipt["record_id"].(string)+"/versions/"+receipt["version_id"].(string), admin, nil, 200)
	if diagnostics, _ := version["diagnostics"].([]any); len(diagnostics) != 0 {
		t.Fatalf("restored A left the import blocked: %v", version)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		drained := registrationAt(t, operator, "0.2.0", a)
		if drained["state"] == "inactive" && drained["pinned_work"].(float64) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("A never finished draining after its build returned: %v", drained)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
