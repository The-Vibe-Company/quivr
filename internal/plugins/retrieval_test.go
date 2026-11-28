package plugins_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

const retrieverManifest = "../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml"

// A retrieval-only pin needs no routes, applies the manifest's defaults, and
// several retrieval plugins can be pinned together.
func TestLoadPinsRoutesSearchToSeveralRetrievalPlugins(t *testing.T) {
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: retrieverManifest, Endpoint: "http://127.0.0.1:9906"}})
	if err != nil {
		t.Fatal(err)
	}
	r := set.Retrievals()[0].Manifest.Contributions.Retrieval
	if got := strings.Join(r.ProfileNames(), ","); got != "default,deep" || r.Limits.MaxResponseBytes != plugins.DefaultRetrievalMaxResponseBytes {
		t.Fatalf("profiles %s, limits %+v", got, r.Limits)
	}
	other := writePinManifest(t, strings.Replace(readFile(t, retrieverManifest), "id: example.fusion_retriever", "id: acme.other_retriever", 1))
	set, err = plugins.LoadPins([]plugins.PinConfig{{Manifest: retrieverManifest, Endpoint: "http://127.0.0.1:9906"}, {Manifest: other, Endpoint: "http://127.0.0.1:9907"}})
	if err != nil {
		t.Fatalf("several search plugins refused: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A profile's hard bound gives a search four times its latency objective,
// never less than 2 s, and never 10 s or more, so the error that ends a search
// always reaches the client before the api's 10 s write timeout (THE-813).
func TestSearchHardBound(t *testing.T) {
	for objective, want := range map[int]time.Duration{50: 2 * time.Second, 1000: 4 * time.Second, 2000: 8 * time.Second, 3000: 9 * time.Second, 10000: 9 * time.Second} {
		if got := (plugins.RetrievalProfile{MaxLatencyMS: objective}).Deadline(); got != want {
			t.Errorf("objective %d ms: bound %s, want %s", objective, got, want)
		}
	}
}

// Deployment alias validation owns startup errors and single-provider inference:
// a wrong target must fail before dependencies are contacted, and a missing map
// must never silently select one of several plugins.
func TestRetrievalProfileAliases(t *testing.T) {
	first := plugins.PinConfig{Manifest: retrieverManifest, Endpoint: "http://127.0.0.1:9906"}
	other := plugins.PinConfig{Manifest: writePinManifest(t, strings.Replace(readFile(t, retrieverManifest), "id: example.fusion_retriever", "id: acme.other_retriever", 1)), Endpoint: "http://127.0.0.1:9907"}
	for _, tc := range []struct {
		name      string
		pins      []plugins.PinConfig
		aliases   map[string]string
		wantError string
	}{
		{name: "legacy", pins: []plugins.PinConfig{first}},
		{name: "several", pins: []plugins.PinConfig{first, other}, aliases: map[string]string{"default": "example.fusion_retriever/default", "deep": "acme.other_retriever/deep", "careful": "acme.other_retriever/deep"}},
		{name: "missing map", pins: []plugins.PinConfig{first, other}, wantError: "must map default"},
		{name: "missing default", pins: []plugins.PinConfig{first}, aliases: map[string]string{}, wantError: "must contain default"},
		{name: "deep-only needs default alias", pins: []plugins.PinConfig{{Manifest: filepath.Join(fixtures, "manifests/valid/retrieval-deep-only.yaml"), Endpoint: first.Endpoint}}, wantError: "must map default"},
		{name: "unknown plugin", pins: []plugins.PinConfig{first}, aliases: map[string]string{"default": "missing/default"}, wantError: "unknown profile"},
		{name: "unknown profile", pins: []plugins.PinConfig{first}, aliases: map[string]string{"default": "example.fusion_retriever/missing"}, wantError: "unknown profile"},
		{name: "full alias", pins: []plugins.PinConfig{first}, aliases: map[string]string{"default": "example.fusion_retriever/default", "other/default": "example.fusion_retriever/deep"}, wantError: "short name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, err := plugins.LoadPins(tc.pins)
			if err != nil {
				t.Fatal(err)
			}
			profiles, err := set.RetrievalProfiles(tc.aliases)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("want %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if profiles[0].FullName != "example.fusion_retriever/default" || strings.Join(profiles[0].Aliases, ",") != "default" {
				t.Fatalf("default profile: %+v", profiles[0])
			}
			if tc.name == "legacy" && (len(profiles) != 2 || profiles[1].FullName != "example.fusion_retriever/deep" || strings.Join(profiles[1].Aliases, ",") != "deep") {
				t.Fatalf("legacy profiles: %+v", profiles)
			}
			if tc.name == "several" && (len(profiles) != 4 || profiles[1].FullName != "acme.other_retriever/deep" || strings.Join(profiles[1].Aliases, ",") != "careful,deep") {
				t.Fatalf("all profiles: %+v", profiles)
			}
			queries := map[string]string{
				"":        "example.fusion_retriever/default",
				"default": "example.fusion_retriever/default", "missing": "", "missing/default": "",
				"example.fusion_retriever/default": "example.fusion_retriever/default",
			}
			queries["example.fusion_retriever/deep"] = "example.fusion_retriever/deep"
			queries["deep"] = "example.fusion_retriever/deep"
			if tc.name == "several" {
				queries["deep"], queries["careful"] = "acme.other_retriever/deep", "acme.other_retriever/deep"
				queries["acme.other_retriever/default"] = "acme.other_retriever/default"
			}
			for query, fullName := range queries {
				selected, ok := set.ResolveRetrievalProfile(query, tc.aliases)
				if ok != (fullName != "") || selected.FullName != fullName || (ok && selected.Pin.Manifest.ID+"/"+selected.Name != fullName) {
					t.Fatalf("route %q: %+v, found %t, want %q", query, selected, ok, fullName)
				}
			}
		})
	}
}

// CheckSearchOutput enforces Plugin API 0.21 gates for k > 100 and include_lead
// so older plugins cannot opt into 0.21 wire features without declaring 0.21.
func TestCheckSearchOutputPluginAPI021Gates(t *testing.T) {
	req := plugins.SearchRequest{Round: 1, Limit: 10}
	manifest := func(apiRange string, maxCand int) *plugins.Manifest {
		return &plugins.Manifest{
			ID:            "example.retriever",
			Version:       "1.0.0",
			Compatibility: plugins.Compatibility{Engine: ">=0.1.0 <0.3.0", PluginAPI: apiRange},
			Requires:      []plugins.Requirement{{Plugin: "core.retrieve", Version: ">=0.1.0 <1.0.0", Profiles: []string{"default"}}},
			Contributions: plugins.Contributions{
				Retrieval: &plugins.Retrieval{
					Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 500}},
					Limits:   plugins.RetrievalLimits{MaxRounds: 2, MaxRequests: 4, MaxCandidates: maxCand, MaxResponseBytes: 1 << 20},
				},
			},
		}
	}
	for _, tc := range []struct {
		name     string
		apiRange string
		maxCand  int
		body     string
		wantCode string
	}{
		{name: "0.21 accepts k=250 and include_lead", apiRange: ">=0.21.0 <0.22.0", maxCand: 300, body: `{"requests":[{"primitive":"bm25","query_text":"harbour","k":250,"include_lead":true}]}`},
		{name: "0.21 accepts include_lead on profile", apiRange: ">=0.21.0 <0.22.0", maxCand: 50, body: `{"requests":[{"primitive":"profile","profile":{"name":"core.retrieve/default","limit":10},"include_lead":true}]}`},
		{name: "0.21 rejects k above max_candidates", apiRange: ">=0.21.0 <0.22.0", maxCand: 200, body: `{"requests":[{"primitive":"bm25","query_text":"harbour","k":250}]}`, wantCode: plugins.CodeCandidateLimit},
		{name: "0.20 rejects k above 100", apiRange: ">=0.20.0 <0.21.0", maxCand: 300, body: `{"requests":[{"primitive":"bm25","query_text":"harbour","k":150}]}`, wantCode: plugins.CodeCandidateLimit},
		{name: "0.20 rejects include_lead", apiRange: ">=0.20.0 <0.21.0", maxCand: 50, body: `{"requests":[{"primitive":"bm25","query_text":"harbour","k":10,"include_lead":true}]}`, wantCode: plugins.CodeIncompatiblePluginAPI},
		{name: "wide range rejects include_lead", apiRange: ">=0.7.0 <0.22.0", maxCand: 50, body: `{"requests":[{"primitive":"bm25","query_text":"harbour","k":10,"include_lead":true}]}`, wantCode: plugins.CodeIncompatiblePluginAPI},
		{name: "k above 400 fails schema", apiRange: ">=0.21.0 <0.22.0", maxCand: 400, body: `{"requests":[{"primitive":"bm25","query_text":"harbour","k":401}]}`, wantCode: plugins.CodeSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := plugins.CheckSearchOutput([]byte(tc.body), req, manifest(tc.apiRange, tc.maxCand))
			if tc.wantCode == "" {
				if len(issues) > 0 {
					t.Fatalf("unexpected issues: %+v", issues)
				}
				return
			}
			if len(issues) == 0 || issues[0].Code != tc.wantCode {
				t.Fatalf("issues = %+v, want code %q", issues, tc.wantCode)
			}
		})
	}
}
