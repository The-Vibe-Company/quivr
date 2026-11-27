package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func TestCascadePromotesFromWidePool(t *testing.T) {
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `[{"index":3,"score":0.98},{"index":0,"score":0.8},{"index":1,"score":0.05},{"index":2,"score":0.6}]`)
	}))
	defer secondary.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload struct{ Texts []string }
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || !slices.Equal(payload.Texts, []string{"doc d", "doc a"}) {
			t.Errorf("primary received %v, want [doc d, doc a] (decode: %v)", payload.Texts, err)
		}
		fmt.Fprint(w, `[{"index":1,"score":0.01},{"index":0,"score":0.99}]`)
	}))
	defer primary.Close()

	r := retriever{client: primary.Client()}
	raw := fmt.Sprintf(`{"rerank_endpoint":%q,"rerank_secondary_endpoint":%q,"rerank_top_k":2,"rerank_cascade_k":4,"surrogate_mode":"raw","rerank_medium_margin":0.99}`, primary.URL, secondary.URL)
	cfg, err := readConfig(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	bundles := testRerankBundles("abcde")
	ranked, err := r.rerankBundles(t.Context(), cfg, "query", bundles, 2)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, b := range ranked {
		ids = append(ids, b.BestSeg.SegmentID)
		if i > 0 && ranked[i-1].FinalScore <= b.FinalScore {
			t.Fatalf("tail stitching lost strict score order at rank %d", i+1)
		}
	}
	if want := []string{"d", "a", "b", "c", "e"}; !slices.Equal(ids, want) {
		t.Fatalf("got ranks %v, want %v", ids, want)
	}

	// The medium tier should return the filter ranking even if the primary is unavailable.
	primary.Close()
	cfg.RerankMediumMargin = new(0.9)
	ranked, err = r.rerankBundles(t.Context(), cfg, "query", testRerankBundles("abcde"), 2)
	if err != nil || ranked[0].BestSeg.SegmentID != "d" {
		t.Fatalf("medium tier failed: %v", err)
	}
}

func TestLogitEnsembleStartsBothRequestsAndUsesRawMargins(t *testing.T) {
	primaryStarted, secondaryStarted := make(chan struct{}), make(chan struct{})
	handler := func(started, peerStarted chan struct{}) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			var payload struct{ Texts []string }
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || !slices.Equal(payload.Texts, []string{"doc a", "doc b"}) {
				t.Errorf("ensemble received %v, want [doc a, doc b] (decode: %v)", payload.Texts, err)
			}
			close(started)
			select {
			case <-peerStarted:
				fmt.Fprint(w, `[{"index":1,"score":1,"raw_score":30},{"index":0,"score":1,"raw_score":25}]`)
			case <-req.Context().Done():
			}
		}
	}
	primary := httptest.NewServer(handler(primaryStarted, secondaryStarted))
	defer primary.Close()
	secondary := httptest.NewServer(handler(secondaryStarted, primaryStarted))
	defer secondary.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	r := retriever{client: primary.Client()}
	raw := fmt.Sprintf(`{"rerank_endpoint":%q,"rerank_secondary_endpoint":%q,"rerank_ensemble":"logit","surrogate_mode":"raw"}`, primary.URL, secondary.URL)
	cfg, err := readConfig(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	ranked, err := r.rerankBundles(ctx, cfg, "query", testRerankBundles("abc"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].BestSeg.SegmentID != "b" || ranked[2].BestSeg.SegmentID != "c" {
		t.Fatalf("expected the larger raw margin to win and c to stay in the tail")
	}
}

func testRerankBundles(ids string) []*recordBundle {
	bundles := make([]*recordBundle, 0, len(ids))
	for i := range len(ids) {
		id := ids[i : i+1]
		bundles = append(bundles, &recordBundle{
			BestSeg:    quivrplugin.Candidate{SegmentID: id, Text: "doc " + id},
			FinalScore: float64(len(ids) - i),
			HasBestSeg: true,
		})
	}
	return bundles
}
