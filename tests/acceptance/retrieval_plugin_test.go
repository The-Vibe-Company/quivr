package acceptance

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The sample retrieval plugin (sdks/go/examples/fusion-retriever) is pinned
// (scripts/retrieval_plugin.py). It ranks every search: keyword and vector
// candidates the engine served, fused by reciprocal rank with an explanation
// per hit. The API lists its profiles, answers each with its name, keeps
// balanced as the name of default and refuses a profile it does not declare.
func TestRetrievalPlugin(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" || os.Getenv("QUIVR_TEST_RETRIEVAL_PLUGIN") == "" {
		t.Skip("make verify pins the sample retrieval plugin")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := fmt.Sprint(time.Now().UnixNano())
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Retrieval plugin", "idempotency_key": "retrieval-plugin-" + run}, 201)["corpus_id"].(string)
	harbour := ingestEnriched(t, corpusID, "harbour-"+run, "harbour", "The harbour stayed closed all day after the storm.")
	ingestEnriched(t, corpusID, "market-"+run, "market", "The flower market opened early.")

	profiles := []string{}
	for _, item := range request(t, "GET", "/v0/search/profiles", admin, nil, 200)["items"].([]any) {
		p := item.(map[string]any)
		if p["provider"].(map[string]any)["plugin_id"] != "example.fusion_retriever" {
			t.Fatalf("profile %v is not the pinned plugin's", p)
		}
		profiles = append(profiles, p["name"].(string))
	}
	if strings.Join(profiles, ",") != "default,deep" {
		t.Fatalf("listed profiles %v", profiles)
	}
	search := func(profile string, want int) map[string]any {
		t.Helper()
		body := map[string]any{"query": "harbour closed", "corpus_ids": []string{corpusID}}
		if profile != "" {
			body["profile"] = profile
		}
		return request(t, "POST", "/v0/search", admin, body, want)
	}
	for profile, want := range map[string]struct {
		name   string
		rounds float64
	}{"": {"default", 2}, "balanced": {"default", 2}, "deep": {"deep", 3}} {
		out := search(profile, 200)
		resolved := out["retrieval_profile"].(map[string]any)
		items := out["items"].([]any)
		usage, _ := out["usage"].(map[string]any)
		if resolved["name"] != want.name || resolved["version"] != "plugin:example.fusion_retriever@0.1.0/"+want.name || usage == nil || usage["rounds"] != want.rounds || len(items) == 0 {
			t.Fatalf("profile %q answered %v", profile, out)
		}
		first := items[0].(map[string]any)
		explanation, _ := first["explanation"].(string)
		if first["version_id"] != harbour || !strings.Contains(explanation, "by keywords") || !strings.Contains(explanation, "by vectors") {
			t.Fatalf("profile %q: the harbour Record ranks first with both lists in its explanation: %v", profile, first)
		}
	}
	if code := search("fast", 422)["code"]; code != "unsupported_profile" {
		t.Fatalf("an undeclared profile: %v", code)
	}
}
