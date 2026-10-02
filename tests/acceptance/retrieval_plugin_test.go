package acceptance

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The sample retrieval plugin and two identities of the fake plugin are pinned
// by scripts/retrieval_plugin.py. The public API lists every installed profile,
// routes aliases and full names to each provider, and preserves the sample's
// keyword/vector ranking semantics.
func TestRetrievalPlugin(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" || os.Getenv("QUIVR_TEST_RETRIEVAL_PLUGIN") == "" {
		t.Skip("make verify pins multiple retrieval plugins")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := fmt.Sprint(time.Now().UnixNano())
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Retrieval plugin", "idempotency_key": "retrieval-plugin-" + run}, 201)["corpus_id"].(string)
	harbour := ingestEnriched(t, corpusID, "harbour-"+run, "harbour", "The harbour stayed closed all day after the storm.")
	ingestEnriched(t, corpusID, "market-"+run, "market", "The flower market opened early.")

	profiles := map[string]string{
		"example.fusion_retriever/default":    "default",
		"example.fusion_retriever/deep":       "deep",
		"quivr-test.first_retriever/default":  "first",
		"quivr-test.second_retriever/default": "second",
	}
	for _, item := range request(t, "GET", "/v0/search/profiles", admin, nil, 200)["items"].([]any) {
		p := item.(map[string]any)
		full := p["full_name"].(string)
		alias, found := profiles[full]
		aliases := p["aliases"].([]any)
		plugin, local, _ := strings.Cut(full, "/")
		if !found || p["name"] != local || p["provider"].(map[string]any)["plugin_id"] != plugin || len(aliases) != 1 || aliases[0] != alias {
			t.Fatalf("unexpected installed profile: %v", p)
		}
		delete(profiles, full)
	}
	if len(profiles) != 0 {
		t.Fatalf("installed profiles missing from the list: %v", profiles)
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
		name, plugin, local string
		rounds              float64
	}{
		"":                                    {"default", "example.fusion_retriever", "default", 2},
		"balanced":                            {"default", "example.fusion_retriever", "default", 2},
		"default":                             {"default", "example.fusion_retriever", "default", 2},
		"deep":                                {"deep", "example.fusion_retriever", "deep", 3},
		"example.fusion_retriever/default":    {"example.fusion_retriever/default", "example.fusion_retriever", "default", 2},
		"example.fusion_retriever/deep":       {"example.fusion_retriever/deep", "example.fusion_retriever", "deep", 3},
		"first":                               {"first", "quivr-test.first_retriever", "default", 2},
		"quivr-test.first_retriever/default":  {"quivr-test.first_retriever/default", "quivr-test.first_retriever", "default", 2},
		"second":                              {"second", "quivr-test.second_retriever", "default", 2},
		"quivr-test.second_retriever/default": {"quivr-test.second_retriever/default", "quivr-test.second_retriever", "default", 2},
	} {
		out := search(profile, 200)
		resolved := out["retrieval_profile"].(map[string]any)
		items := out["items"].([]any)
		usage, _ := out["usage"].(map[string]any)
		if resolved["name"] != want.name || resolved["version"] != "plugin:"+want.plugin+"@0.1.0/"+want.local || usage == nil || usage["rounds"] != want.rounds || len(items) == 0 {
			t.Fatalf("profile %q answered %v", profile, out)
		}
		first := items[0].(map[string]any)
		explanation, _ := first["explanation"].(string)
		if first["version_id"] != harbour {
			t.Fatalf("profile %q: expected the harbour Record first: %v", profile, first)
		}
		if want.plugin == "example.fusion_retriever" {
			if !strings.Contains(explanation, "by keywords") || !strings.Contains(explanation, "by vectors") {
				t.Fatalf("profile %q: expected both candidate lists in the sample's explanation: %v", profile, first)
			}
		} else if explanation != want.plugin+": reciprocal rank fusion" {
			t.Fatalf("profile %q reached the wrong fake provider: %v", profile, first)
		}
	}
	for _, profile := range []string{"fast", "quivr-test.missing_retriever/default", "example.fusion_retriever/missing"} {
		if code := search(profile, 422)["code"]; code != "unsupported_profile" {
			t.Fatalf("undeclared profile %q: %v", profile, code)
		}
	}
}
