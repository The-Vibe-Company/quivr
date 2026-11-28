package quivrplugin_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// deadlineRecorder answers an empty ranking and records whether its context
// had a deadline.
type deadlineRecorder struct{ bounded, called bool }

func (d *deadlineRecorder) Search(ctx context.Context, _ *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	_, d.bounded = ctx.Deadline()
	d.called = true
	return &quivrplugin.SearchAnswer{Ranking: []quivrplugin.RankedHit{}}, nil
}

// A profile's max_latency_ms is its latency objective, not a deadline
// (THE-813): the SDK does not cut a round there. The engine ends the request,
// and so the round's context, at the profile's hard bound.
func TestSearchRoundsAreNotCutAtTheObjective(t *testing.T) {
	plugin, err := quivrplugin.New("../examples/fusion-retriever/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &deadlineRecorder{}
	if err = plugin.Retrieval(recorder); err != nil {
		t.Fatal(err)
	}
	handler, err := plugin.Handler()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../../contracts/plugins/v0/fixtures/requests/retrieval/search-round-1.json")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/contributions/retrieval/search", bytes.NewReader(body)))
	if rec.Code != 200 || !recorder.called {
		t.Fatalf("search answered %d %s", rec.Code, rec.Body)
	}
	if recorder.bounded {
		t.Fatal("the SDK gave the round a deadline of its own; only the engine's request bounds it")
	}
}

type composingRetriever struct{ t *testing.T }

func (r composingRetriever) Search(_ context.Context, q *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	if q.Budget == nil || q.Budget.RemainingCostCents != 0.5 {
		r.t.Fatalf("budget %+v", q.Budget)
	}
	if q.Round == 1 {
		return quivrplugin.Ask(quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveProfile, Profile: &quivrplugin.ProfileCandidates{Name: "core.retrieve/default", Limit: 10}}), nil
	}
	c := q.Served[0].Candidates[0]
	if q.Served[0].Request.Profile.Name != "core.retrieve/default" || c.Explanation != "Inner ranking" {
		r.t.Fatalf("served %+v", q.Served)
	}
	return quivrplugin.Rank(quivrplugin.RankedHit{SegmentID: c.SegmentID, Score: c.Score, Explanation: c.Explanation}), nil
}

// The SDK's HTTP boundary owns serialization of a profile request and
// decoding of the engine-supplied explanation and remaining budget.
func TestSDKProfileCandidates(t *testing.T) {
	root := "../../../contracts/plugins/v0/fixtures/"
	manifest, err := os.ReadFile(root + "manifests/valid/profile-candidates.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "quivr-plugin.yaml")
	if err = os.WriteFile(path, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := quivrplugin.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Manifest().Requires) != 1 || p.Manifest().Requires[0].Plugin != "core.retrieve" {
		t.Fatalf("manifest %+v", p.Manifest())
	}
	if err = p.Retrieval(composingRetriever{t}); err != nil {
		t.Fatal(err)
	}
	handler, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []string{"search-profile-budget.json", "search-profile-served.json"} {
		body, err := os.ReadFile(root + "requests/retrieval/" + fixture)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/contributions/retrieval/search", bytes.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", fixture, rec.Code, rec.Body)
		}
		var got map[string]any
		if err = json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if fixture == "search-profile-budget.json" {
			requests := got["requests"].([]any)
			request := requests[0].(map[string]any)
			if request["primitive"] != "profile" || request["k"] != nil || request["profile"].(map[string]any)["limit"] != float64(10) {
				t.Fatalf("request %s", rec.Body)
			}
		} else if got["ranking"].(map[string]any)["hits"].([]any)[0].(map[string]any)["explanation"] != "Inner ranking" {
			t.Fatalf("ranking %s", rec.Body)
		}
	}
}

type richSDKRetriever struct{ t *testing.T }

func (r richSDKRetriever) Search(_ context.Context, q *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	if q.Round == 1 {
		return quivrplugin.Ask(quivrplugin.CandidateRequest{
			Primitive:   quivrplugin.PrimitiveHybrid,
			Space:       q.Spaces[0].ID,
			QueryText:   q.Query.Text,
			K:           250,
			GroupBy:     "record",
			IncludeLead: true,
		}), nil
	}
	c := q.Served[0].Candidates[0]
	if c.Title != "Harbour ferry crews begin stoppage" ||
		c.CommonMetadata == nil || c.CommonMetadata.Language != "en" || c.CommonMetadata.Source != "Harbour Wire" ||
		c.PassageOrdinal == nil || *c.PassageOrdinal != 2 ||
		c.TotalPassages != 5 ||
		c.LeadPassage != "Dockers stopped work at the northern harbour at dawn." {
		r.t.Fatalf("unexpected decoded 0.21 candidate: %+v", c)
	}
	return quivrplugin.Rank(quivrplugin.RankedHit{SegmentID: c.SegmentID, Score: c.Score}), nil
}

// Go SDK enforces Plugin API 0.21 gating for max_candidates > 100 and
// round-trips rich candidate metadata, passage ordinals, and include_lead.
func TestSDKRichRetrievalAndDeepCandidatePools(t *testing.T) {
	root := "../../../contracts/plugins/v0/fixtures/"
	if _, err := quivrplugin.New(root + "manifests/invalid/retrieval-deep-pool-plugin-api-0.20.yaml"); err == nil {
		t.Fatal("expected Plugin API 0.20 manifest with max_candidates: 250 to be rejected")
	}
	p, err := quivrplugin.New(root + "manifests/valid/retrieval-deep-pool.yaml")
	if err != nil {
		t.Fatalf("load 0.21 manifest: %v", err)
	}
	if err = p.Retrieval(richSDKRetriever{t}); err != nil {
		t.Fatal(err)
	}
	handler, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789012345678901")
	ring, err := json.Marshal(map[string]any{
		"active": "k1",
		"keys":   []map[string]any{{"id": "k1", "secret": base64.RawURLEncoding.EncodeToString(secret)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(quivrplugin.EnvSigningKeys, string(ring))
	sign := func(body []byte) string {
		now := time.Now().Unix()
		sum := sha256.Sum256(body)
		h, _ := json.Marshal(map[string]any{"alg": "HS256", "typ": "quivr-engine+jwt", "kid": "k1"})
		c, _ := json.Marshal(map[string]any{
			"aud": p.Manifest().ID, "plugin_id": p.Manifest().ID, "contribution": "retrieval",
			"method": http.MethodPost, "target": "/v0/contributions/retrieval/search",
			"iat": now, "exp": now + 30, "body_sha256": hex.EncodeToString(sum[:]),
		})
		enc := base64.RawURLEncoding.EncodeToString
		input := enc(h) + "." + enc(c)
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(input))
		return "Bearer " + input + "." + enc(mac.Sum(nil))
	}
	round1, err := os.ReadFile(root + "requests/retrieval/search-round-1.json")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/contributions/retrieval/search", bytes.NewReader(round1))
	req.Header.Set("Authorization", sign(round1))
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("round 1: %d %s", rec.Code, rec.Body)
	}
	var ans map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &ans); err != nil {
		t.Fatal(err)
	}
	req0 := ans["requests"].([]any)[0].(map[string]any)
	if req0["k"] != float64(250) || req0["include_lead"] != true {
		t.Fatalf("round 1 answer = %s; want k=250 and include_lead=true", rec.Body)
	}

	round2, err := os.ReadFile(root + "requests/retrieval/search-round-2-rich-candidates.json")
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/contributions/retrieval/search", bytes.NewReader(round2))
	req.Header.Set("Authorization", sign(round2))
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("round 2: %d %s", rec.Code, rec.Body)
	}
}
