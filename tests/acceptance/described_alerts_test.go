package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Descriptions the fake System One server (alerts.fake_system_one) judges by
// topic, in English and French.
const (
	strikeDescription = "Dock workers going on strike at a harbour"
	visaDescription   = "Diplomatic tensions over visas"
)

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

// TestDescribedAlertsJudgedInOneCall saves plain-language alerts for two end
// users and one global alert, next to a keyword alert. A rephrased English
// article and a translated French one alert every Subscription whose
// description they fit, an unrelated article alerts none, and a stricter
// per-alert threshold holds back. The Match evidence carries the classifier's
// score. Each article costs exactly one classifier call, which asks every
// distinct description once, whatever the number of Subscriptions or owners.
func TestDescribedAlertsJudgedInOneCall(t *testing.T) {
	fake := fakeSystemOne(t)
	id, _ := keywordEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := changeCorpus(t, "described-alerts-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)

	alice := ownedSubscription(t, admin, "alice-"+run, c, described(strikeDescription), nil, "user-a-"+run, destinationA, 201)
	bob := ownedSubscription(t, admin, "bob-"+run, c, described(strikeDescription), nil, "user-b-"+run, destinationA, 201)
	strict := ownedSubscription(t, admin, "strict-"+run, c, described(strikeDescription), map[string]any{"threshold": 0.95}, "user-b-"+run, destinationA, 201)
	visas := ownedSubscription(t, admin, "visas-"+run, c, described(visaDescription), nil, "", destinationA, 201)
	keywords := ownedSubscription(t, admin, "keywords-"+run, c, map[string]any{"kind": "keywords", "match": term("grève")}, nil, "", destinationA, 201)

	english := awaitReady(t, ingest(t, c, "described-en-"+run, "Harbour staff walk out ("+run+"). Dockers at the northern harbour began a walkout on Tuesday."))
	french := awaitReady(t, ingest(t, c, "described-fr-"+run, "Les dockers votent la grève au port ("+run+"). Au port de Portval, les dockers ont voté une grève de 48 heures."))
	unrelated := awaitReady(t, ingest(t, c, "described-other-"+run, "League final ends in a draw ("+run+"). The football final ended without a goal."))
	// alice and bob on both fitting articles, and the keyword alert on the French one.
	awaitMatches(t, admin, c, start, 5)
	// Let any late decision surface.
	time.Sleep(quietPeriod)
	seen, _ := drain(t, admin, c, start, 0)

	records := func(sub map[string]any) []string {
		var out []string
		for _, notice := range matchCreatedFor(seen, sub["subscription_id"].(string)) {
			out = append(out, notice["monitoring"].(map[string]any)["record_id"].(string))
		}
		sort.Strings(out)
		return out
	}
	fitting := []string{english["record_id"].(string), french["record_id"].(string)}
	sort.Strings(fitting)
	for name, tc := range map[string]struct {
		sub  map[string]any
		want []string
	}{
		"alice": {alice, fitting}, "bob": {bob, fitting}, "strict": {strict, nil}, "visas": {visas, nil},
		"keywords": {keywords, []string{french["record_id"].(string)}},
	} {
		if got := records(tc.sub); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: want Matches for %v, got %v (unrelated record %v)", name, tc.want, got, unrelated["record_id"])
		}
	}

	notice := matchCreatedFor(seen, alice["subscription_id"].(string))[0]
	evidence := request(t, "GET", "/v0/matches/"+notice["monitoring"].(map[string]any)["match_id"].(string), admin, nil, 200)["evidence"].(map[string]any)
	details, _ := evidence["details"].(map[string]any)
	if evidence["evaluator"].(map[string]any)["plugin_id"] != id || details["kind"] != "described" || details["model"] != "jev-1.13.0" || details["score"] != 0.92 ||
		!strings.Contains(evidence["explanation"].(string), "score 0.92") || len(evidence["part_keys"].([]any)) == 0 {
		t.Fatal("the evidence must name the classifier, its score and the Parts it saw", evidence)
	}

	// The unrelated article has no Match to wait for: wait until the classifier saw
	// every article, then let any second call surface.
	articles := []string{"Harbour staff", "Les dockers", "League final"}
	seenAll := func(calls []map[string]any) bool {
		for _, word := range articles {
			found := false
			for _, call := range calls {
				found = found || strings.Contains(call["article"].(string), word)
			}
			if !found {
				return false
			}
		}
		return true
	}
	for deadline := time.Now().Add(monitoringWait); !seenAll(fakeRequests(t, fake, run)); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the classifier never saw every article: %v", fakeRequests(t, fake, run))
		}
	}
	time.Sleep(quietPeriod)
	calls := fakeRequests(t, fake, run)
	perArticle := map[string]int{}
	for _, call := range calls {
		article := call["article"].(string)
		for _, word := range articles {
			if strings.Contains(article, word) {
				perArticle[word]++
			}
		}
		var asked []string
		for _, d := range call["descriptions"].([]any) {
			asked = append(asked, d.(string))
		}
		sort.Strings(asked)
		if strings.Join(asked, "|") != strings.Join(sortedCopy([]string{strikeDescription, visaDescription}), "|") {
			t.Errorf("each call must ask every distinct description once, got %v", asked)
		}
	}
	if len(calls) != 3 || perArticle["Harbour staff"] != 1 || perArticle["Les dockers"] != 1 || perArticle["League final"] != 1 {
		t.Fatalf("want exactly one classifier call per article, got %d calls: %v", len(calls), perArticle)
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

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
