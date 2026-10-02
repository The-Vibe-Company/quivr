package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Descriptions the fake System One server (alerts.fake_system_one) judges by
// topic, in English and French.
const strikeDescription = "Dock workers going on strike at a harbour"

// fakeSystemOne is the fake System One server scripts/subscription_plugin.py
// starts for described alerts in make verify. The tests skip without it.
func fakeSystemOne(t *testing.T) string {
	t.Helper()
	keywordEvaluator(t)
	url := os.Getenv("QUIVR_TEST_FAKE_SYSTEM_ONE_URL")
	if url == "" {
		t.Skip("QUIVR_TEST_FAKE_SYSTEM_ONE_URL is set by scripts/subscription_plugin.py")
	}
	return url
}

// fakeRequests lists what the fake received for articles whose text contains marker.
func fakeRequests(t *testing.T, url, marker string) []map[string]any {
	t.Helper()
	response, err := http.Get(url + "/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, r := range body.Requests {
		if strings.Contains(r["article"].(string), marker) {
			out = append(out, r)
		}
	}
	return out
}

// ownedSubscription pins a Saved Query with the expression to the alerts
// evaluator, for an owner ("" for a global alert).
func ownedSubscription(t *testing.T, token, key, corpusID string, expression, configuration map[string]any, owner, destination string, want int) map[string]any {
	t.Helper()
	id, version := keywordEvaluator(t)
	query := request(t, "POST", "/v0/saved-queries", token, map[string]any{"idempotency_key": "described-query-" + key, "name": "Described " + key, "definition": map[string]any{
		"corpus_ids": []string{corpusID}, "expression": expression, "retrieval_profile": "default", "temporal_policy": "from_activation"}}, 201)
	if configuration == nil {
		configuration = map[string]any{}
	}
	body := map[string]any{"idempotency_key": "described-subscription-" + key, "name": "Described " + key,
		"saved_query_id": query["saved_query_id"], "saved_query_version_id": query["current_version"].(map[string]any)["version_id"],
		"evaluator": map[string]any{"plugin_id": id, "version": version, "configuration": configuration}, "destination_id": destination}
	if owner != "" {
		body["owner"] = owner
	}
	return request(t, "POST", "/v0/subscriptions", token, body, want)
}

func described(text string) map[string]any {
	return map[string]any{"kind": "described", "description": text}
}

// TestDescribedAlertEvidenceReachesTheAPI proves a described alert is evaluated
// through the configured classifier and its evidence reaches the public Match.
// Plugin route tests own thresholds, negative decisions and call deduplication;
// the engine batch tests own subscription fan-out and already-decided intents.
func TestDescribedAlertEvidenceReachesTheAPI(t *testing.T) {
	fakeSystemOne(t)
	id, _ := keywordEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := changeCorpus(t, "described-alerts-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	sub := ownedSubscription(t, admin, "described-"+run, c, described(strikeDescription), nil, "user-"+run, destinationA, 201)
	article := awaitReady(t, ingest(t, c, "described-"+run, "Harbour staff walk out ("+run+"). Dockers at the northern harbour began a walkout on Tuesday."))
	_, created := awaitMatches(t, admin, c, start, 1)
	notice := matchCreatedFor(created, sub["subscription_id"].(string))[0]
	if notice["monitoring"].(map[string]any)["record_id"] != article["record_id"] {
		t.Fatal("described Match must name the evaluated Record", notice, article)
	}
	evidence := request(t, "GET", "/v0/matches/"+notice["monitoring"].(map[string]any)["match_id"].(string), admin, nil, 200)["evidence"].(map[string]any)
	details, _ := evidence["details"].(map[string]any)
	if evidence["evaluator"].(map[string]any)["plugin_id"] != id || details["kind"] != "described" || details["model"] != "jev-1.13.0" || details["score"] != 0.92 ||
		!strings.Contains(evidence["explanation"].(string), "score 0.92") || len(evidence["part_keys"].([]any)) == 0 {
		t.Fatal("the evidence must name the classifier, its score and the Parts it saw", evidence)
	}
}

// TestKeylessRefusesDescribedAlerts runs against the core scripts/local.py
// restarts without optional keys, which pins the alerts plugin as an
// installation without a TypeSafe key: kinds ["keywords"]. A described alert
// is refused at creation with a clear 422, and a keyword alert is accepted.
func TestKeylessRefusesDescribedAlerts(t *testing.T) {
	token := os.Getenv("QUIVR_TEST_KEYLESS")
	if os.Getenv("QUIVR_TEST_KEYLESS_MODE") == "" || token == "" {
		t.Skip("make verify restarts the core without optional keys for this test")
	}
	keywordEvaluator(t)
	run := monitoringRun()
	c := request(t, "POST", "/v0/corpora", token, map[string]any{"name": "Keyless alerts " + run, "idempotency_key": "keyless-alerts-" + run}, 201)["corpus_id"].(string)
	refused := ownedSubscription(t, token, "keyless-described-"+run, c, described(strikeDescription), nil, "", "local-receiver-org-k", 422)
	if refused["code"] != "invalid_expression" || refused["field"] != "/saved_query_version_id" ||
		!strings.Contains(refused["message"].(string), `does not offer the kind "described"`) {
		t.Fatal("a described alert must be refused without a classifier", refused)
	}
	ownedSubscription(t, token, "keyless-keywords-"+run, c, map[string]any{"kind": "keywords", "match": term("strike")}, nil, "", "local-receiver-org-k", 201)
	ownedSubscription(t, token, "keyless-vectors-"+run, c, map[string]any{"kind": "meaning", "meaning_check": "vectors", "description": strikeDescription}, nil, "", "local-receiver-org-k", 201)
}
