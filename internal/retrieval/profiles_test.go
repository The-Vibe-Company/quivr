package retrieval_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// Real HTTP rankers own dispatch, local-profile translation, installer settings,
// version identity and snapshot continuity. A plan change during round one must
// only affect the next search, including a change to the selected deadline.
func TestSearchResolvesProfilesAndPinsAllRounds(t *testing.T) {
	var live *plugins.Live
	var next *plugins.PinSet
	var swap bool
	var sent []struct {
		plugin  string
		request plugins.SearchRequest
	}
	server := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var q plugins.SearchRequest
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			sent = append(sent, struct {
				plugin  string
				request plugins.SearchRequest
			}{id, q})
			if swap && q.Round == 1 {
				swap = false
				if err := live.Store("next", next); err != nil {
					t.Error(err)
				}
			}
			body, err := passthrough(r.Context(), q)
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
	}
	a, b, c := server("normal"), server("careful"), server("upgraded")
	defer a.Close()
	defer b.Close()
	defer c.Close()
	pin := func(id, version, endpoint string) *plugins.Pin {
		t.Helper()
		return &plugins.Pin{Endpoint: endpoint, Configuration: json.RawMessage(`{"setting":"` + id + `"}`), Manifest: plugins.Manifest{ID: id, Version: version, Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
			Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 2000}, "deep": {MaxLatencyMS: 1000}},
			Limits:   plugins.RetrievalLimits{MaxRounds: 3, MaxRequests: 2, MaxCandidates: 50},
		}}}}
	}
	normal, careful := pin("example.normal", "1.0.0", a.URL), pin("example.careful", "1.0.0", b.URL)
	set, err := plugins.NewPinSet([]*plugins.Pin{normal, careful})
	if err != nil {
		t.Fatal(err)
	}
	next, err = plugins.NewPinSet([]*plugins.Pin{normal, pin("example.careful", "2.0.0", c.URL)})
	if err != nil {
		t.Fatal(err)
	}
	live, err = plugins.NewLive("first", set)
	if err != nil {
		t.Fatal(err)
	}
	s := service(&fakeProjection{candidates: []content.Candidate{{SegmentID: "a", GenerationID: "gen"}}}, &fakeEmbeddings{})
	s.ProfilesRouter = pluginhttp.LiveRetriever{Live: live, Aliases: map[string]string{"default": "example.normal/default", "deep": "example.careful/deep"}}
	profiles := s.Profiles()
	if len(profiles) != 4 || profiles[0].FullName != "example.normal/default" || len(profiles[0].Aliases) != 1 || profiles[0].Aliases[0] != "default" {
		t.Fatalf("profiles %+v", profiles)
	}
	for _, tc := range []struct{ profile, plugin, local, version string }{
		{"", "normal", "default", "plugin:example.normal@1.0.0/default"},
		{"balanced", "normal", "default", "plugin:example.normal@1.0.0/default"},
		{"deep", "careful", "deep", "plugin:example.careful@1.0.0/deep"},
		{"example.normal/deep", "normal", "deep", "plugin:example.normal@1.0.0/deep"},
		{"example.careful/default", "careful", "default", "plugin:example.careful@1.0.0/default"},
	} {
		sent = nil
		out, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "library", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: tc.profile})
		if err != nil {
			t.Fatal(err)
		}
		if len(sent) != 2 || sent[0].plugin != tc.plugin || sent[1].plugin != tc.plugin || sent[0].request.Profile != tc.local || out.ProfileVersion != tc.version {
			t.Fatalf("%s: sent %+v result %+v", tc.profile, sent, out)
		}
		wantSetting := `{"setting":"example.` + map[string]string{"normal": "normal", "careful": "careful"}[tc.plugin] + `"}`
		if string(sent[0].request.Configuration) != wantSetting {
			t.Fatalf("%s: configuration %s", tc.profile, sent[0].request.Configuration)
		}
	}
	sent = nil
	if _, err := s.Search(context.Background(), searchScope, retrieval.Request{Profile: "missing"}); !errors.Is(err, retrieval.ErrUnsupportedProfile) || len(sent) != 0 {
		t.Fatalf("unknown profile: %v, calls %v", err, sent)
	}
	swap = true
	sent = nil
	out, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "library", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep"})
	if err != nil || len(sent) != 2 || sent[1].plugin != "careful" || out.ProfileVersion != "plugin:example.careful@1.0.0/deep" {
		t.Fatalf("search crossing activation: %+v, %v, sent %+v", out, err, sent)
	}
	sent = nil
	out, err = s.Search(context.Background(), searchScope, retrieval.Request{Query: "library", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep"})
	if err != nil || len(sent) != 2 || sent[0].plugin != "upgraded" || out.ProfileVersion != "plugin:example.careful@2.0.0/deep" {
		t.Fatalf("search after activation: %+v, %v, sent %+v", out, err, sent)
	}
}
